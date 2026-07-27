"""Wave-5 multi-energy: ev_thermal synth domain, fleet mixes, dataset builder.

Run from the repo root:  python -m pytest services/python/ml-platform/tests -q
"""

from __future__ import annotations

import os

import numpy as np
import pandas as pd

from data.synth import (EV_PACK_CAPACITY_KWH, FLEET_MIXES, THERMAL_FALLBACK_FLEET,
                        SynthConfig, fleet_energy_types, generate)
from models import EV_THERMAL_FEATURES
from training import datasets as ds


def _gen(tmp_path, name, seed=7, days=3, fleet_mix="h2"):
    out = tmp_path / name
    generate(SynthConfig(days=days, seed=seed, fleet_mix=fleet_mix), str(out))
    return str(out)


class TestFleetEnergyTypes:
    def test_h2_mix_matches_legacy_fleet(self):
        types = fleet_energy_types(SynthConfig(fleet_mix="h2"))
        assert [fn for fn, _ in types] == [f"H2-{i:03d}" for i in range(1, 51)]
        assert {t for _, t in types} == {"h2"}

    def test_single_energy_mixes(self):
        for mix, prefix in (("battery", "EV"), ("diesel", "D")):
            types = fleet_energy_types(SynthConfig(fleet_mix=mix, fleet_size=10))
            assert len(types) == 10
            assert {t for _, t in types} == {mix}
            assert all(fn.startswith(f"{prefix}-") for fn, _ in types)

    def test_mixed_mix_has_all_types(self):
        types = fleet_energy_types(SynthConfig(fleet_mix="mixed", fleet_size=50))
        by_type = {}
        for fn, t in types:
            by_type.setdefault(t, []).append(fn)
        assert set(by_type) == {"h2", "battery", "diesel"}
        assert all(fn.startswith("H2-") for fn in by_type["h2"])
        assert all(fn.startswith("EV-") for fn in by_type["battery"])
        assert all(fn.startswith("D-") for fn in by_type["diesel"])

    def test_invalid_mix_rejected(self):
        try:
            fleet_energy_types(SynthConfig(fleet_mix="gng"))
        except ValueError:
            pass
        else:  # pragma: no cover
            raise AssertionError("invalid fleet_mix should raise")
        assert set(FLEET_MIXES) == {"h2", "battery", "diesel", "mixed"}


class TestBatteryThermalSynth:
    def test_frame_schema_and_labels(self, tmp_path):
        out = _gen(tmp_path, "a", days=4)
        th = pd.read_parquet(os.path.join(out, "battery_thermal.parquet"))
        for col in ("ts", "fleet_no", *EV_THERMAL_FEATURES, "is_anomaly"):
            assert col in th.columns
        assert set(th["is_anomaly"].unique()) == {0, 1}  # normal + episodes
        # physical ranges (normal rows)
        normal = th[th["is_anomaly"] == 0]
        assert normal["cell_temp_c"].between(-20, 60).all()
        assert normal["cell_voltage_v"].between(3.0, 4.2).all()
        assert (normal["pack_current_a"] >= 0).all()

    def test_fallback_thermal_fleet_for_h2_mix(self, tmp_path):
        out = _gen(tmp_path, "a")
        th = pd.read_parquet(os.path.join(out, "battery_thermal.parquet"))
        assert th["fleet_no"].nunique() == THERMAL_FALLBACK_FLEET
        assert all(fn.startswith("EV-") for fn in th["fleet_no"].unique())

    def test_battery_mix_uses_battery_buses(self, tmp_path):
        out = _gen(tmp_path, "a", fleet_mix="battery", days=2)
        th = pd.read_parquet(os.path.join(out, "battery_thermal.parquet"))
        tel = pd.read_parquet(os.path.join(out, "telemetry.parquet"))
        assert set(th["fleet_no"].unique()) == set(tel["fleet_no"].unique())

    def test_determinism(self, tmp_path):
        a, b = _gen(tmp_path, "a"), _gen(tmp_path, "b")
        da = pd.read_parquet(os.path.join(a, "battery_thermal.parquet"))
        db = pd.read_parquet(os.path.join(b, "battery_thermal.parquet"))
        pd.testing.assert_frame_equal(da, db)


class TestEnergyTelemetry:
    def test_h2_buses_write_both_level_columns(self, tmp_path):
        out = _gen(tmp_path, "a", days=2)
        tel = pd.read_parquet(os.path.join(out, "telemetry.parquet"))
        assert set(tel["energy_type"].unique()) == {"h2"}
        assert tel["h2_level_pct"].notna().all()
        pd.testing.assert_series_equal(tel["h2_level_pct"], tel["energy_level_pct"],
                                       check_names=False)
        pd.testing.assert_series_equal(tel["fuel_cell_kw"], tel["powertrain_kw"],
                                       check_names=False)

    def test_battery_bus_energy_contract(self, tmp_path):
        out = _gen(tmp_path, "a", days=2, fleet_mix="battery")
        tel = pd.read_parquet(os.path.join(out, "telemetry.parquet"))
        assert set(tel["energy_type"].unique()) == {"battery"}
        # h2 columns stay NULL for non-h2 buses (0008 contract: additive nullable)
        assert tel["h2_level_pct"].isna().all()
        assert tel["fuel_cell_kw"].isna().all()
        assert tel["energy_level_pct"].between(0, 100).all()
        assert tel["powertrain_kw"].between(0, 300).all()

    def test_mixed_mix_energy_types(self, tmp_path):
        out = _gen(tmp_path, "a", days=2, fleet_mix="mixed")
        tel = pd.read_parquet(os.path.join(out, "telemetry.parquet"))
        assert set(tel["energy_type"].unique()) == {"h2", "battery", "diesel"}
        h2 = tel[tel["energy_type"] == "h2"]
        ev = tel[tel["energy_type"] == "battery"]
        assert h2["h2_level_pct"].notna().all()
        assert ev["h2_level_pct"].isna().all()
        assert ev["energy_level_pct"].notna().all()

    def test_manifest_records_fleet_mix(self, tmp_path):
        import json

        out = _gen(tmp_path, "a", fleet_mix="diesel")
        with open(os.path.join(out, "manifest.json")) as f:
            assert json.load(f)["fleet_mix"] == "diesel"


class TestEvThermalDataset:
    def test_build_ev_thermal(self, tmp_path):
        out = _gen(tmp_path, "a", days=3)
        th = pd.read_parquet(os.path.join(out, "battery_thermal.parquet"))
        X, y, groups = ds.build_ev_thermal(th)
        assert X.shape == (len(th), len(EV_THERMAL_FEATURES))
        assert X.dtype == np.float32
        assert set(np.unique(y)) == {0, 1}
        assert len(groups) == len(th)

    def test_empty_frame_raises(self):
        import pytest

        with pytest.raises(RuntimeError, match="battery_thermal"):
            ds.build_ev_thermal(pd.DataFrame())

    def test_synth_loader_includes_battery_thermal(self, tmp_path):
        out = _gen(tmp_path, "a", days=2)
        frames = ds.load_frames("synth", out)
        assert "battery_thermal" in frames
        assert not frames["battery_thermal"].empty

    def test_pack_capacity_constant(self):
        assert EV_PACK_CAPACITY_KWH > 0
