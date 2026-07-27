"""Configuration (SPEC.md §3.5 env conventions).

OCPP-specific knobs:

* ``OCPP_OPEN_ID_TAGS`` — comma-separated id_tag whitelist for the Authorize
  policy. The default ``*`` accepts every id_tag (DEV ONLY — a loud warning
  is logged at startup and on every accepted-unknown tag). Any other value is
  an exact-match whitelist; id_tags matching a known bus (fleet.vehicles id
  or fleet_no) are always accepted.
* ``OCPP_METER_UNIT`` — unit of the charger meter register (``wh`` default,
  per the OCPP 1.6 default measurand Energy.Active.Import.Register in Wh, or
  ``kwh``). ``infra.charging_sessions.kwh`` is always stored in kWh:
  ``kwh = (meter_stop - meter_start) * kwh_factor``.
"""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="", case_sensitive=False)

    port: int = 8100
    database_url: str = "postgresql://postgres:postgres@localhost:5432/h2fleet"
    kafka_brokers: str = "localhost:9092"

    # Kafka topic for charge-point status changes (packages/events catalog).
    status_topic: str = "station.status.changed"

    # Comma-separated id_tag whitelist; "*" accepts all (dev default).
    ocpp_open_id_tags: str = "*"
    # Heartbeat interval (seconds) returned in BootNotification responses.
    ocpp_boot_interval: int = 300
    # Meter register unit reported by chargers: "wh" (OCPP default) or "kwh".
    ocpp_meter_unit: str = "wh"

    @property
    def open_charging(self) -> bool:
        """True when every id_tag is accepted (dev-mode open charging)."""
        return "*" in self.open_id_tags

    @property
    def open_id_tags(self) -> set[str]:
        return {t.strip() for t in self.ocpp_open_id_tags.split(",") if t.strip()}

    @property
    def kwh_factor(self) -> float:
        """Multiplier converting meter-register deltas to kWh."""
        return 0.001 if self.ocpp_meter_unit.lower() == "wh" else 1.0


settings = Settings()
