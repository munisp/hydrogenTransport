"""Dataset loaders + feature builders for all five models.

Three sources, selected with ``--source``:

* ``synth``    — parquet produced by ``data/synth.py`` (generated on demand
                 into ``--data-dir`` when absent). BOOTSTRAP ONLY.
* ``postgres`` — live platform OLTP (``fleet.telemetry``, ``infra.incidents``,
                 ``commerce.fare_payments``). Ambient temperature is not stored
                 in the schema, so a deterministic seasonal estimate from the
                 row timestamp is used (documented approximation). Failure
                 labels are derived from ``infra.incidents`` of type
                 ``fuel-cell-fault``/``leak``; until enough incidents exist the
                 maintenance model simply has few/no positives.
* ``iceberg``  — lakehouse parquet layout (Spark/Iceberg output on MinIO
                 mirrored to a readable dir, ``--lakehouse-dir`` or
                 ``LAKEHOUSE_DIR``). Expected partitions:
                 ``telemetry/``, ``ridership/``, ``leak_sensors/``,
                 ``carbon_periods/`` plus ``graph.npz``.

Each builder returns numpy arrays + a feature-schema dict (feature names +
train-split mean/std) which is shipped inside the model artifact so inference
applies identical normalisation (drift monitor reuses it as the baseline).
"""

from __future__ import annotations

import logging
import os

import numpy as np
import pandas as pd

from data.synth import FLEET_NOS, SynthConfig, generate
from models import (CARBON_FEATURES, DEMAND_FEATURES, GRAPH_NODE_FEATURES,
                    LEAK_SENSOR_FEATURES, SEQ_FEATURES)

log = logging.getLogger("ml-platform.datasets")

SYNTH_FILENAMES = ["telemetry", "ridership", "leak_sensors", "carbon_periods"]


# ------------------------------------------------------------------ sources --
def load_frames(source: str, data_dir: str, database_url: str = "",
                lakehouse_dir: str = "", days: int = 42, seed: int = 42) -> dict[str, object]:
    """Return {'telemetry','ridership','leak_sensors','carbon_periods','graph'}."""
    if source == "synth":
        manifest = os.path.join(data_dir, "manifest.json")
        if not os.path.exists(manifest):
            log.info("synthetic data not found in %s — generating (days=%d seed=%d)",
                     data_dir, days, seed)
            generate(SynthConfig(days=days, seed=seed), data_dir)
        frames = {name: pd.read_parquet(os.path.join(data_dir, f"{name}.parquet"))
                  for name in SYNTH_FILENAMES}
        graph = np.load(os.path.join(data_dir, "graph.npz"), allow_pickle=False)
        frames["graph"] = {k: graph[k] for k in graph.files}
        return frames
    if source == "postgres":
        return _load_postgres(database_url)
    if source == "iceberg":
        return _load_iceberg(lakehouse_dir)
    raise ValueError(f"unknown --source {source!r} (expected synth|postgres|iceberg)")


def _seasonal_temp(ts: pd.Series) -> np.ndarray:
    doy = ts.dt.dayofyear.to_numpy()
    hour = ts.dt.hour.to_numpy() + ts.dt.minute.to_numpy() / 60.0
    return 10.0 + 9.0 * np.sin(2 * np.pi * (doy - 100) / 365.0) + 4.0 * np.sin(2 * np.pi * (hour - 14) / 24.0)


