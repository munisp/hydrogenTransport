//! Store-and-forward spool: an append-only, CRC-framed file queue that lets
//! the agent survive uplink outages and restarts without losing records.
//!
//! On-disk format (`spool.data`):
//!   frame := [u32 crc32][u32 len][payload]
//!   payload := [u8 has_key][u32 key_len][key bytes][u32 value_len][value bytes]
//!
//! Unacked frames are replayed into memory at startup. Frames are removed from
//! the front only after Kafka has acknowledged them; the file is compacted
//! (rewrite + rename) once the acknowledged prefix exceeds half the file, so
//! steady-state write amplification stays ~2x while a full rewrite per drain
//! is avoided. A corrupted tail frame (crash mid-write) is detected via the
//! CRC/length check and truncated.

use std::collections::VecDeque;
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufReader, Read, Write};
use std::path::{Path, PathBuf};

use anyhow::{bail, Context, Result};

const SPOOL_FILE: &str = "spool.data";
const SPOOL_TMP: &str = "spool.tmp";

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SpoolRecord {
    pub key: Option<Vec<u8>>,
    pub value: Vec<u8>,
}

pub struct Spool {
    dir: PathBuf,
    /// In-memory queue of unacked records (mirrors the on-disk tail).
    queue: VecDeque<SpoolRecord>,
    /// Number of records acknowledged but not yet compacted away.
    acked_pending_compact: usize,
    /// Byte size of the on-disk file.
    file_bytes: u64,
    writer: File,
}

fn encode(rec: &SpoolRecord) -> Vec<u8> {
    let key_len = rec.key.as_ref().map(|k| k.len()).unwrap_or(0) as u32;
    let mut payload = Vec::with_capacity(1 + 4 + key_len as usize + 4 + rec.value.len());
    payload.push(if rec.key.is_some() { 1 } else { 0 });
    payload.extend_from_slice(&key_len.to_le_bytes());
    if let Some(k) = &rec.key {
        payload.extend_from_slice(k);
    }
    payload.extend_from_slice(&(rec.value.len() as u32).to_le_bytes());
    payload.extend_from_slice(&rec.value);

    let mut frame = Vec::with_capacity(8 + payload.len());
    let crc = crc32fast::hash(&payload);
    frame.extend_from_slice(&crc.to_le_bytes());
    frame.extend_from_slice(&(payload.len() as u32).to_le_bytes());
    frame.extend_from_slice(&payload);
    frame
}

fn decode(payload: &[u8]) -> Result<SpoolRecord> {
    if payload.len() < 9 {
        bail!("payload too short");
    }
    let has_key = payload[0] == 1;
    let key_len = u32::from_le_bytes(payload[1..5].try_into()?) as usize;
    let mut pos = 5;
    if payload.len() < pos + key_len + 4 {
        bail!("payload truncated at key");
    }
    let key = if has_key {
        Some(payload[pos..pos + key_len].to_vec())
    } else {
        None
    };
    pos += key_len;
    let value_len = u32::from_le_bytes(payload[pos..pos + 4].try_into()?) as usize;
    pos += 4;
    if payload.len() != pos + value_len {
        bail!("payload length mismatch");
    }
    Ok(SpoolRecord { key, value: payload[pos..].to_vec() })
}

impl Spool {
    /// Open (creating if needed) the spool directory and replay unacked frames.
    pub fn open(dir: impl AsRef<Path>) -> Result<Self> {
        let dir = dir.as_ref().to_path_buf();
        fs::create_dir_all(&dir).with_context(|| format!("create spool dir {}", dir.display()))?;
        let path = dir.join(SPOOL_FILE);
        let mut queue = VecDeque::new();
        let mut file_bytes = 0u64;
        if path.exists() {
            let len = fs::metadata(&path)?.len();
            let f = File::open(&path)?;
            let mut reader = BufReader::new(f);
            let mut valid_up_to = 0u64;
            loop {
                let mut hdr = [0u8; 8];
                match reader.read_exact(&mut hdr) {
                    Ok(()) => {}
                    Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => break,
                    Err(e) => return Err(e).with_context(|| format!("read spool {}", path.display())),
                }
                let crc = u32::from_le_bytes(hdr[0..4].try_into().unwrap());
                let len = u32::from_le_bytes(hdr[4..8].try_into().unwrap()) as usize;
                if len > 16 * 1024 * 1024 {
                    // Absurd frame length: treat as corruption and truncate.
                    tracing::warn!(offset = valid_up_to, "spool frame length corrupt; truncating");
                    break;
                }
                let mut payload = vec![0u8; len];
                match reader.read_exact(&mut payload) {
                    Ok(()) => {}
                    Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => {
                        tracing::warn!(offset = valid_up_to, "spool tail truncated by crash; dropping partial frame");
                        break;
                    }
                    Err(e) => return Err(e).context("read spool frame"),
                }
                if crc32fast::hash(&payload) != crc {
                    tracing::warn!(offset = valid_up_to, "spool frame CRC mismatch; truncating");
                    break;
                }
                match decode(&payload) {
                    Ok(rec) => {
                        valid_up_to += (8 + len) as u64;
                        queue.push_back(rec);
                    }
                    Err(e) => {
                        tracing::warn!(offset = valid_up_to, error = %e, "spool frame undecodable; truncating");
                        break;
                    }
                }
            }
            // Truncate any corrupt tail so future appends stay aligned.
            if valid_up_to < len {
                let f = OpenOptions::new().write(true).open(&path)?;
                f.set_len(valid_up_to)?;
                f.sync_all()?;
            }
            file_bytes = valid_up_to;
        }
        let writer = OpenOptions::new().create(true).append(true).open(&path)?;
        Ok(Spool { dir, queue, acked_pending_compact: 0, file_bytes, writer })
    }

