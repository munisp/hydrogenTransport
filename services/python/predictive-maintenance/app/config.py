"""Runtime configuration (env-driven, SPEC §3.5)."""

from __future__ import annotations

import os
from dataclasses import dataclass


def _split_brokers(raw: str) -> list[str]:
    return [b.strip() for b in raw.split(",") if b.strip()]


@dataclass(frozen=True)
class Settings:
    database_url: str = os.environ.get("DATABASE_URL", "")
    kafka_brokers: list[str] = None  # type: ignore[assignment]
    toggle_url: str = os.environ.get("TOGGLE_URL", "http://toggle-service:8080")
    toggle_module: str = "predictive-maintenance"
    input_topic: str = os.environ.get("INPUT_TOPIC", "telemetry.enriched")
    output_topic: str = os.environ.get("OUTPUT_TOPIC", "maintenance.predicted")
    # SPEC §3.3 fuel-monitoring topic; derived from enriched telemetry and
    # consumed back to learn per-bus consumption into fleet.fuel_consumption.
    fuel_topic: str = os.environ.get("FUEL_TOPIC", "fuel.reading")
    kafka_group_id: str = os.environ.get("KAFKA_GROUP_ID", "predictive-maintenance")
    scoring_interval_s: int = int(os.environ.get("SCORING_INTERVAL_S", "300"))
    feature_window_hours: int = int(os.environ.get("FEATURE_WINDOW_HOURS", "24"))
    high_risk_threshold: float = float(os.environ.get("HIGH_RISK_THRESHOLD", "0.7"))
    model_dir: str = os.environ.get("MODEL_DIR", "/app/models")

    def __post_init__(self):
        object.__setattr__(
            self,
            "kafka_brokers",
            _split_brokers(os.environ.get("KAFKA_BROKERS", "kafka:9092")),
        )


settings = Settings()