def _load_postgres(database_url: str) -> dict[str, object]:
    if not database_url:
        raise ValueError("--source postgres requires DATABASE_URL/--database-url")
    import psycopg  # pinned in requirements; imported lazily for synth-only runs

    with psycopg.connect(database_url) as conn:
        tel = pd.read_sql(
            """
            SELECT t.ts, v.fleet_no, t.speed_kph, t.h2_level_pct, t.fuel_cell_kw,
                   t.battery_soc_pct, t.odometer_km
            FROM fleet.telemetry t JOIN fleet.vehicles v ON v.id = t.bus_id
            ORDER BY v.fleet_no, t.ts
            """, conn)
        incidents = pd.read_sql(
            "SELECT type, bus_id, opened_at FROM infra.incidents", conn)
        fares = pd.read_sql(
            "SELECT created_at, amount_minor FROM commerce.fare_payments "
            "WHERE status = 'settled'", conn)

    if tel.empty:
        raise RuntimeError("fleet.telemetry is empty — no training data yet; use --source synth")
    tel["ambient_temp_c"] = _seasonal_temp(tel["ts"])

    # Failure labels from incidents: fuel-cell-fault -> fuel_cell failure,
    # leak -> tank_valve failure. days_to_failure measured to the incident.
    for c in ["fuel_cell", "compressor", "tank_valve", "battery"]:
        tel[f"days_to_failure_{c}"] = np.float32(60.0)
        tel[f"risk_label_{c}"] = np.int8(0)
    if not incidents.empty:
        # map bus uuid -> fleet_no via a second query (kept explicit)
        with psycopg.connect(database_url) as conn:
            vmap = pd.read_sql("SELECT id, fleet_no FROM fleet.vehicles", conn)
        id_to_fleet = dict(zip(vmap["id"].astype(str), vmap["fleet_no"]))
        comp_map = {"fuel-cell-fault": "fuel_cell", "leak": "tank_valve"}
        for _, inc in incidents.iterrows():
            comp = comp_map.get(str(inc["type"]))
            fleet_no = id_to_fleet.get(str(inc["bus_id"]))
            if not comp or not fleet_no:
                continue
            mask = (tel["fleet_no"] == fleet_no) & (tel["ts"] <= inc["opened_at"])
            dtf = ((inc["opened_at"] - tel.loc[mask, "ts"]).dt.total_seconds() / 86400.0).clip(0, 60)
            tel.loc[mask, f"days_to_failure_{comp}"] = dtf.astype(np.float32)
            tel.loc[mask, f"risk_label_{comp}"] = (dtf <= 14).astype(np.int8)

    # Ridership proxy: settled fares per hour on a single 'ALL' route grain
    # (routes are not recorded in commerce.fare_payments yet).
    if fares.empty:
        ridership = pd.DataFrame(columns=["ts", "route_id", "ridership", "hour", "dow",
                                          "is_weekend", "temp_c", "precip_mm"])
    else:
        hourly = fares.set_index("created_at").resample("1h").size().rename("ridership").reset_index()
        hourly = hourly.rename(columns={"created_at": "ts"})
        hourly["route_id"] = "ALL"
        hourly["hour"] = hourly["ts"].dt.hour.astype(np.float32)
        hourly["dow"] = hourly["ts"].dt.dayofweek.astype(np.float32)
        hourly["is_weekend"] = (hourly["dow"] >= 5).astype(np.float32)
        hourly["temp_c"] = _seasonal_temp(hourly["ts"])
        hourly["precip_mm"] = 0.0
        ridership = hourly

    frames: dict[str, object] = {
        "telemetry": tel, "ridership": ridership,
        "leak_sensors": pd.DataFrame(columns=["ts", "fleet_no"] + LEAK_SENSOR_FEATURES + ["is_leak"]),
        "carbon_periods": _carbon_from_telemetry(tel),
        "graph": _graph_from_static(),
    }
    return frames


def _carbon_from_telemetry(tel: pd.DataFrame) -> pd.DataFrame:
    t = tel.copy()
    t["period"] = t["ts"].dt.to_period("W").astype(str)
    rows = []
    prev: dict[str, float] = {}
    for (period, fleet_no), g in t.groupby(["period", "fleet_no"]):
        km = float(g["odometer_km"].max() - g["odometer_km"].min())
        h2_kg = float((g["fuel_cell_kw"] * 0.0011 * 5).sum())
        rows.append({
            "period": period, "fleet_no": fleet_no, "total_km": km,
            "h2_consumed_kg": h2_kg, "total_ridership": km * 3.0,
            "active_buses": 1, "avg_temp_c": float(g["ambient_temp_c"].mean()),
            "weekday_frac": float((g["ts"].dt.dayofweek < 5).mean()),
            "period_days": int(g["ts"].dt.date.nunique()),
            "prev_kg_co2_avoided": prev.get(fleet_no, 200.0),
            "kg_co2_avoided": km * 1.15,
        })
        prev[fleet_no] = rows[-1]["kg_co2_avoided"]
    return pd.DataFrame(rows)


