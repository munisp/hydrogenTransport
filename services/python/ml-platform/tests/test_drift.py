"""Drift monitor PSI/KS sanity: in-distribution ~ 0, shifted >> warn level."""

from __future__ import annotations

import numpy as np

from app.drift import FeatureDriftMonitor, psi


def _schema(mu=0.0, sigma=1.0):
    rng = np.random.default_rng(0)
    sample = rng.normal(mu, sigma, 20000)
    edges = np.quantile(sample, np.linspace(0, 1, 11))
    edges[0], edges[-1] = -np.inf, np.inf
    counts, edges = np.histogram(sample, bins=edges)
    return {
        "features": ["x"],
        "baseline": {"x": {"bin_edges": [float(e) for e in edges[1:-1]],
                           "proportions": (counts / counts.sum()).tolist()}},
    }


def test_psi_identical_is_zero():
    p = np.array([0.1, 0.2, 0.4, 0.2, 0.1])
    q = np.array([0.4, 0.2, 0.1, 0.2, 0.1])
    assert psi(p, p) < 1e-9
    assert psi(p, q) > 0  # mass shifted between bins is detected


def test_indistribution_no_drift():
    mon = FeatureDriftMonitor("m", _schema(), window=2048, psi_warn=0.2)
    mon.observe(np.random.default_rng(1).normal(0, 1, (512, 1)))
    snap = mon.compute()
    assert snap["status"] == "ok"
    assert snap["features"]["x"]["psi"] < 0.2
    assert snap["features"]["x"]["ks"] < 0.2


def test_shifted_distribution_drifts():
    mon = FeatureDriftMonitor("m", _schema(), window=2048, psi_warn=0.2)
    mon.observe(np.random.default_rng(1).normal(3.0, 1.0, (512, 1)))  # mean shift
    snap = mon.compute()
    assert snap["status"] == "drift"
    assert snap["features"]["x"]["psi"] > 1.0
    assert snap["features"]["x"]["drifted"] is True
    assert snap["worst_psi"] == snap["features"]["x"]["psi"]


def test_insufficient_data_status():
    mon = FeatureDriftMonitor("m", _schema())
    mon.observe(np.zeros((5, 1)))
    assert mon.compute()["status"] == "insufficient-data"


def test_ring_buffer_evicts_old():
    mon = FeatureDriftMonitor("m", _schema(), window=64, psi_warn=0.2)
    mon.observe(np.random.default_rng(1).normal(5.0, 1.0, (64, 1)))   # drifted
    mon.observe(np.random.default_rng(2).normal(0.0, 1.0, (64, 1)))   # evicts
    snap = mon.compute()
    assert snap["status"] == "ok"
