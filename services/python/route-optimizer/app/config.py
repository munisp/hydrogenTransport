"""Configuration (SPEC.md §3.5 env conventions)."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8091
    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"
    toggle_url: str = "http://localhost:8080"
    toggle_module: str = "route-energy-optimizer"

    # H2 bus energy model (fleet defaults; per-bus capacity from fleet.vehicles).
    h2_consumption_kg_per_km: float = 0.08  # ~8 kg / 100 km
    range_safety_km: float = 20.0           # never plan below this remaining range
    solver_time_limit_s: int = 10
    stop_drop_penalty: int = 50_000         # synthetic km penalty for unserved stop


settings = Settings()
