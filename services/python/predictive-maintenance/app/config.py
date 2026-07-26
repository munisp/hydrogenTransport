"""Service configuration via environment (SPEC.md §3.5 conventions)."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8090
    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"
    kafka_brokers: str = "localhost:9092"
    toggle_url: str = "http://localhost:8080"
    toggle_module: str = "predictive-maintenance"

    input_topic: str = "telemetry.enriched"
    output_topic: str = "maintenance.predicted"
    fuel_topic: str = "fuel.reading"
    kafka_group_id: str = "predictive-maintenance"

    model_path: str = "models/model.joblib"
    # ml-platform artifact root (shared volume); champion maintenance_lstm is
    # preferred over the legacy sklearn joblib when present.
    model_artifacts_dir: str = "artifacts"
    feature_window_hours: int = 24
    scoring_interval_s: int = 300
    high_risk_threshold: float = 0.7


settings = Settings()
