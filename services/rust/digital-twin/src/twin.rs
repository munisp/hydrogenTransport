//! Twin engine: consumes telemetry.enriched, maintains Redis hot state,
//! publishes twin.updated, and periodically snapshots to Postgres.

use std::time::Duration;

use anyhow::Context;
use chrono::Utc;
use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::message::Message;
use rdkafka::producer::{FutureProducer, FutureRecord};
use redis::aio::MultiplexedConnection;
use redis::AsyncCommands;
use sqlx::PgPool;
use tokio::sync::watch;
use uuid::Uuid;

use crate::config::Config;
use crate::model::{Envelope, OutEnvelope, TelemetryEnriched, TwinState, TwinUpdated};
use crate::toggles::ToggleGate;

const SERVICE_NAME: &str = "digital-twin";
pub const TWIN_KEY_PREFIX: &str = "twin:";
pub const TWIN_INDEX_KEY: &str = "twin:buses";

/// Hot-state update loop. While the module is disabled the subscription is
/// paused (heartbeats continue) and hot state is left untouched.
pub async fn run_engine(
    cfg: Config,
    redis: MultiplexedConnection,
    producer: FutureProducer,
    consumer: StreamConsumer,
    gate: ToggleGate,
    mut shutdown: watch::Receiver<bool>,
) -> anyhow::Result<()> {
    consumer
        .subscribe(&[cfg.input_topic.as_str()])
        .context("subscribe")?;
    let mut redis = redis;
    let mut paused = false;

    loop {
        if !gate.is_enabled() {
            if !paused {
                if let Ok(tpl) = consumer.assignment() {
                    let _ = consumer.pause(&tpl);
                }
                paused = true;
                tracing::info!("digital-twin disabled; consumer paused");
            }
            let _ = tokio::time::timeout(Duration::from_millis(1000), consumer.recv()).await;
            if *shutdown.borrow() {
                break;
            }
            continue;
        }
        if paused {
            if let Ok(tpl) = consumer.assignment() {
                let _ = consumer.resume(&tpl);
            }
            paused = false;
            tracing::info!("digital-twin enabled; consumer resumed");
        }

        let msg = tokio::select! {
            biased;
            _ = shutdown.changed() => break,
            m = tokio::time::timeout(Duration::from_millis(500), consumer.recv()) => m,
        };

        let km = match msg {
            Ok(Ok(km)) => km,
            Ok(Err(err)) => {
                tracing::warn!(error = %err, "kafka consume error");
                tokio::time::sleep(Duration::from_millis(500)).await;
                continue;
            }
            Err(_) => continue, // poll timeout
        };

        let Some(payload) = km.payload().map(|p| p.to_vec()) else { continue };

        match serde_json::from_slice::<Envelope<TelemetryEnriched>>(&payload) {
            Ok(env) if env.kind == cfg.input_topic => {
                tracing::trace!(event_id = %env.id, source = %env.source, event_time = %env.time, "twin update");
                if let Err(err) = apply_update(&cfg, &mut redis, &producer, &env.data).await {
                    tracing::error!(error = %err, "twin update failed; offset not committed");
                    tokio::time::sleep(Duration::from_millis(500)).await;
                    continue;
                }
            }
            Ok(_) => tracing::warn!("dropping envelope with unexpected type"),
            Err(err) => tracing::warn!(error = %err, "dropping malformed envelope"),
        }
        let _ = consumer.commit_message(&km, CommitMode::Async);
    }
    Ok(())
}

