"""Configuration (SPEC.md §3.5 env conventions)."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8094
    enable_api: bool = True
    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"
    kafka_brokers: str = "localhost:9092"
    toggle_url: str = "http://localhost:8080"
    toggle_module: str = "carbon-credits"
    output_topic: str = "carbon.credit.issued"

    # Diesel baseline: kg CO2 emitted per km by a comparable diesel bus.
    diesel_baseline_kg_co2_per_km: float = 1.2
    # kg of CO2 avoided per issued credit (1 credit = 1 tonne by default).
    credit_kg_co2: float = 1000.0


settings = Settings()
