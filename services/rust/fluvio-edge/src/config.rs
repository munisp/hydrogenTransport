//! Environment-driven configuration (SPEC §3.5 style: every knob is an env var
//! with a sane edge default).

use std::time::Duration;

#[derive(Debug, Clone)]
pub struct Config {
    /// Fluvio SC endpoint on the bus gateway, e.g. "fluvio-sc:9003" or
    /// "localhost:9003" when the agent runs as a sidecar on the gateway.
    pub fluvio_endpoint: String,
    /// Fluvio topic produced by the on-bus telemetry publisher.
    pub fluvio_topic: String,
    /// Fluvio partition (edge topics are single-partition by default).
    pub fluvio_partition: u32,
    /// Platform Kafka bootstrap servers (comma-separated).
    pub kafka_brokers: String,
    /// Platform topic that telemetry-ingest consumes (SPEC §3.3).
    pub kafka_topic: String,
    /// Directory for the store-and-forward spool + offset state. Must be on
    /// persistent storage on the gateway to survive restarts.
    pub spool_dir: String,
    /// Max records per Kafka batch.
    pub batch_max: usize,
    /// Max time to wait before flushing a partial batch.
    pub batch_linger: Duration,
    /// Soft cap on spool size; when exceeded the Fluvio consumer pauses
    /// (backpressure) instead of unbounded disk growth.
    pub spool_max_bytes: u64,
    /// Health endpoint port.
    pub port: u16,
}

fn env(key: &str, default: &str) -> String {
    std::env::var(key).unwrap_or_else(|_| default.to_string())
}

impl Config {
    pub fn from_env() -> Self {
        let linger_ms: u64 = env("BATCH_LINGER_MS", "500").parse().unwrap_or(500);
        Config {
            fluvio_endpoint: env("FLUVIO_ENDPOINT", "localhost:9003"),
            fluvio_topic: env("FLUVIO_TOPIC", "bus-telemetry"),
            fluvio_partition: env("FLUVIO_PARTITION", "0").parse().unwrap_or(0),
            kafka_brokers: env("KAFKA_BROKERS", "kafka:9092"),
            kafka_topic: env("KAFKA_TOPIC", "telemetry.raw"),
            spool_dir: env("SPOOL_DIR", "/var/lib/fluvio-edge"),
            batch_max: env("BATCH_MAX", "500").parse().unwrap_or(500),
            batch_linger: Duration::from_millis(linger_ms),
            spool_max_bytes: env("SPOOL_MAX_BYTES", "67108864").parse().unwrap_or(67_108_864),
            port: env("PORT", "8093").parse().unwrap_or(8093),
        }
    }
}