    pub fn len(&self) -> usize {
        self.queue.len()
    }

    pub fn is_empty(&self) -> bool {
        self.queue.is_empty()
    }

    pub fn file_bytes(&self) -> u64 {
        self.file_bytes
    }

    /// Append records durably (write + fsync) and enqueue them.
    pub fn append(&mut self, records: &[SpoolRecord]) -> Result<()> {
        for rec in records {
            let frame = encode(rec);
            self.writer.write_all(&frame)?;
            self.file_bytes += frame.len() as u64;
        }
        self.writer.flush()?;
        self.writer.sync_data()?; // survive power loss, not just process crash
        self.queue.extend(records.iter().cloned());
        Ok(())
    }

    /// Peek at up to `max` records from the front for a drain attempt.
    pub fn peek(&self, max: usize) -> Vec<SpoolRecord> {
        self.queue.iter().take(max).cloned().collect()
    }

    /// Mark the first `n` records as delivered to Kafka. Compacts the file
    /// once the acked prefix is large (or the queue is fully drained), so
    /// steady-state write amplification stays bounded.
    pub fn ack(&mut self, n: usize) -> Result<()> {
        for _ in 0..n.min(self.queue.len()) {
            self.queue.pop_front();
            self.acked_pending_compact += 1;
        }
        if self.acked_pending_compact > 0
            && (self.queue.is_empty() || self.acked_pending_compact >= self.queue.len().max(512))
        {
            self.compact()?;
        }
        Ok(())
    }

    /// Rewrite the file with only the unacked queue (rename is atomic).
    fn compact(&mut self) -> Result<()> {
        let tmp = self.dir.join(SPOOL_TMP);
        {
            let mut w = io::BufWriter::new(File::create(&tmp)?);
            for rec in &self.queue {
                w.write_all(&encode(rec))?;
            }
            w.flush()?;
            w.get_ref().sync_all()?;
        }
        fs::rename(&tmp, self.dir.join(SPOOL_FILE))?;
        self.writer = OpenOptions::new().append(true).open(self.dir.join(SPOOL_FILE))?;
        self.file_bytes = fs::metadata(self.dir.join(SPOOL_FILE))?.len();
        self.acked_pending_compact = 0;
        Ok(())
    }
}

/// Fluvio offset bookkeeping: a tiny two-slot state file (`offset.state`)
/// written with fsync so the consumer resumes after restart without
/// re-forwarding already-queued records.
pub fn load_offset(dir: &Path) -> Option<i64> {
    let raw = fs::read_to_string(dir.join("offset.state")).ok()?;
    raw.trim().parse().ok()
}

pub fn store_offset(dir: &Path, next_offset: i64) -> Result<()> {
    let tmp = dir.join("offset.tmp");
    fs::write(&tmp, format!("{next_offset}\n"))?;
    File::open(&tmp)?.sync_all()?;
    fs::rename(&tmp, dir.join("offset.state"))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip() {
        let dir = std::env::temp_dir().join(format!("spool-test-{}", uuid::Uuid::new_v4()));
        {
            let mut spool = Spool::open(&dir).unwrap();
            spool
                .append(&[
                    SpoolRecord { key: Some(b"bus-01".to_vec()), value: b"{}".to_vec() },
                    SpoolRecord { key: None, value: b"{\"a\":1}".to_vec() },
                ])
                .unwrap();
            assert_eq!(spool.len(), 2);
        }
        // Reopen: records must survive the "restart".
        let mut spool = Spool::open(&dir).unwrap();
        assert_eq!(spool.len(), 2);
        let peeked = spool.peek(10);
        assert_eq!(peeked[0].key.as_deref(), Some(b"bus-01".as_ref()));
        assert_eq!(peeked[1].value, b"{\"a\":1}");
        spool.ack(1).unwrap();
        assert_eq!(spool.len(), 1);
        spool.ack(1).unwrap();
        assert!(spool.is_empty());
        // After full drain + compaction nothing replays.
        let spool = Spool::open(&dir).unwrap();
        assert!(spool.is_empty());
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn offset_state_roundtrip() {
        let dir = std::env::temp_dir().join(format!("offset-test-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        assert!(load_offset(&dir).is_none());
        store_offset(&dir, 42).unwrap();
        assert_eq!(load_offset(&dir), Some(42));
        std::fs::remove_dir_all(&dir).ok();
    }
}