def _graph_from_static() -> dict[str, np.ndarray]:
    """Fallback static graph (same topology as synth) when no route topology
    table exists yet; export_graph.py refreshes the real one from Postgres."""
    from data.synth import _graph, SynthConfig as _Cfg
    return _graph(_Cfg())


def _load_iceberg(lakehouse_dir: str) -> dict[str, object]:
    if not lakehouse_dir:
        lakehouse_dir = os.environ.get("LAKEHOUSE_DIR", "")
    if not lakehouse_dir or not os.path.isdir(lakehouse_dir):
        raise ValueError("--source iceberg requires --lakehouse-dir or LAKEHOUSE_DIR "
                         "pointing at the lakehouse parquet export")
    frames: dict[str, object] = {}
    for name in SYNTH_FILENAMES:
        part = os.path.join(lakehouse_dir, name)
        if os.path.isdir(part):
            frames[name] = pd.read_parquet(part)
        elif os.path.exists(part + ".parquet"):
            frames[name] = pd.read_parquet(part + ".parquet")
        else:
            frames[name] = pd.DataFrame()
    graph_path = os.path.join(lakehouse_dir, "graph.npz")
    if os.path.exists(graph_path):
        g = np.load(graph_path, allow_pickle=False)
        frames["graph"] = {k: g[k] for k in g.files}
    else:
        frames["graph"] = _graph_from_static()
    return frames


# ------------------------------------------------------------- stats helper --
def feature_stats_from_tensor(arr: np.ndarray, features: list[str]) -> dict[str, dict[str, float]]:
    """Mean/std per feature over a (..., F) array (train split only)."""
    flat = arr.reshape(-1, arr.shape[-1]) if arr.ndim > 2 else arr
    stats = {}
    for i, f in enumerate(features):
        col = flat[:, i]
        stats[f] = {"mean": float(col.mean()), "std": float(col.std()) + 1e-6}
    return stats


def feature_stats(df: pd.DataFrame, features: list[str]) -> dict[str, dict[str, float]]:
    return {f: {"mean": float(df[f].mean()), "std": float(df[f].std() or 1.0) + 1e-6}
            for f in features}


def zscore(arr: np.ndarray, stats: dict[str, dict[str, float]], features: list[str]) -> np.ndarray:
    out = arr.copy()
    for i, f in enumerate(features):
        out[..., i] = (out[..., i] - stats[f]["mean"]) / stats[f]["std"]
    return out.astype(np.float32)


def split_by_group(groups: np.ndarray, seed: int = 0,
                   ratios: tuple[float, float, float] = (0.7, 0.15, 0.15)):
    """Deterministic group-aware train/val/test split (no bus/route leakage)."""
    uniq = np.unique(groups)
    rng = np.random.default_rng(seed)
    rng.shuffle(uniq)
    n = len(uniq)
    n_tr, n_va = int(ratios[0] * n), int(ratios[1] * n)
    tr, va, te = set(uniq[:n_tr]), set(uniq[n_tr:n_tr + n_va]), set(uniq[n_tr + n_va:])
    mask = np.array([g in tr for g in groups]), np.array([g in va for g in groups]), np.array([g in te for g in groups])
    return mask  # type: ignore[return-value]


