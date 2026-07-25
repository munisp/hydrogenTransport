//! Fluvio edge source: consumes `bus-telemetry` records from the on-gateway
//! Fluvio SPU and pushes them into the forwarding pipeline. Reconnects with
//! backoff on SPU/SC failures and resumes from the last persisted offset.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result};
use futures_util::StreamExt;
use tokio::sync::mpsc;
use tracing::{error, info, warn};

use crate::config::Config;
use crate::spool;

/// A raw record handed from the Fluvio source to the forwarder.
#[derive(Debug, Clone)]
pub struct EdgeRecord {
    pub key: Option<Vec<u8>>,
    pub value: Vec<u8>,
    /// Fluvio offset this record was read at; persisted after the batch
    /// containing it has been accepted by the pipeline.
    pub offset: i64,
}

/// Run the Fluvio consumer loop until `shutdown` flips. Sends records into
/// `tx`; applies backpressure by simply not polling when the channel is full.
pub async fn run(
    cfg: Config,
    tx: mpsc::Sender<EdgeRecord>,
    fluvio_up: Arc<AtomicBool>,
    shutdown: Arc<AtomicBool>,
) {
    let spool_dir = PathBuf::from(&cfg.spool_dir);
    let mut backoff = Duration::from_millis(200);
    while !shutdown.load(Ordering::Relaxed) {
        match consume(&cfg, &tx, &fluvio_up, &shutdown, &spool_dir).await {
            Ok(()) => {
                // Clean end of stream (e.g. leadership change): reconnect soon.
                backoff = Duration::from_millis(200);
            }
            Err(e) => {
                fluvio_up.store(false, Ordering::Relaxed);
                error!(error = %e, backoff_ms = backoff.as_millis() as u64, "fluvio consumer error; retrying");
                tokio::time::sleep(backoff).await;
                backoff = (backoff * 2).min(Duration::from_secs(15));
            }
        }
    }
}

async fn consume(
    cfg: &Config,
    tx: &mpsc::Sender<EdgeRecord>,
    fluvio_up: &Arc<AtomicBool>,
    shutdown: &Arc<AtomicBool>,
    spool_dir: &std::path::Path,
) -> Result<()> {
    let cluster = fluvio::config::FluvioConfig::new(&cfg.fluvio_endpoint);
    let fluvio = fluvio::Fluvio::connect_with_config(&cluster)
        .await
        .with_context(|| format!("connect fluvio SC at {}", cfg.fluvio_endpoint))?;
    let consumer = fluvio
        .partition_consumer(&cfg.fluvio_topic, cfg.fluvio_partition)
        .await
        .with_context(|| format!("consumer for topic {}", cfg.fluvio_topic))?;

    // Resume from the last offset that made it into the pipeline; if no state
    // exists, start at the beginning (edge topics are compacted regularly).
    let start = match spool::load_offset(spool_dir) {
        Some(next) => fluvio::Offset::absolute(next).context("invalid persisted offset")?,
        None => fluvio::Offset::beginning(),
    };
    info!(topic = %cfg.fluvio_topic, partition = cfg.fluvio_partition, "fluvio consumer started");
    fluvio_up.store(true, Ordering::Relaxed);

    let mut stream = consumer.stream(start).await?;
    loop {
        if shutdown.load(Ordering::Relaxed) {
            return Ok(());
        }
        let next = tokio::time::timeout(Duration::from_secs(30), stream.next()).await;
        let record = match next {
            Ok(Some(Ok(record))) => record,
            Ok(Some(Err(e))) => return Err(e).context("fluvio stream error"),
            Ok(None) => {
                warn!("fluvio stream ended");
                return Ok(());
            }
            Err(_) => continue, // idle poll interval: re-check shutdown
        };
        let offset = record.offset();
        let rec = EdgeRecord {
            key: record.key().map(|k| k.to_vec()),
            value: record.value().to_vec(),
            offset,
        };
        // Blocks when the pipeline is full → backpressure onto the SPU fetch.
        if tx.send(rec).await.is_err() {
            return Ok(()); // forwarder gone: shutting down
        }
        // Persist next-offset best-effort; the forwarder also checkpoints
        // batch-contiguous offsets, so this file is only a resume hint.
        if offset % 64 == 0 {
            if let Err(e) = spool::store_offset(spool_dir, offset + 1) {
                warn!(error = %e, "failed to persist fluvio offset hint");
            }
        }
    }
}
