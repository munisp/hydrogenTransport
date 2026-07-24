//! Environment configuration (SPEC.md §3.5).

use std::env;
use std::time::Duration;

#[derive(Debug, Clone)]
pub struct Config {
    pub port: u16,
    pub kafka_brokers: String,
    pub kafka_group_id: String,
    pub input_topic: String,
    pub output_topic: String,
    pub database_url: String,
    pub redis_addr: String,
    pub toggle_url: String,
    pub toggle_module: String,
    pub toggle_poll_interval: Duration,
    /// How often hot twins are snapshotted into fleet.twin_snapshots.
    pub snapshot_interval: Duration,
    /// TTL for Redis hot state; refreshed on every update.
    pub twin_ttl: Duration,
}

fn env_or(key: &str, default: &str) -> String {
    env::var(key).unwrap_or_else(|_| default.to_string())
}

impl Config {
    pub fn from_env() -> Self {
        let poll_s = env_or("TOGGLE_POLL_INTERVAL_S", "10").parse().unwrap_or(10);
        let snap_s = env_or("SNAPSHOT_INTERVAL_S", "60").parse().unwrap_or(60);
        let ttl_s = env_or("TWIN_TTL_S", "900").parse().unwrap_or(900);
        Self {
            port: env_or("PORT", "8092").parse().unwrap_or(8092),
            kafka_brokers: env_or("KAFKA_BROKERS", "localhost:9092"),
            kafka_group_id: env_or("KAFKA_GROUP_ID", "digital-twin"),
            input_topic: env_or("INPUT_TOPIC", "telemetry.enriched"),
            output_topic: env_or("OUTPUT_TOPIC", "twin.updated"),
            database_url: env_or(
                "DATABASE_URL",
                "postgres://postgres:postgres@localhost:5432/h2fleet",
            ),
            redis_addr: env_or("REDIS_ADDR", "localhost:6379"),
            toggle_url: env_or("TOGGLE_URL", "http://localhost:8080"),
            toggle_module: env_or("TOGGLE_MODULE", "digital-twin"),
            toggle_poll_interval: Duration::from_secs(poll_s),
            snapshot_interval: Duration::from_secs(snap_s),
            twin_ttl: Duration::from_secs(ttl_s),
        }
    }

    pub fn redis_url(&self) -> String {
        if self.redis_addr.contains("://") {
            self.redis_addr.clone()
        } else {
            format!("redis://{}", self.redis_addr)
        }
    }
}
