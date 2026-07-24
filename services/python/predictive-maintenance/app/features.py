"""Feature engineering: aggregate recent fleet.telemetry into model features.

Shared verbatim between the runtime service and train.py so that training and
inference always see identical feature semantics.
"""

from __future__ import annotations

# Ordered feature list — MUST match the artifact's "features" array.
FEATURES: list[str] = [
    "n_samples",
    "avg_speed_kph",
    "max_speed_kph",
    "avg_fuel_cell_kw",
    "max_fuel_cell_kw",
    "std_fuel_cell_kw",
    "avg_h2_level_pct",
    "min_h2_level_pct",
    "refuel_events",
    "avg_battery_soc_pct",
    "min_battery_soc_pct",
    "std_battery_soc_pct",
    "km_driven",
]

_AGGREGATE_SQL = """
WITH t AS (
    SELECT
        speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km,
        h2_level_pct - LAG(h2_level_pct) OVER (ORDER BY ts) AS h2_delta
    FROM fleet.telemetry
    WHERE bus_id = $1::uuid AND ts > now() - make_interval(hours => $2)
)
SELECT
    count(*)::float8                                   AS n_samples,
    coalesce(avg(speed_kph), 0)::float8                AS avg_speed_kph,
    coalesce(max(speed_kph), 0)::float8                AS max_speed_kph,
    coalesce(avg(fuel_cell_kw), 0)::float8             AS avg_fuel_cell_kw,
    coalesce(max(fuel_cell_kw), 0)::float8             AS max_fuel_cell_kw,
    coalesce(stddev_samp(fuel_cell_kw), 0)::float8     AS std_fuel_cell_kw,
    coalesce(avg(h2_level_pct), 0)::float8             AS avg_h2_level_pct,
    coalesce(min(h2_level_pct), 0)::float8             AS min_h2_level_pct,
    count(*) FILTER (WHERE h2_delta > 5)::float8       AS refuel_events,
    coalesce(avg(battery_soc_pct), 0)::float8          AS avg_battery_soc_pct,
    coalesce(min(battery_soc_pct), 0)::float8          AS min_battery_soc_pct,
    coalesce(stddev_samp(battery_soc_pct), 0)::float8  AS std_battery_soc_pct,
    coalesce(max(odometer_km) - min(odometer_km), 0)::float8 AS km_driven
FROM t
"""


async def fetch_features(pool, bus_id: str, window_hours: int) -> dict[str, float] | None:
    """Aggregate the last `window_hours` of telemetry for one bus.

    Returns None when the bus has no telemetry in the window.
    """
    row = await pool.fetchrow(_AGGREGATE_SQL, bus_id, float(window_hours))
    if row is None or row["n_samples"] == 0:
        return None
    return {name: float(row[name]) for name in FEATURES}


def feature_vector(features: dict[str, float]) -> list[float]:
    """Ordered numeric vector matching FEATURES."""
    return [float(features.get(name, 0.0)) for name in FEATURES]
