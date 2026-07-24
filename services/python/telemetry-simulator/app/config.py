"""Environment configuration for the telemetry simulator (SPEC §3.5)."""
from __future__ import annotations

import os


class Config:
    # Core middleware addresses — same env contract as every other service.
    # Required: no default (must come from the environment / .env — see root .env.example)
    DATABASE_URL: str = os.environ["DATABASE_URL"]
    KAFKA_BROKERS: str = os.getenv("KAFKA_BROKERS", "localhost:9094")
    REDIS_ADDR: str = os.getenv("REDIS_ADDR", "localhost:6379")

    # Simulator behaviour.
    KAFKA_TOPIC: str = os.getenv("KAFKA_TOPIC", "telemetry.raw")
    SIM_INTERVAL_SECONDS: float = float(os.getenv("SIM_INTERVAL_SECONDS", "5"))
    SIM_SOURCE: str = os.getenv("SIM_SOURCE", "telemetry-simulator")
    # H2 tank drain in percent-per-km (37.5 kg tank, ~285 km effective range).
    SIM_H2_DRAIN_PCT_PER_KM: float = float(os.getenv("SIM_H2_DRAIN_PCT_PER_KM", "0.35"))
    # Refuel threshold: tank resets to ~full at/under this level.
    SIM_REFUEL_THRESHOLD_PCT: float = float(os.getenv("SIM_REFUEL_THRESHOLD_PCT", "15"))
    # Startup retry budget while middleware is still coming up.
    CONNECT_RETRY_SECONDS: float = float(os.getenv("CONNECT_RETRY_SECONDS", "3"))
    CONNECT_RETRIES: int = int(os.getenv("CONNECT_RETRIES", "40"))

    LOG_LEVEL: str = os.getenv("LOG_LEVEL", "INFO")

    @classmethod
    def redis_host_port(cls) -> tuple[str, int]:
        host, _, port = cls.REDIS_ADDR.rpartition(":")
        if not host:
            return cls.REDIS_ADDR, 6379
        return host, int(port or 6379)


config = Config()
