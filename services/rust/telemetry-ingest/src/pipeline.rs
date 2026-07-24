//! Consume -> validate -> enrich -> batch-insert -> republish pipeline.
//!
//! Backpressure: the consumer stops polling while a batch is flushed, so Kafka
//! flow control and `max.poll.interval.ms` provide natural backpressure; there
//! is no unbounded in-memory queue. Offsets are committed only after the batch
//! is durably written to TimescaleDB (at-least-once).

use std::time::{Duration, Instant};

use anyhow::Context;
use chrono::Utc;
use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::message::Message;
use rdkafka::producer::{FutureProducer, FutureRecord};
use rdkafka::{Offset, TopicPartitionList};
use redis::aio::MultiplexedConnection;
use sqlx::PgPool;
use tokio::sync::watch;
use uuid::Uuid;

use crate::config::Config;
use crate::model::{Envelope, OutEnvelope, TelemetryEnriched, TelemetryRaw};
use crate::store;
use crate::toggles::ToggleGate;

const SERVICE_NAME: &str = "telemetry-ingest";

struct Pending {
    raw: TelemetryRaw,
    partition: i32,
    offset: i64,
}

pub async fn run(
    cfg: Config,
    pool: PgPool,
    redis: MultiplexedConnection,
    consumer: StreamConsumer,
    producer: FutureProducer,
    gate: ToggleGate,
    mut shutdown: watch::Receiver<bool>,
) -> anyhow::Result<()> {
    consumer
        .subscribe(&[cfg.input_topic.as_str()])
        .context("subscribe")?;

    let mut redis = redis;
    let mut batch: Vec<Pending> = Vec::with_capacity(cfg.batch_size);
    let mut batch_started: Option<Instant> = None;
    let mut paused = false;

    loop {
        // --- toggle gate: pause/resume the subscription ---
        if !gate.is_enabled() {
            if !paused {
                if let Ok(tpl) = consumer.assignment() {
                    let _ = consumer.pause(&tpl);
                }
                paused = true;
                tracing::info!(module = %cfg.toggle_module, "module disabled; consumer paused");
            }
            // Keep polling while paused so group membership is maintained.
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
            tracing::info!(module = %cfg.toggle_module, "module enabled; consumer resumed");
        }

        // --- poll with deadline = remaining batch window ---
        let wait = match batch_started {
            Some(started) if !batch.is_empty() => cfg
                .batch_max_wait
                .checked_sub(started.elapsed())
                .unwrap_or(Duration::ZERO),
            _ => cfg.batch_max_wait,
        };

        let msg = tokio::select! {
            biased;
            _ = shutdown.changed() => {
                if *shutdown.borrow() {
                    tracing::info!("shutdown: flushing {} buffered records", batch.len());
                    flush(&cfg, &pool, &mut redis, &producer, &consumer, &mut batch).await;
                    break;
                }
                continue;
            }
            m = tokio::time::timeout(wait.max(Duration::from_millis(50)), consumer.recv()) => m,
        };

        match msg {
            Ok(Ok(km)) => {
                let payload = match km.payload() {
                    Some(p) => p.to_vec(),
                    None => continue,
                };
                let partition = km.partition();
                let offset = km.offset();
                match parse_and_validate(&payload, &cfg.input_topic) {
                    Some(raw) => {
                        if batch.is_empty() {
                            batch_started = Some(Instant::now());
                        }
                        batch.push(Pending { raw, partition, offset });
                    }
                    None => {
                        // Poison record: skip but still commit so we don't wedge.
                        commit_one(&consumer, &cfg.input_topic, partition, offset);
                    }
                }
            }
            Ok(Err(err)) => {
                tracing::warn!(error = %err, "kafka consume error");
                tokio::time::sleep(Duration::from_millis(500)).await;
            }
            Err(_) => { /* poll timeout: fall through to flush check */ }
        }

        let deadline_hit = batch_started
            .map(|s| s.elapsed() >= cfg.batch_max_wait)
            .unwrap_or(false);
        if batch.len() >= cfg.batch_size || (!batch.is_empty() && deadline_hit) {
            flush(&cfg, &pool, &mut redis, &producer, &consumer, &mut batch).await;
            batch_started = None;
        }
    }
    Ok(())
}

