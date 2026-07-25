"""LSTM artifact preference in predictive-maintenance.

The trained ml-platform maintenance_lstm artifact (shipped in the monorepo
under services/python/ml-platform/artifacts) is preferred over the legacy
sklearn joblib and the rule fallback. The HTTP API contract is unchanged.
"""

from __future__ import annotations

import os

import numpy as np
import pytest

from app.model import COMPONENTS, RuleModel, SklearnModel, load_model

ML_PLATFORM_ARTIFACTS = os.path.abspath(os.path.join(
    os.path.dirname(__file__), "..", "..", "ml-platform", "artifacts"))

HAVE_ARTIFACT = os.path.isdir(os.path.join(ML_PLATFORM_ARTIFACTS, "maintenance_lstm"))


def _sequence(n=48):
    rng = np.random.default_rng(0)
    return np.column_stack([
        rng.uniform(30, 90, n),   # h2_level_pct
        rng.uniform(20, 90, n),   # fuel_cell_kw
        rng.uniform(30, 95, n),   # battery_soc_pct
        rng.uniform(0, 60, n),    # speed_kph
        rng.uniform(-5, 30, n),   # ambient_temp_c
    ]).astype("float32")


@pytest.mark.skipif(not HAVE_ARTIFACT, reason="ml-platform artifacts not present")
def test_lstm_artifact_loads_and_scores():
    from app.lstm_model import LSTMScorer

    model = load_model("models/does-not-exist.joblib", ML_PLATFORM_ARTIFACTS)
    assert isinstance(model, LSTMScorer)
    assert model.needs_sequence is True
    assert model.version.startswith("lstm-")

    risks = model.predict_all({"_sequence": _sequence()})
    assert [r.component for r in risks] == COMPONENTS
    for r in risks:
        assert 0.0 <= r.risk_score <= 1.0
        assert r.horizon_days >= 1


@pytest.mark.skipif(not HAVE_ARTIFACT, reason="ml-platform artifacts not present")
def test_lstm_missing_sequence_raises():
    model = load_model("models/does-not-exist.joblib", ML_PLATFORM_ARTIFACTS)
    with pytest.raises(ValueError, match="_sequence"):
        model.predict_all({})


def test_fallback_to_rules_when_no_artifacts(tmp_path):
    model = load_model(str(tmp_path / "missing.joblib"), str(tmp_path))
    assert isinstance(model, RuleModel)
    assert model.version == "rules-v1"


def test_corrupt_lstm_dir_falls_through(tmp_path):
    # An artifacts dir with garbage must not take the service down.
    bad = tmp_path / "artifacts" / "maintenance_lstm" / "vBad"
    bad.mkdir(parents=True)
    (bad / "weights.pt").write_bytes(b"not a checkpoint")
    (bad / "feature_schema.json").write_text("{}")
    model = load_model(str(tmp_path / "missing.joblib"), str(tmp_path / "artifacts"))
    assert isinstance(model, RuleModel)
