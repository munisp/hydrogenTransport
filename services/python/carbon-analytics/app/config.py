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
    # This is the REFERENCE every energy_type is credited against.
    diesel_baseline_kg_co2_per_km: float = 1.2
    # Battery buses: grid electricity CO2 intensity (kg CO2 per kWh, ~EU grid
    # average) and fleet electricity consumption (kWh per km) until per-bus
    # learned kWh/km exists. Avoided = km * (diesel_baseline - kwh_km*grid).
    grid_co2_kg_per_kwh: float = 0.35
    ev_kwh_per_km: float = 1.1
    # CNG buses: tailpipe factor vs the diesel reference (kg CO2 per km).
    cng_kg_co2_per_km: float = 1.0
    # kg of CO2 avoided per issued credit (1 credit = 1 tonne by default).
    credit_kg_co2: float = 1000.0


settings = Settings()
