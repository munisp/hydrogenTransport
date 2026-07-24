//! telemetry-ingest — consumes `telemetry.raw`, validates/enriches, writes
//! TimescaleDB `fleet.telemetry`, republishes `telemetry.enriched`.
//! Gated on the `telematics` module toggle. HTTP surface: /healthz only.

mod config;
mod model;
mod pipeline;
mod store;
mod toggles;

use std::net::SocketAddr;
use std::sync::Arc;

use anyhow::Context;
use axum::extract::State;
use axum::http::StatusCode;
use axum::response::Json;
use axum::routing::get;
use axum::Router;
use rdkafka::consumer::StreamConsumer;
use rdkafka::producer::FutureProducer;
use rdkafka::ClientConfig;
use sqlx::postgres::PgPoolOptions;

use crate::config::Config;
use crate::toggles::ToggleGate;

#[derive(Clone)]
struct HealthState {
    pool: sqlx::PgPool,
    gate: ToggleGate,
}

async fn healthz(State(state): State<Arc<HealthState>>) -> (StatusCode, Json<serde_json::Value>) {
    let db_ok = sqlx::query("SELECT 1").execute(&state.pool).await.is_ok();
    let status = if db_ok { StatusCode::OK } else { StatusCode::SERVICE_UNAVAILABLE };
    (
        status,
        Json(serde_json::json!({
            "status": if db_ok { "ok" } else { "degraded" },
            "service": "telemetry-ingest",
            "module": "telematics",
            "enabled": state.gate.is_enabled(),
            "db": db_ok,
        })),
    )
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,telemetry_ingest=debug".into()),
        )
        .init();

    let cfg = Config::from_env();
    tracing::info!(?cfg, "starting telemetry-ingest");

    let pool = PgPoolOptions::new()
        .max_connections(8)
        .connect(&cfg.database_url)
        .await
        .context("connect postgres")?;

    let redis_client = redis::Client::open(cfg.redis_url()).context("redis client")?;
    let redis = redis_client
        .get_multiplexed_async_connection()
        .await
        .context("connect redis")?;

    let consumer: StreamConsumer = ClientConfig::new()
        .set("group.id", &cfg.kafka_group_id)
        .set("bootstrap.servers", &cfg.kafka_brokers)
        .set("enable.auto.commit", "false")
        .set("enable.auto.offset.store", "false")
        .set("auto.offset.reset", "earliest")
        .set("max.poll.interval.ms", "600000")
        .set("session.timeout.ms", "45000")
        .create()
        .context("create kafka consumer")?;

    let producer: FutureProducer = ClientConfig::new()
        .set("bootstrap.servers", &cfg.kafka_brokers)
        .set("message.timeout.ms", "10000")
        .set("acks", "all")
        .create()
        .context("create kafka producer")?;

    let gate = ToggleGate::new();
    let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

    // Prometheus recorder; /metrics is scraped per infra/observability/prometheus.yml.
    let prom_recorder = metrics_exporter_prometheus::PrometheusBuilder::new().build_recorder();
    let prom_handle = prom_recorder.handle();
    metrics::set_global_recorder(prom_recorder).context("install prometheus recorder")?;

    // Toggle poller (every 10s by default).
    {
        let gate = gate.clone();
        let url = cfg.toggle_url.clone();
        let module = cfg.toggle_module.clone();
        let interval = cfg.toggle_poll_interval;
        let mut shutdown = shutdown_rx.clone();
        tokio::spawn(async move {
            tokio::select! {
                _ = toggles::run_poller(gate, url, module, interval) => {},
                _ = shutdown.changed() => {},
            }
        });
    }

    // Health + metrics endpoints (only HTTP surface).
    let health = Router::new()
        .route("/healthz", get(healthz))
        .route(
            "/metrics",
            get(move || {
                let handle = prom_handle.clone();
                async move { handle.render() }
            }),
        )
        .with_state(Arc::new(HealthState { pool: pool.clone(), gate: gate.clone() }));
    let addr = SocketAddr::from(([0, 0, 0, 0], cfg.port));
    let listener = tokio::net::TcpListener::bind(addr).await.context("bind health port")?;
    let mut shutdown_rx_health = shutdown_rx.clone();
    tokio::spawn(async move {
        tracing::info!(%addr, "health endpoint listening");
        let _ = axum::serve(listener, health)
            .with_graceful_shutdown(async move {
                let _ = shutdown_rx_health.changed().await;
            })
            .await;
    });

    // Graceful shutdown on SIGINT/SIGTERM.
    tokio::spawn(async move {
        shutdown_signal().await;
        let _ = shutdown_tx.send(true);
    });

    pipeline::run(cfg, pool, redis, consumer, producer, gate, shutdown_rx).await?;
    tracing::info!("telemetry-ingest stopped");
    Ok(())
}

async fn shutdown_signal() {
    use tokio::signal::unix::{signal, SignalKind};
    let mut sigterm = signal(SignalKind::terminate()).expect("install SIGTERM handler");
    tokio::select! {
        _ = tokio::signal::ctrl_c() => {},
        _ = sigterm.recv() => {},
    }
}
