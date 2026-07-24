//! Environment configuration (SPEC.md §3.5 env-var conventions).

use std::env;
use std::time::Duration;

#[derive(Debug, Clone)]
pub struct Config {
    /// /healthz listen port (default 8093).
    pub port: u16,
    /// Comma-separated Kafka brokers.
    pub kafka_brokers: String,
    /// Consumer group id.
    pub kafka_group_id: String,
    /// Input topic (telemetry.raw).
    pub input_topic: String,
    /// Output topic (telemetry.enriched).
    pub output_topic: String,
    /// Postgres/TimescaleDB DSN.
    pub database_url: String,
    /// Redis address (host:port or redis://host:port).
    pub redis_addr: String,
    /// toggle-service base URL.
    pub toggle_url: String,
    /// Module id this service is gated on (SPEC §3.1).
    pub toggle_module: String,
    /// Max rows per TimescaleDB batch insert.
    pub batch_size: usize,
    /// Max time to hold a non-full batch before flushing.
    pub batch_max_wait: Duration,
    /// Toggle poll interval (SPEC/mission: 10s).
    pub toggle_poll_interval: Duration,
}

fn env_or(key: &str, default: &str) -> String {
    env::var(key).unwrap_or_else(|_| default.to_string())
}

impl Config {
    pub fn from_env() -> Self {
        let batch_size = env_or("BATCH_SIZE", "500").parse().unwrap_or(500);
        let batch_max_wait_ms = env_or("BATCH_MAX_WAIT_MS", "500").parse().unwrap_or(500);
        let toggle_poll_s = env_or("TOGGLE_POLL_INTERVAL_S", "10").parse().unwrap_or(10);
        Self {
            port: env_or("PORT", "8093").parse().unwrap_or(8093),
            kafka_brokers: env_or("KAFKA_BROKERS", "localhost:9092"),
            kafka_group_id: env_or("KAFKA_GROUP_ID", "telemetry-ingest"),
            input_topic: env_or("INPUT_TOPIC", "telemetry.raw"),
            output_topic: env_or("OUTPUT_TOPIC", "telemetry.enriched"),
            database_url: env_or(
                "DATABASE_URL",
                "postgres://postgres:postgres@localhost:5432/h2fleet",
            ),
            redis_addr: env_or("REDIS_ADDR", "localhost:6379"),
            toggle_url: env_or("TOGGLE_URL", "http://localhost:8080"),
            toggle_module: env_or("TOGGLE_MODULE", "telematics"),
            batch_size,
            batch_max_wait: Duration::from_millis(batch_max_wait_ms),
            toggle_poll_interval: Duration::from_secs(toggle_poll_s),
        }
    }

    /// Normalize REDIS_ADDR to a redis:// URL (accepts bare host:port).
    pub fn redis_url(&self) -> String {
        if self.redis_addr.contains("://") {
            self.redis_addr.clone()
        } else {
            format!("redis://{}", self.redis_addr)
        }
    }
}
