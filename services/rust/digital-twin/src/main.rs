//! digital-twin — per-bus hot twin state (Redis) + snapshots (Postgres) + read API.
//! Gated on the `digital-twin` module toggle.

mod api;
mod config;
mod model;
mod toggles;
mod twin;

use std::net::SocketAddr;
use std::sync::Arc;

use anyhow::Context;
use rdkafka::consumer::StreamConsumer;
use rdkafka::producer::FutureProducer;
use rdkafka::ClientConfig;
use sqlx::postgres::PgPoolOptions;

use crate::api::AppState;
use crate::config::Config;
use crate::toggles::ToggleGate;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,digital_twin=debug".into()),
        )
        .init();

    let cfg = Config::from_env();
    tracing::info!(?cfg, "starting digital-twin");

    let pool = PgPoolOptions::new()
        .max_connections(4)
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
        .set("auto.offset.reset", "latest")
        .set("max.poll.interval.ms", "600000")
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

    // Toggle poller.
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

    // Snapshot loop.
    tokio::spawn(twin::run_snapshots(
        cfg.clone(),
        pool.clone(),
        redis.clone(),
        gate.clone(),
        shutdown_rx.clone(),
    ));

    // HTTP API (read + health).
    let app = api::router(Arc::new(AppState { redis: redis.clone(), gate: gate.clone() }));
    let addr = SocketAddr::from(([0, 0, 0, 0], cfg.port));
    let listener = tokio::net::TcpListener::bind(addr).await.context("bind api port")?;
    let mut shutdown_api = shutdown_rx.clone();
    tokio::spawn(async move {
        tracing::info!(%addr, "api listening");
        let _ = axum::serve(listener, app)
            .with_graceful_shutdown(async move {
                let _ = shutdown_api.changed().await;
            })
            .await;
    });

    // Graceful shutdown on SIGINT/SIGTERM.
    tokio::spawn(async move {
        use tokio::signal::unix::{signal, SignalKind};
        let mut sigterm = signal(SignalKind::terminate()).expect("install SIGTERM handler");
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {},
            _ = sigterm.recv() => {},
        }
        let _ = shutdown_tx.send(true);
    });

    twin::run_engine(cfg, redis, producer, consumer, gate, shutdown_rx).await?;
    tracing::info!("digital-twin stopped");
    Ok(())
}
