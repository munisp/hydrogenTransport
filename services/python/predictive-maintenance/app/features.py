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


# ------------------------------------------------------- LSTM sequence input --
#: Raw per-timestep features consumed by the trained LSTM artifact
#: (ml-platform models/maintenance_lstm.py — same order).
SEQ_FEATURES: list[str] = [
    "h2_level_pct",
    "fuel_cell_kw",
    "battery_soc_pct",
    "speed_kph",
    "ambient_temp_c",
]

_SEQUENCE_SQL = """
SELECT ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct
FROM fleet.telemetry
WHERE bus_id = $1::uuid AND ts > now() - make_interval(hours => $2)
ORDER BY ts
"""

SEQUENCE_STEPS = 48  # resampled window length expected by the LSTM artifact


def _seasonal_temp(ts) -> float:
    """Ambient temperature is not stored in fleet.telemetry (SPEC §3.4);
    use the same deterministic seasonal estimate as ml-platform training."""
    import math

    doy = ts.timetuple().tm_yday
    hour = ts.hour + ts.minute / 60.0
    return 10.0 + 9.0 * math.sin(2 * math.pi * (doy - 100) / 365.0) \
        + 4.0 * math.sin(2 * math.pi * (hour - 14) / 24.0)


async def fetch_sequence(pool, bus_id: str, window_hours: int,
                         steps: int = SEQUENCE_STEPS):
    """Resample the raw telemetry window onto `steps` evenly spaced timesteps
    (linear interpolation) -> numpy array (steps, 5) in SEQ_FEATURES order,
    or None when the bus has too little telemetry (< 4 rows)."""
    import numpy as np

    rows = await pool.fetch(_SEQUENCE_SQL, bus_id, float(window_hours))
    if len(rows) < 4:
        return None
    t0, t1 = rows[0]["ts"], rows[-1]["ts"]
    span = max((t1 - t0).total_seconds(), 1.0)
    x = np.array([(r["ts"] - t0).total_seconds() / span for r in rows])
    grid = np.linspace(0.0, 1.0, steps)
    cols = []
    for name in ("h2_level_pct", "fuel_cell_kw", "battery_soc_pct", "speed_kph"):
        y = np.array([float(r[name]) for r in rows])
        cols.append(np.interp(grid, x, y))
    temp = np.array([_seasonal_temp(t0 + (t1 - t0) * float(g)) for g in grid])
    cols.append(temp)
    return np.stack(cols, axis=1).astype("float32")
