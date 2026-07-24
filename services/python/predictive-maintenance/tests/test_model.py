"""Tests for the rule-based fallback model and feature engineering.

Run: python -m pytest services/python/predictive-maintenance/tests -q
"""

from __future__ import annotations

import pytest

from app.features import FEATURES, feature_vector
from app.model import COMPONENTS, RuleModel, SklearnModel, _horizon, load_model


def healthy_features() -> dict[str, float]:
    """Synthetic feature window for a bus in good condition."""
    return {
        "n_samples": 1440.0,  # ~24h at 1 sample/min
        "avg_speed_kph": 30.0,
        "max_speed_kph": 55.0,
        "avg_fuel_cell_kw": 40.0,
        "max_fuel_cell_kw": 80.0,
        "std_fuel_cell_kw": 10.0,
        "avg_h2_level_pct": 55.0,
        "min_h2_level_pct": 30.0,
        "refuel_events": 1.0,
        "avg_battery_soc_pct": 65.0,
        "min_battery_soc_pct": 45.0,
        "std_battery_soc_pct": 8.0,
        "km_driven": 250.0,
    }


def degraded_features() -> dict[str, float]:
    """Synthetic window showing fuel-cell stress, run-dry tank and deep
    battery discharge — every risk driver above its rule threshold."""
    return {
        "n_samples": 1440.0,
        "avg_speed_kph": 60.0,
        "max_speed_kph": 100.0,
        "avg_fuel_cell_kw": 85.0,
        "max_fuel_cell_kw": 130.0,
        "std_fuel_cell_kw": 40.0,
        "avg_h2_level_pct": 20.0,
        "min_h2_level_pct": 3.0,
        "refuel_events": 8.0,
        "avg_battery_soc_pct": 92.0,
        "min_battery_soc_pct": 5.0,
        "std_battery_soc_pct": 30.0,
        "km_driven": 450.0,
    }


class TestRuleModelPredictAll:
    def test_shape_and_component_order(self):
        risks = RuleModel().predict_all(healthy_features())
        assert [r.component for r in risks] == COMPONENTS
        assert len(risks) == 4

    def test_scores_within_unit_bounds(self):
        for name, feats in [("healthy", healthy_features()), ("degraded", degraded_features()),
                            ("empty", {}), ("extreme", {k: 1e9 for k in FEATURES})]:
            for r in RuleModel().predict_all(feats):
                assert 0.0 <= r.risk_score <= 1.0, f"{name}/{r.component}: {r.risk_score} out of [0,1]"
                assert r.horizon_days >= 3, f"{name}/{r.component}: horizon below floor"

    def test_degraded_scores_above_healthy(self):
        healthy = {r.component: r.risk_score for r in RuleModel().predict_all(healthy_features())}
        degraded = {r.component: r.risk_score for r in RuleModel().predict_all(degraded_features())}
        for comp in COMPONENTS:
            assert degraded[comp] > healthy[comp], (
                f"{comp}: degraded {degraded[comp]} should exceed healthy {healthy[comp]}"
            )

    def test_missing_features_default_to_safe(self):
        # An empty feature dict must still produce a valid (low-risk) result —
        # the service degrades gracefully when telemetry is sparse.
        risks = RuleModel().predict_all({})
        assert all(0.0 <= r.risk_score <= 1.0 for r in risks)
        assert all(r.risk_score <= 0.2 for r in risks)

    def test_horizon_monotonic_and_bounded(self):
        assert _horizon(0.0) == 60
        assert _horizon(1.0) == 3  # floor
        assert _horizon(0.5) in range(3, 61)
        assert _horizon(0.9) < _horizon(0.1)


class TestFeatureEngineering:
    def test_feature_vector_order_matches_feature_list(self):
        feats = {name: float(i + 1) for i, name in enumerate(FEATURES)}
        assert feature_vector(feats) == [float(i + 1) for i in range(len(FEATURES))]

    def test_feature_vector_missing_keys_default_zero(self):
        vec = feature_vector({"n_samples": 7.0})
        assert vec[0] == 7.0
        assert all(v == 0.0 for v in vec[1:])
        assert len(vec) == len(FEATURES)

    def test_feature_list_covers_synthetic_telemetry_signals(self):
        # The aggregation SQL derives these from fleet.telemetry rows; pin the
        # exact contract shared with train.py.
        assert FEATURES == [
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


class _FakeClf:
    def __init__(self, proba: float):
        self._proba = proba

    def predict_proba(self, x):
        return [[1.0 - self._proba, self._proba]]


class TestSklearnModel:
    def test_feature_mismatch_raises(self):
        model = SklearnModel({"models": {}, "features": ["wrong"], "version": "t"})
        with pytest.raises(ValueError, match="features"):
            model.predict_all(healthy_features())

    def test_missing_components_fall_back_to_rules(self):
        artifact = {
            "models": {"fuel_cell": _FakeClf(0.9)},
            "features": list(FEATURES),
            "version": "t",
        }
        risks = SklearnModel(artifact).predict_all(healthy_features())
        by_comp = {r.component: r for r in risks}
        assert by_comp["fuel_cell"].risk_score == pytest.approx(0.9)
        # The other three come from the rule fallback.
        rules = {r.component: r for r in RuleModel().predict_all(healthy_features())}
        for comp in ("compressor", "tank_valve", "battery"):
            assert by_comp[comp].risk_score == rules[comp].risk_score
        assert [r.component for r in risks] == COMPONENTS


def test_load_model_falls_back_for_missing_artifact(tmp_path):
    model = load_model(str(tmp_path / "does-not-exist.joblib"))
    assert isinstance(model, RuleModel)
    assert model.version == "rules-v1"