/// Apply one telemetry update to the hot twin and broadcast twin.updated.
async fn apply_update(
    cfg: &Config,
    redis: &mut MultiplexedConnection,
    producer: &FutureProducer,
    t: &TelemetryEnriched,
) -> anyhow::Result<()> {
    let state = TwinState::from_telemetry(t);
    let key = format!("{}{}", TWIN_KEY_PREFIX, state.bus_id);
    let json = serde_json::to_string(&state).context("serialize twin")?;
    let ttl = cfg.twin_ttl.as_secs() as i64;

    redis::pipe()
        .cmd("SET")
        .arg(&key)
        .arg(&json)
        .arg("EX")
        .arg(ttl)
        .ignore()
        .cmd("SADD")
        .arg(TWIN_INDEX_KEY)
        .arg(state.bus_id.to_string())
        .ignore()
        .query_async::<()>(redis)
        .await
        .context("write twin hot state")?;

    let event = OutEnvelope {
        id: Uuid::new_v4(),
        kind: cfg.output_topic.as_str(),
        source: SERVICE_NAME,
        time: Utc::now(),
        data: TwinUpdated { bus_id: state.bus_id, ts: state.ts, state },
    };
    let payload = serde_json::to_vec(&event).context("serialize twin.updated")?;
    let key = t.bus_id.to_string();
    let record = FutureRecord::to(cfg.output_topic.as_str()).key(&key).payload(&payload);
    if let Err((err, _)) = producer.send(record, Duration::from_secs(5)).await {
        // Hot state is already updated; a missed notification is non-fatal.
        tracing::warn!(error = %err, "twin.updated publish failed");
    }
    Ok(())
}

/// Periodic snapshot of every hot twin into `fleet.twin_snapshots`.
pub async fn run_snapshots(
    cfg: Config,
    pool: PgPool,
    redis: MultiplexedConnection,
    gate: ToggleGate,
    mut shutdown: watch::Receiver<bool>,
) {
    let mut redis = redis;
    let mut ticker = tokio::time::interval(cfg.snapshot_interval);
    ticker.tick().await; // first tick is immediate; skip it
    loop {
        tokio::select! {
            biased;
            _ = shutdown.changed() => break,
            _ = ticker.tick() => {},
        }
        if !gate.is_enabled() {
            continue;
        }
        match snapshot_once(&pool, &mut redis).await {
            Ok(n) if n > 0 => tracing::info!(snapshots = n, "twin snapshots written"),
            Ok(_) => {},
            Err(err) => tracing::error!(error = %err, "twin snapshot failed"),
        }
    }
}

async fn snapshot_once(pool: &PgPool, redis: &mut MultiplexedConnection) -> anyhow::Result<usize> {
    let bus_ids: Vec<String> = redis.smembers(TWIN_INDEX_KEY).await.context("smembers twin:buses")?;
    if bus_ids.is_empty() {
        return Ok(0);
    }
    let keys: Vec<String> = bus_ids.iter().map(|b| format!("{}{}", TWIN_KEY_PREFIX, b)).collect();
    let values: Vec<Option<String>> = redis.get(keys).await.context("mget twins")?;

    let mut ids: Vec<Uuid> = Vec::new();
    let mut states: Vec<serde_json::Value> = Vec::new();
    let mut stale: Vec<String> = Vec::new();
    for (bus, val) in bus_ids.iter().zip(values) {
        match val {
            Some(json) => match (Uuid::parse_str(bus), serde_json::from_str::<serde_json::Value>(&json)) {
                (Ok(id), Ok(state)) => {
                    ids.push(id);
                    states.push(state);
                }
                _ => stale.push(bus.clone()),
            },
            None => stale.push(bus.clone()), // TTL expired: prune from index
        }
    }
    if !stale.is_empty() {
        let _: () = redis.srem(TWIN_INDEX_KEY, stale).await.unwrap_or(());
    }
    if ids.is_empty() {
        return Ok(0);
    }

    let rows = sqlx::query(
        r#"
        INSERT INTO fleet.twin_snapshots (bus_id, ts, state)
        SELECT u.bus_id, now(), u.state
        FROM unnest($1::uuid[], $2::jsonb[]) AS u(bus_id, state)
        "#,
    )
    .bind(&ids)
    .bind(&states)
    .execute(pool)
    .await
    .context("insert fleet.twin_snapshots")?
    .rows_affected();
    Ok(rows as usize)
}