# ------------------------------------------------------------- per-model ----
def build_maintenance(telemetry: pd.DataFrame, window: int = 48, stride: int = 6,
                      max_windows_per_bus: int = 1200, seed: int = 0):
    """Sliding windows over SEQ_FEATURES -> (X, risk_y, days_y, stats, groups)."""
    Xs, RYs, DYs, Gs = [], [], [], []
    rng = np.random.default_rng(seed)
    for fleet_no, g in telemetry.groupby("fleet_no"):
        g = g.sort_values("ts")
        feats = g[SEQ_FEATURES].to_numpy(np.float32)
        risk = g[[f"risk_label_{c}" for c in ["fuel_cell", "compressor", "tank_valve", "battery"]]].to_numpy(np.float32)
        days = g[[f"days_to_failure_{c}" for c in ["fuel_cell", "compressor", "tank_valve", "battery"]]].to_numpy(np.float32)
        idx = np.arange(window, len(g), stride)
        if len(idx) > max_windows_per_bus:
            idx = rng.choice(idx, max_windows_per_bus, replace=False)
            idx.sort()
        for i in idx:
            Xs.append(feats[i - window:i])
            RYs.append(risk[i - 1])
            DYs.append(days[i - 1])
            Gs.append(fleet_no)
    if not Xs:
        raise RuntimeError("no maintenance windows could be built (telemetry too short?)")
    X = np.stack(Xs)
    return X, np.stack(RYs), np.stack(DYs), np.array(Gs)


def build_demand(ridership: pd.DataFrame, window: int = 72, horizon: int = 24,
                 stride: int = 6):
    """-> (X, Y, route_groups, window_end_ordinals).

    The forecaster is deployed per route, so evaluation splits by TIME (not by
    route): the window-end ordinal drives the chronological train/val/test
    split, which is the honest forecast-generalisation question.
    """
    if ridership.empty:
        raise RuntimeError("ridership frame is empty for the selected source")
    df = ridership.copy().sort_values(["route_id", "ts"])
    df["hour_sin"] = np.sin(2 * np.pi * df["hour"] / 24)
    df["hour_cos"] = np.cos(2 * np.pi * df["hour"] / 24)
    df["dow_sin"] = np.sin(2 * np.pi * df["dow"] / 7)
    df["dow_cos"] = np.cos(2 * np.pi * df["dow"] / 7)
    Xs, Ys, Gs, Es = [], [], [], []
    for route, g in df.groupby("route_id"):
        feats = g[DEMAND_FEATURES].to_numpy(np.float32)
        target = g["ridership"].to_numpy(np.float32)
        for i in range(window, len(g) - horizon, stride):
            Xs.append(feats[i - window:i])
            Ys.append(target[i:i + horizon])
            Gs.append(route)
            Es.append(i + horizon)   # hours since series start at window end
    if not Xs:
        raise RuntimeError("ridership history too short for demand windows")
    return np.stack(Xs), np.stack(Ys), np.array(Gs), np.array(Es)


def split_chronological(ends: np.ndarray,
                        ratios: tuple[float, float, float] = (0.7, 0.15, 0.15)):
    q1 = np.quantile(ends, ratios[0])
    q2 = np.quantile(ends, ratios[0] + ratios[1])
    return ends <= q1, (ends > q1) & (ends <= q2), ends > q2


def build_leak(leaks: pd.DataFrame):
    if leaks.empty:
        raise RuntimeError("leak_sensors frame is empty for the selected source")
    X = leaks[LEAK_SENSOR_FEATURES].to_numpy(np.float32)
    y = leaks["is_leak"].to_numpy(np.int64)
    groups = leaks["fleet_no"].to_numpy() if "fleet_no" in leaks else np.arange(len(leaks))
    return X, y, groups


def build_carbon(periods: pd.DataFrame):
    if periods.empty:
        raise RuntimeError("carbon_periods frame is empty for the selected source")
    X = periods[CARBON_FEATURES].to_numpy(np.float32)
    y = periods["kg_co2_avoided"].to_numpy(np.float32)
    groups = periods["fleet_no"].to_numpy() if "fleet_no" in periods else periods["period"].to_numpy()
    return X, y, groups


def build_graph(graph: dict[str, np.ndarray]):
    return (graph["adjacency"].astype(np.float32),
            graph["node_features"].astype(np.float32),
            graph["delay_target"].astype(np.float32),
            graph["energy_target"].astype(np.float32),
            [str(n) for n in graph["node_names"]])
