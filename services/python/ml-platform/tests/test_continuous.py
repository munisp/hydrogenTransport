"""Continuous training smoke: fine-tune -> evaluate -> promote-if-better."""

from __future__ import annotations

import os

from app.registry import read_registry
from training import continuous


class _Args:
    source = "synth"
    database_url = ""
    lakehouse_dir = ""
    epochs = 1
    batch_size = 64
    lr = 3e-3
    seed = 1
    days = 7
    max_windows = 40
    device = "cpu"
    ray = False
    once = True
    version = ""
    register = False
    finetune_from = ""
    window = None

    def __init__(self, data_dir, artifacts_dir, models):
        self.data_dir = data_dir
        self.artifacts_dir = artifacts_dir
        self.models = models


def test_iterate_promotes_first_candidate(tiny_synth_dir, tmp_path):
    args = _Args(tiny_synth_dir, str(tmp_path), ["carbon_forecaster"])
    outcomes = continuous.iterate(args)
    assert outcomes[0]["model"] == "carbon_forecaster"
    assert outcomes[0]["promoted"] is True          # no champion existed
    reg = read_registry(str(tmp_path))
    assert reg["champion"]["carbon_forecaster"] == outcomes[0]["candidate"]
    # artifact on disk
    assert os.path.isdir(os.path.join(
        str(tmp_path), "carbon_forecaster", outcomes[0]["candidate"]))


def test_iterate_finetunes_and_keeps_champion_gate(tiny_synth_dir, tmp_path):
    args = _Args(tiny_synth_dir, str(tmp_path), ["carbon_forecaster"])
    first = continuous.iterate(args)[0]
    second = continuous.iterate(args)[0]            # fine-tunes from champion
    reg = read_registry(str(tmp_path))
    # second candidate is always recorded; champion only replaced when better
    if second["promoted"]:
        assert reg["champion"]["carbon_forecaster"] == second["candidate"]
    else:
        assert reg["champion"]["carbon_forecaster"] == first["candidate"]
        assert reg["challenger"]["carbon_forecaster"] == second["candidate"]
