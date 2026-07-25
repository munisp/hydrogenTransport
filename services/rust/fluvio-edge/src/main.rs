//! fluvio-edge — H2Fleet edge agent (runs ON each bus gateway).
//!
//! Pipeline: Fluvio SPU (`bus-telemetry` topic, local to the gateway)
//!   → batcher (BATCH_MAX records or BATCH_LINGER_MS)
//!   → platform Kafka (`telemetry.raw`, rdkafka, lz4, acks=all)
//!   → store-and-forward spool on disk when the uplink is down
//!     (CRC-framed append-only file, survives restart, FIFO drain on recovery).
//!
//! Honest architecture note (docs/MIDDLEWARE_HARDENING.md): Kafka remains the
//! platform event backbone (SPEC §3.3). Fluvio is the *edge ingestion tier*:
//! on-bus gateways buffer and pre-aggregate telemetry close to the vehicles
//! where connectivity is intermittent; this agent bridges the tiers.

mod config;
mod forwarder;
mod source;
mod spool;

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use anyhow::Result;
use axum::extract::State;
use axum::routing::get;
use axum::{Json, Router};
use serde::Serialize;
use tracing::info;
use tracing_subscriber::EnvFilter;

use crate::config::Config;
use crate::forwarder::ForwardState;

struct AppState {
    cfg: Config,
    fluvio_up: Arc<AtomicBool>,
    fwd: Arc<ForwardState>,
    shutdown: Arc<AtomicBool>,
}

#[derive(Serialize)]
struct Health {
    status: &'static str,
    fluvio_connected: bool,
    kafka_ok: bool,
    last_kafka_ok_unix: i64,
    spool_depth: u64,
    forwarded_total: u64,
    spooled_total: u64,
    fluvio_topic: String,
    kafka_topic: String,
}

async fn healthz(State(s): State<Arc<AppState>>) -> Json<Health> {
    let kafka_ok = s.fwd.kafka_ok.load(Ordering::Relaxed);
    let fluvio_up = s.fluvio_up.load(Ordering::Relaxed);
    Json(Health {
        // The agent's job during an outage is precisely to keep spooling —
        // "degraded" is a normal operating mode at the edge, so healthz stays
        // 200 and reports the components truthfully.
        status: "ok",
        fluvio_connected: fluvio_up,
        kafka_ok,
        last_kafka_ok_unix: s.fwd.last_kafka_ok_unix.load(Ordering::Relaxed),
        spool_depth: s.fwd.spool_depth.load(Ordering::Relaxed),
        forwarded_total: s.fwd.forwarded_total.load(Ordering::Relaxed),
        spooled_total: s.fwd.spooled_total.load(Ordering::Relaxed),
        fluvio_topic: s.cfg.fluvio_topic.clone(),
        kafka_topic: s.cfg.kafka_topic.clone(),
    })
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();
    let cfg = Config::from_env();
    info!(
        fluvio = %cfg.fluvio_endpoint,
        topic = %cfg.fluvio_topic,
        kafka = %cfg.kafka_brokers,
        kafka_topic = %cfg.kafka_topic,
        spool_dir = %cfg.spool_dir,
        "fluvio-edge starting"
    );

    let shutdown = Arc::new(AtomicBool::new(false));
    let fluvio_up = Arc::new(AtomicBool::new(false));
    let fwd = Arc::new(ForwardState::new());

    // Source → forwarder channel. Bounded: full channel backpressures the
    // Fluvio fetch loop instead of growing memory unboundedly.
    let (tx, rx) = tokio::sync::mpsc::channel(cfg.batch_max * 4);

    let src = {
        let cfg = cfg.clone();
        let fluvio_up = fluvio_up.clone();
        let shutdown = shutdown.clone();
        tokio::spawn(async move { source::run(cfg, tx, fluvio_up, shutdown).await })
    };
    let fwd_task = {
        let cfg = cfg.clone();
        let fwd = fwd.clone();
        let shutdown = shutdown.clone();
        tokio::spawn(async move { forwarder::run(cfg, rx, fwd, shutdown).await })
    };

    let app_state = Arc::new(AppState {
        cfg: cfg.clone(),
        fluvio_up,
        fwd,
        shutdown: shutdown.clone(),
    });
    let app = Router::new().route("/healthz", get(healthz)).with_state(app_state);
    let listener = tokio::net::TcpListener::bind(("0.0.0.0", cfg.port)).await?;

    // Graceful shutdown: SIGTERM/SIGINT → stop consuming, flush batch, exit.
    let shutdown_signal = {
        let shutdown = shutdown.clone();
        async move {
            let mut term =
                tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                    .expect("install SIGTERM handler");
            tokio::select! {
                _ = tokio::signal::ctrl_c() => {},
                _ = term.recv() => {},
            }
            shutdown.store(true, Ordering::Relaxed);
        }
    };

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal)
        .await?;

    // Dropping tx (in source task when it observes shutdown) ends the
    // forwarder's channel; both tasks then finish their final flush.
    shutdown.store(true, Ordering::Relaxed);
    let _ = tokio::time::timeout(std::time::Duration::from_secs(10), src).await;
    let _ = tokio::time::timeout(std::time::Duration::from_secs(35), fwd_task).await;
    info!("fluvio-edge stopped");
    Ok(())
}
