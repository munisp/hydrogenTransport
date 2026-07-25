//! Forwarder: batches edge records and produces them to the platform Kafka
//! topic `telemetry.raw`. When the uplink is down the batch is appended to the
//! durable spool instead; a drainer replays spooled records (FIFO) once Kafka
//! acknowledges again. At-least-once semantics: a produce that fails after a
//! partial broker write can duplicate a record — telemetry-ingest dedupes on
//! the CloudEvents `id` (SPEC §3.3).

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{Context, Result};
use futures_util::future::join_all;
use rdkafka::config::ClientConfig;
use rdkafka::producer::{FutureProducer, FutureRecord};
use tokio::sync::mpsc;
use tracing::{error, info, warn};

use crate::config::Config;
use crate::source::EdgeRecord;
use crate::spool::{self, Spool, SpoolRecord};

/// Anything with a Kafka key/value — EdgeRecord (fresh) and SpoolRecord
/// (replayed) both qualify.
pub trait Kv {
    fn key(&self) -> Option<&[u8]>;
    fn value(&self) -> &[u8];
}

impl Kv for EdgeRecord {
    fn key(&self) -> Option<&[u8]> {
        self.key.as_deref()
    }
    fn value(&self) -> &[u8] {
        &self.value
    }
}

impl Kv for SpoolRecord {
    fn key(&self) -> Option<&[u8]> {
        self.key.as_deref()
    }
    fn value(&self) -> &[u8] {
        &self.value
    }
}

/// Shared health/telemetry state surfaced by /healthz.
pub struct ForwardState {
    pub kafka_ok: AtomicBool,
    pub last_kafka_ok_unix: AtomicI64,
    pub spool_depth: AtomicU64,
    pub forwarded_total: AtomicU64,
    pub spooled_total: AtomicU64,
}

impl ForwardState {
    pub fn new() -> Self {
        ForwardState {
            kafka_ok: AtomicBool::new(false),
            last_kafka_ok_unix: AtomicI64::new(0),
            spool_depth: AtomicU64::new(0),
            forwarded_total: AtomicU64::new(0),
            spooled_total: AtomicU64::new(0),
        }
    }
}

fn build_producer(cfg: &Config) -> Result<FutureProducer> {
    ClientConfig::new()
        .set("bootstrap.servers", &cfg.kafka_brokers)
        .set("client.id", "fluvio-edge")
        .set("acks", "all")
        // Micro-batching inside one flush; the platform-side producer tuning
        // lives in infra/tuning/kafka-server.prod.properties.
        .set("linger.ms", "20")
        .set("compression.type", "lz4")
        .set("message.timeout.ms", "30000")
        .create()
        .context("create kafka producer")
}

/// Produce a batch; returns the records that could NOT be confirmed by the
/// broker (they must be spooled). A record whose delivery future failed may
/// still have been written — retrying can duplicate it (at-least-once).
async fn produce_batch<R: Kv>(
    producer: &FutureProducer,
    topic: &str,
    batch: Vec<R>,
) -> (usize, Vec<R>) {
    let futures = batch.iter().map(|rec| {
        let key: &[u8] = rec.key().unwrap_or(&[]);
        producer.send(
            FutureRecord::to(topic).key(key).payload(rec.value()),
            Duration::from_secs(30),
        )
    });
    let results = join_all(futures).await;
    let mut ok = 0usize;
    let mut failed = Vec::new();
    for (rec, res) in batch.into_iter().zip(results) {
        match res {
            Ok(_) => ok += 1,
            Err((e, _)) => {
                warn!(error = %e, "kafka delivery failed; spooling record");
                failed.push(rec);
            }
        }
    }
    (ok, failed)
}

/// Run the forwarding loop: batch from the channel, produce, spool on
/// failure, and continuously drain the spool while Kafka is healthy.
pub async fn run(
    cfg: Config,
    mut rx: mpsc::Receiver<EdgeRecord>,
    state: Arc<ForwardState>,
    shutdown: Arc<AtomicBool>,
) -> Result<()> {
    let producer = build_producer(&cfg)?;
    let spool_dir = PathBuf::from(&cfg.spool_dir);
    let mut spool = Spool::open(&spool_dir)?;
    state.spool_depth.store(spool.len() as u64, Ordering::Relaxed);
    if !spool.is_empty() {
        info!(depth = spool.len(), "recovered spooled records from previous run");
    }

    let mut batch: Vec<EdgeRecord> = Vec::with_capacity(cfg.batch_max);
    let mut flush_at = Instant::now() + cfg.batch_linger;
    let mut drain_tick = tokio::time::interval(Duration::from_secs(2));
    drain_tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

    loop {
        tokio::select! {
            maybe = rx.recv() => {
                match maybe {
                    Some(rec) => {
                        batch.push(rec);
                        if batch.len() >= cfg.batch_max {
                            flush(&cfg, &producer, &mut spool, &state, &mut batch).await;
                            flush_at = Instant::now() + cfg.batch_linger;
                        }
                    }
                    None => {
                        // Source is gone (shutdown): flush what we hold.
                        flush(&cfg, &producer, &mut spool, &state, &mut batch).await;
                        info!("forwarder stopped");
                        return Ok(());
                    }
                }
            }
            _ = tokio::time::sleep_until(flush_at.into()) => {
                if !batch.is_empty() {
                    flush(&cfg, &producer, &mut spool, &state, &mut batch).await;
                }
                flush_at = Instant::now() + cfg.batch_linger;
            }
            _ = drain_tick.tick() => {
                drain_spool(&cfg, &producer, &mut spool, &state).await;
                if shutdown.load(Ordering::Relaxed) && batch.is_empty() && rx.is_empty() {
                    info!("forwarder stopped (shutdown)");
                    return Ok(());
                }
            }
        }
    }
}