/// Parse the envelope and validate plausibility; None => poison record.
fn parse_and_validate(payload: &[u8], expected_topic: &str) -> Option<TelemetryRaw> {
    let env: Envelope<TelemetryRaw> = match serde_json::from_slice(payload) {
        Ok(e) => e,
        Err(err) => {
            tracing::warn!(error = %err, "dropping malformed envelope");
            return None;
        }
    };
    if env.kind != expected_topic {
        tracing::warn!(kind = %env.kind, source = %env.source, "dropping envelope with unexpected type");
        return None;
    }
    tracing::trace!(event_id = %env.id, source = %env.source, event_time = %env.time, "accepted telemetry record");
    if let Err(err) = env.data.validate() {
        tracing::warn!(error = %err, event_id = %env.id, "dropping implausible telemetry record");
        return None;
    }
    Some(env.data)
}

async fn flush(
    cfg: &Config,
    pool: &PgPool,
    redis: &mut MultiplexedConnection,
    producer: &FutureProducer,
    consumer: &StreamConsumer,
    batch: &mut Vec<Pending>,
) {
    if batch.is_empty() {
        return;
    }
    let raws: Vec<TelemetryRaw> = batch.iter().map(|p| p.raw.clone()).collect();
    let enriched: Vec<TelemetryEnriched> = store::enrich_batch(redis, &raws).await;

    // --- durable write with bounded retry; offsets are NOT committed on failure ---
    let mut backoff = Duration::from_millis(200);
    let rows = loop {
        match store::insert_batch(pool, &enriched).await {
            Ok(rows) => break rows,
            Err(err) => {
                tracing::error!(error = %err, size = enriched.len(), "timescale insert failed; retrying");
                tokio::time::sleep(backoff).await;
                backoff = (backoff * 2).min(Duration::from_secs(30));
            }
        }
    };

    // --- republish telemetry.enriched (best effort, logged on failure) ---
    for rec in &enriched {
        let out = OutEnvelope {
            id: Uuid::new_v4(),
            kind: cfg.output_topic.as_str(),
            source: SERVICE_NAME,
            time: Utc::now(),
            data: rec.clone(),
        };
        let payload = match serde_json::to_vec(&out) {
            Ok(p) => p,
            Err(err) => {
                tracing::error!(error = %err, "serialize enriched event failed");
                continue;
            }
        };
        let key = rec.raw.bus_id.to_string();
        let record = FutureRecord::to(cfg.output_topic.as_str())
            .key(&key)
            .payload(&payload);
        if let Err((err, _)) = producer.send(record, Duration::from_secs(5)).await {
            tracing::error!(error = %err, bus_id = %key, "republish to telemetry.enriched failed");
        }
    }

    // --- commit offsets (highest offset per partition, +1) ---
    let mut tpl = TopicPartitionList::new();
    let mut high: std::collections::HashMap<i32, i64> = std::collections::HashMap::new();
    for p in batch.iter() {
        high.entry(p.partition)
            .and_modify(|o| *o = (*o).max(p.offset))
            .or_insert(p.offset);
    }
    for (partition, offset) in high {
        tpl.add_partition_offset(cfg.input_topic.as_str(), partition, Offset::Offset(offset + 1))
            .ok();
    }
    if let Err(err) = consumer.commit(&tpl, CommitMode::Async) {
        tracing::warn!(error = %err, "offset commit failed (will redeliver)");
    }

    tracing::debug!(rows, batch = batch.len(), "batch flushed");
    batch.clear();
}

fn commit_one(consumer: &StreamConsumer, topic: &str, partition: i32, offset: i64) {
    let mut tpl = TopicPartitionList::new();
    if tpl
        .add_partition_offset(topic, partition, Offset::Offset(offset + 1))
        .is_ok()
    {
        let _ = consumer.commit(&tpl, CommitMode::Async);
    }
}
