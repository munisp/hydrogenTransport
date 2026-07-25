"""Shared fixtures: sys.path wiring, tiny synthetic dataset, env for the app.

Run from the repo root:  python -m pytest services/python/ml-platform/tests -q
"""

from __future__ import annotations

import os
import sys

import pytest

ML_PLATFORM = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHARED = os.path.abspath(os.path.join(ML_PLATFORM, "..", "shared"))
for p in (ML_PLATFORM, SHARED):
    if p not in sys.path:
        sys.path.insert(0, p)

# Point the inference app at the real trained artifacts shipped in the repo
# BEFORE app.config is imported anywhere.
os.environ.setdefault("MODEL_ARTIFACTS_DIR", os.path.join(ML_PLATFORM, "artifacts"))
os.environ.setdefault("DRIFT_INTERVAL_S", "3600")  # background loop stays idle in tests


@pytest.fixture(scope="session")
def tiny_synth_dir(tmp_path_factory):
    """Small synthetic dataset (7 days) for training smoke tests."""
    from data.synth import SynthConfig, generate

    out = tmp_path_factory.mktemp("synth")
    generate(SynthConfig(days=7, seed=7), str(out))
    return str(out)


@pytest.fixture(scope="session")
def tiny_frames(tiny_synth_dir):
    from training import datasets as ds

    return ds.load_frames("synth", tiny_synth_dir)


@pytest.fixture(scope="session")
def train_args():
    class A:
        seed = 0
        epochs = 1
        batch_size = 256
        lr = 1e-3
        max_windows = 40
        finetune_from = ""
        artifacts_dir = ""
        ray = False
        window = None
        source = "synth"

    return A()
