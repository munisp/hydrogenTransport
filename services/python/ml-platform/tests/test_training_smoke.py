"""1-epoch training smoke per model on a tiny synthetic dataset.

Asserts each trainer converges through a real loop and produces a valid
artifact bundle (weights + metrics + feature schema) within the param budget.
"""

from __future__ import annotations

import json
import os

import pytest

from app.registry import load_artifact, save_artifact
from training.train import TRAINERS


@pytest.mark.parametrize("model", list(TRAINERS))
def test_one_epoch_smoke(model, tiny_frames, train_args, tmp_path):
    train_args.artifacts_dir = str(tmp_path)
    net, metrics, schema, model_config = TRAINERS[model](tiny_frames, train_args, "cpu")

    n_params = sum(p.numel() for p in net.parameters())
    assert n_params <= 200_000
    assert metrics["primary_metric"] in metrics or "primary_value" in metrics
    assert isinstance(metrics["primary_value"], float)
    assert schema["features"] and schema["stats"]
    assert "baseline" in schema

    out = save_artifact(str(tmp_path), model, "vTest", net, model_config,
                        metrics, schema)
    for fname in ("weights.pt", "metrics.json", "feature_schema.json"):
        assert os.path.exists(os.path.join(out, fname))
    net2, metrics2, schema2 = load_artifact(str(tmp_path), model, "vTest")
    assert schema2["features"] == schema["features"]
    with open(os.path.join(out, "metrics.json")) as f:
        assert json.load(f)["model"] == model
