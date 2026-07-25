"""Synthetic fleet data generator — BOOTSTRAP ONLY.

=============================================================================
WHY THIS EXISTS / HONEST STATUS
=============================================================================
The platform has no real failure labels and almost no real telemetry yet
(infra/sql/002_seed.sql inserts only a handful of sample rows). This generator
produces a *plausible* synthetic fleet so that models, training loops, the
inference server, A/B plumbing and the drift monitor can be developed and
tested end-to-end. All reported metrics are measured ON THIS SYNTHETIC DATA
and say nothing about real-world accuracy. Once `fleet.telemetry`,
`fleet.maintenance_predictions`, leak incidents and ridership accumulate,
train with `--source postgres` / `--source iceberg` and retire this path.

Distributions are seeded from the actual platform schema/seed:
  * 50 buses  H2-001..H2-050            (infra/sql/002_seed.sql)
  * 3 refueling stations with the seeded names
  * fleet.telemetry columns             (SPEC.md §3.4)
  * components: fuel_cell | compressor | tank_valve | battery
  * leak incidents: rare-but-clustered  (Poisson clusters, not i.i.d.)
  * diurnal + weekday/weekend ridership, seasonal weather
  * per-component degradation curves with injectable failure modes

Outputs (parquet, one directory):
  telemetry.parquet   5-min rows per bus with degradation-affected signals and
                      per-component failure schedule labels (failure_day,
                      days_to_failure_<component>, risk_label_<component>)
  ridership.parquet   hourly boardings per route with weather covariates
  leak_sensors.parquet hourly sensor vectors, normal + injected leak episodes
  carbon_periods.parquet weekly period features + kg_co2_avoided target
  graph.npz           route/station/depot adjacency + node names + static
                      node features + synthetic propagation targets
  manifest.json       seed/config fingerprint for determinism checks

Everything is deterministic for a given (seed, days) pair.
"""

from __future__ import annotations

import argparse
import json
import os
from dataclasses import dataclass, field

import numpy as np
import pandas as pd

FLEET_SIZE = 50
FLEET_NOS = [f"H2-{i:03d}" for i in range(1, FLEET_SIZE + 1)]
STATION_NAMES = ["Depot Central HRS", "Riverside HRS", "Northgate HRS"]
N_ROUTES = 12
ROUTE_IDS = [f"R-{i:02d}" for i in range(1, N_ROUTES + 1)]
COMPONENTS = ["fuel_cell", "compressor", "tank_valve", "battery"]

STEP_MIN = 5            # telemetry cadence
STEPS_PER_DAY = 24 * 60 // STEP_MIN
START_TS = pd.Timestamp("2024-03-01T00:00:00Z")

#: Injectable failure modes: name -> (component, signal distortion applied as
#: wear -> 1 in the days leading to the scheduled failure).
FAILURE_MODES: dict[str, str] = {
    "fuel_cell_degradation": "fuel_cell",     # falling output, rising volatility
    "compressor_surge": "compressor",         # fuel-cell demand spikes
    "tank_valve_stiction": "tank_valve",      # h2 drain irregularities, run-dry
    "battery_capacity_fade": "battery",       # deep discharges, wide SoC swing
}

RISK_HORIZON_DAYS = 14   # window labels risk=1 when failure within this horizon
CENSOR_DAYS = 60.0       # capped days_to_failure for non-failing components


@dataclass
class SynthConfig:
    days: int = 42
    seed: int = 42
    fleet_size: int = FLEET_SIZE
    n_routes: int = N_ROUTES
    failure_modes: dict[str, str] = field(default_factory=lambda: dict(FAILURE_MODES))


