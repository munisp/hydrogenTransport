"""ml-platform inference server configuration via environment."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8095
    model_artifacts_dir: str = "artifacts"
    # Champion/challenger traffic split: fraction of scoring requests routed
    # to the challenger artifact (deterministic per subject key).
    ab_split: float = 0.1
    # Drift monitor: seconds between PSI/KS recomputation; ring-buffer size.
    drift_interval_s: int = 60
    drift_window: int = 512
    drift_psi_warn: float = 0.2

    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"


settings = Settings()
