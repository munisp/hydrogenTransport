"""Synthetic generator: determinism, schema seeding, leak clustering."""

from __future__ import annotations

import json
import os

import pandas as pd

from data.synth import (COMPONENTS, FLEET_NOS, STATION_NAMES, SynthConfig,
                        generate)


def _gen(tmp_path, name, seed=7, days=3):
    out = tmp_path / name
    generate(SynthConfig(days=days, seed=seed), str(out))
    return str(out)


def test_determinism_same_seed(tmp_path):
    a, b = _gen(tmp_path, "a"), _gen(tmp_path, "b")
    for frame in ("telemetry", "ridership", "leak_sensors", "carbon_periods"):
        da = pd.read_parquet(os.path.join(a, f"{frame}.parquet"))
        db = pd.read_parquet(os.path.join(b, f"{frame}.parquet"))
        pd.testing.assert_frame_equal(da, db)
    with open(os.path.join(a, "manifest.json")) as f:
        ma = json.load(f)
    with open(os.path.join(b, "manifest.json")) as f:
        mb = json.load(f)
    assert ma == mb


def test_different_seed_differs(tmp_path):
    a, b = _gen(tmp_path, "a", seed=7), _gen(tmp_path, "b", seed=8)
    da = pd.read_parquet(os.path.join(a, "telemetry.parquet"))
    db = pd.read_parquet(os.path.join(b, "telemetry.parquet"))
    assert not da["fuel_cell_kw"].equals(db["fuel_cell_kw"])


def test_schema_seeded_fleet(tmp_path):
    out = _gen(tmp_path, "a")
    tel = pd.read_parquet(os.path.join(out, "telemetry.parquet"))
    assert sorted(tel["fleet_no"].unique()) == FLEET_NOS  # 50 buses H2-001..050
    for col in ("speed_kph", "h2_level_pct", "fuel_cell_kw", "battery_soc_pct",
                "odometer_km", "ambient_temp_c"):
        assert col in tel.columns
    for c in COMPONENTS:
        assert f"days_to_failure_{c}" in tel.columns
        assert f"risk_label_{c}" in tel.columns
    # value ranges stay physical
    assert tel["h2_level_pct"].between(0, 100).all()
    assert tel["battery_soc_pct"].between(0, 100).all()
    assert tel["fuel_cell_kw"].between(0, 150).all()


def test_ridership_diurnal_and_weekend(tmp_path):
    out = _gen(tmp_path, "a", days=14)
    rd = pd.read_parquet(os.path.join(out, "ridership.parquet"))
    by_hour = rd[rd["is_weekend"] == 0].groupby("hour")["ridership"].mean()
    assert by_hour.idxmax() in (7.0, 8.0, 16.0, 17.0, 18.0)  # commute peaks
    wd = rd[rd["is_weekend"] == 0]["ridership"].mean()
    we = rd[rd["is_weekend"] == 1]["ridership"].mean()
    assert wd > we  # weekday demand exceeds weekend


def test_leaks_rare_but_clustered(tmp_path):
    out = _gen(tmp_path, "a", days=21)
    lk = pd.read_parquet(os.path.join(out, "leak_sensors.parquet"))
    positives = int(lk["is_leak"].sum())
    assert positives > 0
    assert lk["is_leak"].mean() < 0.05  # rare
    # clustered: number of separate leak episodes much smaller than points
    episodes = 0
    for _, g in lk.sort_values("ts").groupby("fleet_no"):
        s = g["is_leak"].to_numpy()
        episodes += int(((s == 1) & (pd.Series(s).shift(fill_value=0) == 0)).sum())
    assert episodes * 3 <= positives
    assert STATION_NAMES  # stations referenced by the generator contract