# ----------------------------------------------------------------- weather --
def _weather(n_steps: int, rng: np.random.Generator) -> tuple[np.ndarray, np.ndarray]:
    """Ambient temp (seasonal + diurnal + noise) and precip per 5-min step."""
    t = np.arange(n_steps)
    day_of_year = (START_TS.dayofyear + t / STEPS_PER_DAY) % 365
    hour = (t % STEPS_PER_DAY) * (24.0 / STEPS_PER_DAY)
    temp = (
        10.0 + 9.0 * np.sin(2 * np.pi * (day_of_year - 100) / 365.0)   # season
        + 4.0 * np.sin(2 * np.pi * (hour - 14) / 24.0)                  # diurnal
        + rng.normal(0, 1.2, n_steps)
    )
    rain_days = rng.random(int(np.ceil(n_steps / STEPS_PER_DAY)) + 1) < 0.30
    precip = np.where(
        rain_days[(t // STEPS_PER_DAY).astype(int)],
        np.maximum(rng.gamma(1.5, 0.8, n_steps), 0.0), 0.0,
    )
    return temp.astype(np.float32), precip.astype(np.float32)


# ------------------------------------------------------------- maintenance --
def _failure_schedule(rng: np.random.Generator) -> dict[str, float]:
    """Per component: failure day within the horizon or NaN (never fails).

    ~45% of buses experience a fuel-cell event inside `days`, fewer for the
    other components — mirrors 'rare but present' maintenance reality.
    """
    probs = {"fuel_cell": 0.45, "compressor": 0.35, "tank_valve": 0.30, "battery": 0.35}
    return {
        c: (float(rng.uniform(8, 60)) if rng.random() < probs[c] else float("nan"))
        for c in COMPONENTS
    }


def _bus_telemetry(bus_idx: int, cfg: SynthConfig, rng: np.random.Generator,
                   temp: np.ndarray) -> pd.DataFrame:
    n = cfg.days * STEPS_PER_DAY
    hour = (np.arange(n) % STEPS_PER_DAY) * (24.0 / STEPS_PER_DAY)
    dow = (START_TS.dayofweek + np.arange(n) // STEPS_PER_DAY) % 7
    weekend = dow >= 5

    # Duty cycle: active ~05:00-23:00, shorter on weekends.
    start_h = 5.0 + (2.0 * weekend) + rng.normal(0, 0.3, n)
    active = ((hour >= start_h) & (hour <= 22.5)).astype(np.float32)
    speed = active * np.clip(24 + 14 * np.sin(hour * 0.9) + rng.normal(0, 6, n), 0, 70)
    km = np.cumsum(speed * (STEP_MIN / 60.0))

    failures = _failure_schedule(rng)
    wear = {c: np.zeros(n, dtype=np.float32) for c in COMPONENTS}
    for c, fday in failures.items():
        if not np.isnan(fday):
            onset = max(fday - 21.0, 0.0)          # degradation starts ~3 weeks out
            days = np.arange(n) / STEPS_PER_DAY
            ramp = np.clip((days - onset) / max(fday - onset, 1e-6), 0.0, 1.0)
            wear[c] = ramp ** 2                     # accelerating degradation

    # Fuel cell: load follows speed; fuel_cell_degradation -> sag + volatility.
    fc_base = 28 + 0.9 * speed
    fc_wear = wear["fuel_cell"] + 0.4 * wear["compressor"]
    fuel_cell_kw = fc_base * (1 - 0.25 * wear["fuel_cell"]) \
        + rng.normal(0, 3 + 14 * fc_wear, n) \
        + wear["compressor"] * rng.gamma(2.0, 8.0, n)     # surge demand spikes
    fuel_cell_kw = np.clip(fuel_cell_kw, 0, 150).astype(np.float32)

    # H2 level: drains with load, refuel jump when low; tank_valve_stiction ->
    # strongly erratic effective drain (sticking valve), stiction plateaus
    # (level sensor/valve stuck flat despite load) and run-dry excursions.
    drain = fuel_cell_kw * 0.0011 * STEP_MIN * (1 + 0.6 * wear["tank_valve"])
    h2 = np.empty(n, dtype=np.float32)
    level = rng.uniform(70, 95)
    stuck = 0
    for i in range(n):
        if wear["tank_valve"][i] > 0.2 and stuck == 0 and rng.random() < 0.01 * wear["tank_valve"][i]:
            stuck = int(rng.integers(6, 36))       # valve/sensor sticks 30min-3h
        if stuck > 0:
            stuck -= 1                              # level frozen this step
        else:
            jitter = 1.0 + 4.0 * wear["tank_valve"][i] * rng.random()
            level -= drain[i] * jitter
        if level < 12 + 5 * wear["tank_valve"][i]:
            level = rng.uniform(88, 97)              # refuel event
        h2[i] = max(level, 0.5)

    # Battery SoC: mean-reverting; capacity fade -> deeper discharge + swing.
    soc = np.empty(n, dtype=np.float32)
    s = rng.uniform(55, 75)
    for i in range(n):
        centre = 62 - 18 * wear["battery"][i]
        s += 0.05 * (centre - s) - 0.04 * fuel_cell_kw[i] * 0.02 \
            + rng.normal(0, 0.6 + 2.5 * wear["battery"][i])
        soc[i] = float(np.clip(s, 2, 100))

    df = pd.DataFrame({
        "ts": START_TS + pd.to_timedelta(np.arange(n) * STEP_MIN, unit="min"),
        "fleet_no": FLEET_NOS[bus_idx],
        "speed_kph": speed.astype(np.float32),
        "h2_level_pct": h2,
        "fuel_cell_kw": fuel_cell_kw,
        "battery_soc_pct": soc,
        "odometer_km": (18000 + km).astype(np.float32),
        "ambient_temp_c": temp,
    })
    days_axis = np.arange(n) / STEPS_PER_DAY
    for c in COMPONENTS:
        fday = failures[c]
        if np.isnan(fday):
            df[f"days_to_failure_{c}"] = np.float32(CENSOR_DAYS)
            df[f"risk_label_{c}"] = np.int8(0)
        else:
            dtf = np.clip(fday - days_axis, 0.0, CENSOR_DAYS)
            df[f"days_to_failure_{c}"] = dtf.astype(np.float32)
            df[f"risk_label_{c}"] = (dtf <= RISK_HORIZON_DAYS).astype(np.int8)
    return df


# -------------------------------------------------------------- ridership --
def _ridership(cfg: SynthConfig, temp: np.ndarray, precip: np.ndarray) -> pd.DataFrame:
    n = cfg.days * STEPS_PER_DAY
    hour = (np.arange(n) % STEPS_PER_DAY) * (24.0 / STEPS_PER_DAY)
    dow = (START_TS.dayofweek + np.arange(n) // STEPS_PER_DAY) % 7
    rng = np.random.default_rng(cfg.seed + 777)

    hourly_idx = np.arange(0, n, 60 // STEP_MIN)   # aggregate to hourly rows
    rows: list[pd.DataFrame] = []
    route_scale = rng.uniform(0.5, 1.6, cfg.n_routes)
    for r in range(cfg.n_routes):
        h = hour[hourly_idx]
        d = dow[hourly_idx]
        weekend = (d >= 5).astype(np.float32)
        # Diurnal shape: commute peaks 7-9h and 16-19h; weekend single midday hump.
        wd = 120 * np.exp(-((h - 8) ** 2) / 4.5) + 150 * np.exp(-((h - 17.5) ** 2) / 6.0) \
            + 25 * np.exp(-((h - 13) ** 2) / 40.0)
        we = 90 * np.exp(-((h - 13) ** 2) / 18.0)
        base = np.where(weekend > 0, we, wd) * route_scale[r]
        weather_mult = np.clip(1.0 - 0.02 * precip[hourly_idx] - 0.004 * np.abs(temp[hourly_idx] - 12), 0.75, 1.1)
        rides = rng.poisson(np.maximum(base * weather_mult, 0.5)).astype(np.float32)
        rows.append(pd.DataFrame({
            "ts": START_TS + pd.to_timedelta(hourly_idx * STEP_MIN, unit="min"),
            "route_id": ROUTE_IDS[r],
            "ridership": rides,
            "hour": h.astype(np.float32),
            "dow": d.astype(np.float32),
            "is_weekend": weekend,
            "temp_c": temp[hourly_idx],
            "precip_mm": precip[hourly_idx],
        }))
    return pd.concat(rows, ignore_index=True)


# ------------------------------------------------------------ leak sensors --
def _leak_sensors(cfg: SynthConfig, temp: np.ndarray) -> pd.DataFrame:
    """Hourly sensor vectors per bus. Leaks are RARE-BUT-CLUSTERED: a handful
    of cluster episodes (e.g. a faulty valve batch) each affecting several
    consecutive hours on a few buses, instead of i.i.d. sprinkling."""
    rng = np.random.default_rng(cfg.seed + 4242)
    hours = cfg.days * 24
    rows = int(hours) * cfg.fleet_size
    idx = np.arange(rows)
    bus = idx % cfg.fleet_size
    hour_of_idx = idx // cfg.fleet_size

    normal = {
        "h2_ppm_tank_bay": rng.gamma(1.5, 20.0, rows),
        "h2_ppm_fuelcell_bay": rng.gamma(1.5, 15.0, rows),
        "h2_ppm_cabin": rng.gamma(1.2, 5.0, rows),
        "h2_ppm_dispenser": rng.gamma(1.5, 25.0, rows),
        "tank_pressure_bar": rng.normal(350, 15, rows),
        "pressure_drop_bar_per_min": np.abs(rng.normal(0.02, 0.01, rows)),
        "flow_rate_kg_per_min": np.abs(rng.normal(0.05, 0.02, rows)),
        "ambient_temp_c": temp[(hour_of_idx * (60 // STEP_MIN)).clip(max=len(temp) - 1)],
    }
    df = pd.DataFrame(normal)
    df.insert(0, "fleet_no", [FLEET_NOS[b] for b in bus])
    df.insert(0, "ts", START_TS + pd.to_timedelta(hour_of_idx, unit="h"))
    df["is_leak"] = np.int8(0)

    # Rare-but-clustered: ~3 episodes, each 2-8 buses x 3-18 consecutive hours.
    n_clusters = 3 + int(rng.random() < 0.5)
    for _ in range(n_clusters):
        c_hour = int(rng.integers(0, max(hours - 18, 1)))
        c_buses = rng.choice(cfg.fleet_size, size=int(rng.integers(2, 9)), replace=False)
        c_len = int(rng.integers(3, 19))
        severity = rng.uniform(2.0, 6.0)
        mask = df["ts"].between(START_TS + pd.Timedelta(hours=c_hour),
                                START_TS + pd.Timedelta(hours=c_hour + c_len)) \
            & df["fleet_no"].isin([FLEET_NOS[b] for b in c_buses])
        m = mask.to_numpy()
        k = int(m.sum())
        df.loc[m, "h2_ppm_tank_bay"] += rng.gamma(severity, 400.0, k)
        df.loc[m, "h2_ppm_fuelcell_bay"] += rng.gamma(severity, 150.0, k)
        df.loc[m, "pressure_drop_bar_per_min"] += rng.gamma(severity, 0.15, k)
        df.loc[m, "flow_rate_kg_per_min"] += rng.gamma(severity, 0.05, k)
        df.loc[m, "is_leak"] = np.int8(1)
    return df


# ------------------------------------------------------------------ carbon --
DIESEL_KG_CO2_PER_KM = 1.15   # articulated diesel bus baseline
H2_GREY_KG_CO2_PER_KG = 0.0   # green H2 assumption; adjust as needed


def _carbon_periods(cfg: SynthConfig, telemetry: pd.DataFrame) -> pd.DataFrame:
    """Per-bus weekly periods (enough rows to train on; fleet totals are a
    groupby away and match the citizen.carbon_credits grain)."""
    tel = telemetry.copy()
    tel["period"] = tel["ts"].dt.to_period("W").astype(str)
    rng = np.random.default_rng(cfg.seed + 99)
    prev_by_bus: dict[str, float] = {}
    rows = []
    for (period, fleet_no), g in tel.groupby(["period", "fleet_no"]):
        km = float(g["odometer_km"].max() - g["odometer_km"].min())
        h2_kg = float((g["fuel_cell_kw"] * 0.0011 * STEP_MIN).sum())
        period_days = int(g["ts"].dt.date.nunique())
        avg_temp = float(g["ambient_temp_c"].mean())
        weekday_frac = float((g["ts"].dt.dayofweek < 5).mean())
        ridership = km * rng.uniform(2.5, 4.5)  # proxy until fare data exists
        prev = prev_by_bus.get(fleet_no, rng.uniform(150, 260))
        target = km * DIESEL_KG_CO2_PER_KM * rng.normal(1.0, 0.02) - h2_kg * H2_GREY_KG_CO2_PER_KG
        rows.append({
            "period": period, "fleet_no": fleet_no, "total_km": km,
            "h2_consumed_kg": h2_kg, "total_ridership": ridership,
            "active_buses": 1, "avg_temp_c": avg_temp,
            "weekday_frac": weekday_frac, "period_days": period_days,
            "prev_kg_co2_avoided": prev, "kg_co2_avoided": target,
        })
        prev_by_bus[fleet_no] = target
    return pd.DataFrame(rows)


# -------------------------------------------------------------------- graph --
def _graph(cfg: SynthConfig) -> dict[str, np.ndarray]:
    """Route/station/depot graph for the GCN.

    Nodes: 3 stations + 2 depots + 12 route termini = 17 nodes. Edges connect
    each route terminus to its nearest station(s) and depot; stations connect
    to depots. Targets come from a synthetic propagation model: a delay shock
    at random source nodes diffuses over adjacency (2 hops, attenuated).
    """
    rng = np.random.default_rng(cfg.seed + 31337)
    n_st, n_dep, n_rt = len(STATION_NAMES), 2, cfg.n_routes
    n = n_st + n_dep + n_rt
    names = STATION_NAMES + ["Depot Central", "Depot North"] + ROUTE_IDS[:n_rt]
    adj = np.zeros((n, n), dtype=np.float32)
    for i in range(n_st):
        for j in range(n_dep):
            adj[i, n_st + j] = adj[n_st + j, i] = 1.0
    for r in range(n_rt):
        st = int(rng.integers(0, n_st))
        dep = int(rng.integers(0, n_dep))
        node = n_st + n_dep + r
        adj[node, st] = adj[st, node] = 1.0
        adj[node, n_st + dep] = adj[n_st + dep, node] = 1.0

    # Node feature snapshot.
    node_type = np.zeros((n, 3), dtype=np.float32)
    node_type[:n_st, 0] = 1
    node_type[n_st:n_st + n_dep, 1] = 1
    node_type[n_st + n_dep:, 2] = 1
    delay = rng.gamma(1.5, 2.0, n).astype(np.float32)
    queue = rng.poisson(1.5, n).astype(np.float32)
    h2_avail = np.concatenate([
        rng.uniform(0.3, 0.9, n_st), rng.uniform(0.5, 1.0, n_dep),
        np.zeros(n_rt),
    ]).astype(np.float32)
    throughput = np.concatenate([
        rng.uniform(2, 6, n_st), rng.uniform(4, 10, n_dep), rng.uniform(1, 4, n_rt),
    ]).astype(np.float32)
    x = np.column_stack([node_type, delay, queue, h2_avail, throughput]).astype(np.float32)

    # Synthetic propagation ground truth: 2-hop diffusion of the delay shock,
    # energy impact proportional to queue + delay at neighbours.
    a_hat = adj + np.eye(n, dtype=np.float32)
    a_hat = a_hat / a_hat.sum(axis=1, keepdims=True)
    delay_target = a_hat @ (a_hat @ delay)
    energy_target = (a_hat @ (queue * 0.4 + delay * 0.1)).astype(np.float32)
    return {
        "adjacency": adj, "node_names": np.array(names),
        "node_features": x, "delay_target": delay_target.astype(np.float32),
        "energy_target": energy_target,
    }


# -------------------------------------------------------------------- main --
def generate(cfg: SynthConfig, out_dir: str) -> dict[str, str]:
    """Generate all datasets deterministically; returns {name: path}."""
    os.makedirs(out_dir, exist_ok=True)
    rng = np.random.default_rng(cfg.seed)
    temp, precip = _weather(cfg.days * STEPS_PER_DAY, rng)

    buses = [_bus_telemetry(i, cfg, np.random.default_rng(cfg.seed + 1000 + i), temp)
             for i in range(cfg.fleet_size)]
    telemetry = pd.concat(buses, ignore_index=True)

    frames: dict[str, pd.DataFrame] = {
        "telemetry": telemetry,
        "ridership": _ridership(cfg, temp, precip),
        "leak_sensors": _leak_sensors(cfg, temp),
        "carbon_periods": _carbon_periods(cfg, telemetry),
    }
    paths: dict[str, str] = {}
    for name, df in frames.items():
        path = os.path.join(out_dir, f"{name}.parquet")
        df.to_parquet(path, index=False)
        paths[name] = path

    graph = _graph(cfg)
    graph_path = os.path.join(out_dir, "graph.npz")
    np.savez(graph_path, **graph)
    paths["graph"] = graph_path

    manifest = {
        "seed": cfg.seed, "days": cfg.days, "fleet_size": cfg.fleet_size,
        "n_routes": cfg.n_routes, "step_min": STEP_MIN, "start_ts": str(START_TS),
        "failure_modes": cfg.failure_modes,
        "status": "SYNTHETIC BOOTSTRAP — replace with real telemetry when available",
    }
    manifest_path = os.path.join(out_dir, "manifest.json")
    with open(manifest_path, "w") as f:
        json.dump(manifest, f, indent=2)
    paths["manifest"] = manifest_path
    return paths


def main() -> None:
    ap = argparse.ArgumentParser(description="Generate synthetic H2Fleet datasets (bootstrap only).")
    ap.add_argument("--out", default="data/synth_out")
    ap.add_argument("--days", type=int, default=42)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()
    paths = generate(SynthConfig(days=args.days, seed=args.seed), args.out)
    for name, path in paths.items():
        print(f"{name}: {path}")


if __name__ == "__main__":
    main()
