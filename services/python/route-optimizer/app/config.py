"""Configuration (SPEC.md §3.5 env conventions)."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8091
    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"
    toggle_url: str = "http://localhost:8080"
    toggle_module: str = "route-energy-optimizer"

    # Energy model per vehicle energy_type (fleet defaults; per-bus capacity
    # from fleet.vehicles and the learned per-bus rate from
    # fleet.fuel_consumption — exposed by fleet-api — win when present).
    h2_consumption_kg_per_km: float = 0.08      # ~8 kg / 100 km
    battery_consumption_kwh_per_km: float = 1.1  # ~110 kWh / 100 km city bus
    diesel_consumption_l_per_km: float = 0.40    # ~40 L / 100 km
    cng_consumption_kg_per_km: float = 0.30      # ~30 kg / 100 km
    # 'mixed' stations serve every energy_type but are weighted by this detour
    # factor vs an exact-type station at the same distance (prefer dedicated).
    mixed_station_detour_factor: float = 1.25
    range_safety_km: float = 20.0           # never plan below this remaining range
    solver_time_limit_s: int = 10
    stop_drop_penalty: int = 50_000         # synthetic km penalty for unserved stop


settings = Settings()
