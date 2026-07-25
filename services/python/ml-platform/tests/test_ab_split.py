"""A/B: deterministic variant assignment + champion/challenger loading."""

from __future__ import annotations

import json
import os
import shutil

import numpy as np

from app.ab import assign_variant
from app.serving import ModelServer

ARTIFACTS = os.environ.get("MODEL_ARTIFACTS_DIR", "")


def test_assignment_deterministic():
    assert assign_variant("m", "bus-1", 0.5, True) == assign_variant("m", "bus-1", 0.5, True)
    # different split can flip the variant, assignment never random per call
    variants = {assign_variant("m", f"bus-{i}", 0.5, True) for i in range(200)}
    assert variants == {"champion", "challenger"}


def test_assignment_respects_split_bounds():
    assert assign_variant("m", "bus-1", 0.0, True) == "champion"
    assert assign_variant("m", "bus-1", 1.0, True) == "challenger"
    assert assign_variant("m", "bus-1", 1.0, False) == "champion"


def test_assignment_fraction_approximates_split():
    n = 4000
    frac = sum(assign_variant("m", f"s-{i}", 0.1, True) == "challenger"
               for i in range(n)) / n
    assert 0.06 < frac < 0.14


def _two_variant_dir(tmp_path):
    """Copy champion artifacts into champion+challenger registry layout."""
    src = os.path.join(ARTIFACTS, "maintenance_lstm", "v1.0.0")
    if not os.path.isdir(src):
        return None
    dst = tmp_path / "ab_artifacts"
    shutil.copytree(os.path.join(ARTIFACTS, "maintenance_lstm"),
                    dst / "maintenance_lstm")
    shutil.copytree(src, dst / "maintenance_lstm" / "v9.9.9")
    with open(dst / "registry.json", "w") as f:
        json.dump({"champion": {"maintenance_lstm": "v1.0.0"},
                   "challenger": {"maintenance_lstm": "v9.9.9"}}, f)
    return str(dst)


def test_server_loads_champion_and_challenger(tmp_path):
    d = _two_variant_dir(tmp_path)
    if d is None:
        import pytest
        pytest.skip("trained artifacts not present")
    server = ModelServer(d, ab_split=0.5)
    assert set(server.models["maintenance_lstm"]) == {"champion", "challenger"}

    window = np.random.default_rng(0).uniform(10, 90, (48, 5)).tolist()
    variants = set()
    for i in range(40):
        res = server.maintenance_score(f"H2-{i:03d}", window)
        variants.add(res["variant"])
        assert res["model_version"] in ("v1.0.0", "v9.9.9")
    assert variants == {"champion", "challenger"}
    # deterministic: same subject -> same variant on repeat calls
    a = server.maintenance_score("H2-001", window)["variant"]
    b = server.maintenance_score("H2-001", window)["variant"]
    assert a == b


def test_server_without_challenger_always_champion(tmp_path):
    src = os.path.join(ARTIFACTS, "maintenance_lstm")
    if not os.path.isdir(src):
        import pytest
        pytest.skip("trained artifacts not present")
    dst = tmp_path / "single"
    shutil.copytree(src, dst / "maintenance_lstm")
    server = ModelServer(str(dst), ab_split=0.9)
    window = np.random.default_rng(0).uniform(10, 90, (48, 5)).tolist()
    for i in range(10):
        assert server.maintenance_score(f"H2-{i:03d}", window)["variant"] == "champion"