/// Try to produce the batch; whatever Kafka does not confirm is spooled
/// durably, then the contiguous Fluvio offset is checkpointed.
async fn flush(
    cfg: &Config,
    producer: &FutureProducer,
    spool: &mut Spool,
    state: &Arc<ForwardState>,
    batch: &mut Vec<EdgeRecord>,
) {
    if batch.is_empty() {
        return;
    }
    let max_offset = batch.iter().map(|r| r.offset).max().unwrap_or(-1);

    // Skip the produce attempt entirely while older data still sits in the
    // spool: forwarding newer records first would break FIFO ordering.
    let attempted = spool.is_empty();
    let (ok, failed) = if attempted {
        let records = std::mem::take(batch);
        produce_batch(producer, &cfg.kafka_topic, records).await
    } else {
        let failed = std::mem::take(batch);
        (0, failed)
    };
    if attempted && failed.is_empty() {
        state.kafka_ok.store(true, Ordering::Relaxed);
        state.last_kafka_ok_unix.store(now_unix(), Ordering::Relaxed);
        state.forwarded_total.fetch_add(ok as u64, Ordering::Relaxed);
    } else if attempted && ok > 0 {
        // Partial success: some delivered, some must be spooled.
        state.kafka_ok.store(false, Ordering::Relaxed);
        state.forwarded_total.fetch_add(ok as u64, Ordering::Relaxed);
    } else if attempted {
        state.kafka_ok.store(false, Ordering::Relaxed);
    }
    if !failed.is_empty() {
        let records: Vec<SpoolRecord> = failed
            .iter()
            .map(|r| SpoolRecord { key: r.key.clone(), value: r.value.clone() })
            .collect();
        match spool.append(&records) {
            Ok(()) => {
                state.spooled_total.fetch_add(records.len() as u64, Ordering::Relaxed);
                state.spool_depth.store(spool.len() as u64, Ordering::Relaxed);
                warn!(count = records.len(), depth = spool.len(), "uplink down; batch spooled");
            }
            Err(e) => error!(error = %e, "SPOOL WRITE FAILED — records may be lost"),
        }
    }
    // Checkpoint the Fluvio offset: every record in this batch is now either
    // delivered or durably spooled, so it is safe to resume after it.
    if max_offset >= 0 {
        if let Err(e) = spool::store_offset(&PathBuf::from(&cfg.spool_dir), max_offset + 1) {
            warn!(error = %e, "failed to checkpoint fluvio offset");
        }
    }
}

/// Replay spooled records while Kafka acknowledges them (FIFO).
async fn drain_spool(
    cfg: &Config,
    producer: &FutureProducer,
    spool: &mut Spool,
    state: &Arc<ForwardState>,
) {
    if spool.is_empty() {
        return;
    }
    if !state.kafka_ok.load(Ordering::Relaxed)
        && now_unix() - state.last_kafka_ok_unix.load(Ordering::Relaxed) < 5
    {
        return; // uplink recently failed; wait for a fresh successful flush
    }
    let chunk = spool.peek(cfg.batch_max);
    if chunk.is_empty() {
        return;
    }
    let n = chunk.len();
    let (ok, failed) = produce_batch(producer, &cfg.kafka_topic, chunk).await;
    if failed.is_empty() {
        state.kafka_ok.store(true, Ordering::Relaxed);
        state.last_kafka_ok_unix.store(now_unix(), Ordering::Relaxed);
        state.forwarded_total.fetch_add(ok as u64, Ordering::Relaxed);
        if let Err(e) = spool.ack(n) {
            error!(error = %e, "spool ack/compaction failed");
        }
        state.spool_depth.store(spool.len() as u64, Ordering::Relaxed);
        info!(drained = n, remaining = spool.len(), "spool drain progress");
    } else {
        state.kafka_ok.store(false, Ordering::Relaxed);
        if ok > 0 {
            // Ack the prefix that did go through to keep the spool honest.
            if let Err(e) = spool.ack(ok) {
                error!(error = %e, "spool partial ack failed");
            }
            state.forwarded_total.fetch_add(ok as u64, Ordering::Relaxed);
        }
        state.spool_depth.store(spool.len() as u64, Ordering::Relaxed);
    }
}

fn now_unix() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}
