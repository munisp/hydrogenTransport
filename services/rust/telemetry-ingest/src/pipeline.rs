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
use crate::model::{DlqRecord, Envelope, OutEnvelope, TelemetryEnriched, TelemetryRaw};
use crate::store;
use crate::toggles::ToggleGate;

const SERVICE_NAME: &str = "telemetry-ingest";

/// Max TimescaleDB insert attempts per batch before the batch is dead-lettered.
/// With the backoff schedule below (2s doubling, capped at 60s) this spans
/// roughly five minutes of retrying before giving up.
const MAX_FLUSH_ATTEMPTS: u32 = 10;

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
                        metrics::counter!("telemetry_records_consumed_total").increment(1);
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
    let mut data = env.data;
    if let Err(err) = data.validate() {
        tracing::warn!(error = %err, event_id = %env.id, "dropping implausible telemetry record");
        return None;
    }
    // Wave 5 compat: legacy h2-only payloads get the generic energy fields
    // mirrored in before persistence/republish (mixed/non-h2 payloads are
    // passed through untouched).
    data.normalize_energy_fields();
    Some(data)
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

    // --- durable write with bounded retry; on permanent failure the batch is
    // published to the DLQ and offsets ARE committed so the pipeline never
    // wedges on a poison/outage batch (see MAX_FLUSH_ATTEMPTS) ---
    let mut backoff = Duration::from_secs(2);
    let mut attempt: u32 = 0;
    let mut last_err = String::new();
    let rows = loop {
        attempt += 1;
        match store::insert_batch(pool, &enriched).await {
            Ok(rows) => break Some(rows),
            Err(err) => {
                last_err = err.to_string();
                if attempt >= MAX_FLUSH_ATTEMPTS {
                    tracing::error!(
                        error = %err, size = enriched.len(), attempts = attempt,
                        dlq_topic = %cfg.dlq_topic,
                        "timescale insert failed permanently; dead-lettering batch"
                    );
                    break None;
                }
                tracing::error!(error = %err, size = enriched.len(), attempt, max_attempts = MAX_FLUSH_ATTEMPTS, "timescale insert failed; retrying");
                tokio::time::sleep(backoff).await;
                backoff = (backoff * 2).min(Duration::from_secs(60));
            }
        }
    };

    let Some(rows) = rows else {
        dead_letter(cfg, producer, &enriched, &last_err, attempt).await;
        metrics::counter!("telemetry_records_dlq_total").increment(batch.len() as u64);
        commit_batch_offsets(consumer, &cfg.input_topic, batch);
        tracing::error!(size = batch.len(), "batch dead-lettered; offsets committed");
        batch.clear();
        return;
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
    commit_batch_offsets(consumer, &cfg.input_topic, batch);

    metrics::counter!("telemetry_records_written_total").increment(rows);
    tracing::debug!(rows, batch = batch.len(), "batch flushed");
    batch.clear();
}

/// Commit the highest offset (+1) per partition for a consumed batch.
fn commit_batch_offsets(consumer: &StreamConsumer, topic: &str, batch: &[Pending]) {
    let mut tpl = TopicPartitionList::new();
    let mut high: std::collections::HashMap<i32, i64> = std::collections::HashMap::new();
    for p in batch.iter() {
        high.entry(p.partition)
            .and_modify(|o| *o = (*o).max(p.offset))
            .or_insert(p.offset);
    }
    for (partition, offset) in high {
        tpl.add_partition_offset(topic, partition, Offset::Offset(offset + 1))
            .ok();
    }
    if let Err(err) = consumer.commit(&tpl, CommitMode::Async) {
        tracing::warn!(error = %err, "offset commit failed (will redeliver)");
    }
}

/// Publish every record of a permanently failed batch to the DLQ topic
/// (best effort; publish failures are logged, not retried).
async fn dead_letter(
    cfg: &Config,
    producer: &FutureProducer,
    enriched: &[TelemetryEnriched],
    error: &str,
    attempts: u32,
) {
    for rec in enriched {
        let out = OutEnvelope {
            id: Uuid::new_v4(),
            kind: cfg.dlq_topic.as_str(),
            source: SERVICE_NAME,
            time: Utc::now(),
            data: DlqRecord { record: rec, error, attempts },
        };
        let payload = match serde_json::to_vec(&out) {
            Ok(p) => p,
            Err(err) => {
                tracing::error!(error = %err, "serialize DLQ event failed");
                continue;
            }
        };
        let key = rec.raw.bus_id.to_string();
        let record = FutureRecord::to(cfg.dlq_topic.as_str()).key(&key).payload(&payload);
        if let Err((err, _)) = producer.send(record, Duration::from_secs(5)).await {
            tracing::error!(error = %err, bus_id = %key, topic = %cfg.dlq_topic, "DLQ publish failed");
        }
    }
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
