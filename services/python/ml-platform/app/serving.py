"""Artifact loading + CPU inference for all six models.

Loads champion (and challenger when a registry entry exists) per model at
startup from MODEL_ARTIFACTS_DIR. Every predict path:
  1. validates/normalises features with the artifact's feature_schema stats
  2. picks the variant via deterministic A/B assignment
  3. runs torch inference under no_grad on CPU
  4. feeds the raw features to the drift monitor
  5. returns (result dict, variant, version) — responses + logs are variant-tagged
"""

from __future__ import annotations

import logging
import os
import threading

import numpy as np
import torch

from models import ANOMALY_DOMAIN_MODELS, normalize_adjacency
from models.maintenance_lstm import COMPONENTS

from .ab import assign_variant
from .drift import FeatureDriftMonitor
from .registry import MODEL_NAMES, load_artifact, read_registry, resolve_version

log = logging.getLogger("ml-platform.serving")


class ModelBundle:
    def __init__(self, net, metrics: dict, schema: dict, version: str):
        self.net = net
        self.metrics = metrics
        self.schema = schema
        self.version = version

    def normalize(self, rows: np.ndarray) -> np.ndarray:
        stats = self.schema["stats"]
        out = np.asarray(rows, dtype=np.float32).copy()
        for i, f in enumerate(self.schema["features"]):
            out[..., i] = (out[..., i] - stats[f]["mean"]) / stats[f]["std"]
        return out


class ModelServer:
    def __init__(self, artifacts_dir: str, ab_split: float = 0.1,
                 drift_window: int = 512, drift_psi_warn: float = 0.2):
        self.artifacts_dir = artifacts_dir
        self.ab_split = ab_split
        self._lock = threading.Lock()
        self.models: dict[str, dict[str, ModelBundle]] = {}
        self.monitors: dict[str, FeatureDriftMonitor] = {}
        for name in MODEL_NAMES:
            self._load(name, drift_window, drift_psi_warn)

    # ------------------------------------------------------------- loading --
    def _load(self, name: str, drift_window: int, psi_warn: float) -> None:
        bundles: dict[str, ModelBundle] = {}
        for role in ("champion", "challenger"):
            version = resolve_version(self.artifacts_dir, name, role)
            if not version:
                continue
            if role == "challenger" and version == bundles.get("champion", ModelBundle(None, {}, {}, "")).version:
                continue
            try:
                net, metrics, schema = load_artifact(self.artifacts_dir, name, version)
                bundles[role] = ModelBundle(net, metrics, schema, version)
                log.info("loaded %s %s=%s (params=%s)", name, role, version,
                         metrics.get("n_params"))
            except Exception:
                log.exception("failed to load artifact %s/%s (%s)", name, version, role)
        if "champion" in bundles:
            self.models[name] = bundles
            self.monitors[name] = FeatureDriftMonitor(
                name, bundles["champion"].schema, drift_window, psi_warn)
        else:
            log.warning("no loadable artifact for %s — endpoint will 503", name)

    def reload(self, name: str, drift_window: int = 512, psi_warn: float = 0.2) -> None:
        with self._lock:
            self._load(name, drift_window, psi_warn)

    # ------------------------------------------------------------- helpers --
    def _pick(self, name: str, subject_key: str) -> tuple[ModelBundle, str]:
        bundles = self.models.get(name)
        if not bundles:
            raise ModelUnavailable(name)
        variant = assign_variant(name, subject_key, self.ab_split,
                                 "challenger" in bundles)
        return bundles[variant], variant

    def info(self) -> dict:
        out = {}
        reg = read_registry(self.artifacts_dir)
        for name in MODEL_NAMES:
            bundles = self.models.get(name, {})
            out[name] = {
                role: {"version": b.version, "metrics": b.metrics.get("metrics", b.metrics),
                       "n_params": b.metrics.get("n_params"),
                       "trained_at": b.metrics.get("trained_at")}
                for role, b in bundles.items()
            }
            out[name]["registry"] = {"champion": reg.get("champion", {}).get(name),
                                     "challenger": reg.get("challenger", {}).get(name)}
            out[name]["loaded"] = bool(bundles)
        return out

    # ----------------------------------------------------------- predictions --
    def maintenance_score(self, subject: str, window: list[list[float]]) -> dict:
        bundle, variant = self._pick("maintenance_lstm", subject)
        x = np.asarray(window, dtype=np.float32)
        self.monitors["maintenance_lstm"].observe(x)
        xn = bundle.normalize(x)[None, ...]
        with torch.no_grad():
            risk, days = bundle.net(torch.tensor(xn))
        risk, days = risk[0].tolist(), days[0].tolist()
        return {
            "predictions": [
                {"component": c, "risk_score": round(float(risk[i]), 4),
                 "days_to_failure": round(float(days[i]), 1)}
                for i, c in enumerate(COMPONENTS)
            ],
            "variant": variant, "model_version": bundle.version,
        }

    def demand_forecast(self, route_id: str, history_rows: np.ndarray) -> dict:
        bundle, variant = self._pick("demand_forecaster", route_id)
        self.monitors["demand_forecaster"].observe(history_rows)
        rows = np.asarray(history_rows, dtype=np.float32)
        if bundle.schema.get("extra", {}).get("per_window_scaling") == "ridership_mean":
            # replicate training: scale ridership channel + target by the
            # window's own mean (shape learning, route-scale invariant)
            scale = max(float(rows[:, 0].mean()), 1.0)
            rows = rows.copy()
            rows[:, 0] /= scale
            xn = bundle.normalize(rows)[None, ...]
            with torch.no_grad():
                y = bundle.net(torch.tensor(xn))[0].tolist()
            riders = [max(0.0, float(v) * scale) for v in y]
        else:
            xn = bundle.normalize(rows)[None, ...]
            with torch.no_grad():
                y = bundle.net(torch.tensor(xn))[0].tolist()
            stats = bundle.schema["stats"]["ridership"]
            riders = [max(0.0, float(v)) for v in np.asarray(y) * stats["std"] + stats["mean"]]
        return {"route_id": route_id,
                "forecast": [{"hour_offset": i + 1, "ridership": round(r, 1)}
                             for i, r in enumerate(riders)],
                "variant": variant, "model_version": bundle.version}

    def leak_score(self, subject: str, rows: np.ndarray, domain: str = "h2") -> dict:
        """Anomaly-domain scoring. domain='h2' -> leak_autoencoder (default,
        unchanged); domain='ev_thermal' -> ev_thermal_autoencoder."""
        model = ANOMALY_DOMAIN_MODELS.get(domain)
        if model is None:
            raise ValueError(f"unknown anomaly domain {domain!r} "
                             f"(expected one of {sorted(ANOMALY_DOMAIN_MODELS)})")
        bundle, variant = self._pick(model, subject)
        self.monitors[model].observe(rows)
        xn = bundle.normalize(rows)
        with torch.no_grad():
            scores = bundle.net.anomaly_score(torch.tensor(xn)).tolist()
        threshold = float(bundle.schema.get("extra", {}).get("anomaly_threshold", 1.0))
        return {"domain": domain,
                "scores": [round(float(s), 5) for s in scores],
                "is_anomaly": [bool(s > threshold) for s in scores],
                "threshold": threshold, "max_score": round(float(max(scores)), 5),
                "variant": variant, "model_version": bundle.version}

    def fleet_propagate(self, node_features: np.ndarray,
                        adjacency: np.ndarray | None) -> dict:
        bundle, variant = self._pick("fleet_gcn", "fleet")
        self.monitors["fleet_gcn"].observe(node_features)
        if adjacency is None:
            adjacency = np.asarray(bundle.schema["extra"]["adjacency"], dtype=np.float32)
        xn = torch.tensor(bundle.normalize(node_features))
        adj_norm = normalize_adjacency(adjacency)
        with torch.no_grad():
            delay, energy = bundle.net(xn, adj_norm)
        names = bundle.schema.get("node_names", [f"node-{i}" for i in range(len(node_features))])
        return {"nodes": [{"node": names[i] if i < len(names) else f"node-{i}",
                           "delay_propagation_min": round(float(delay[i]), 3),
                           "h2_impact_kg": round(float(energy[i]), 3)}
                          for i in range(len(node_features))],
                "variant": variant, "model_version": bundle.version}

    def carbon_forecast(self, subject: str, rows: np.ndarray) -> dict:
        bundle, variant = self._pick("carbon_forecaster", subject)
        self.monitors["carbon_forecaster"].observe(rows)
        xn = torch.tensor(bundle.normalize(rows))
        with torch.no_grad():
            y = bundle.net(xn).tolist()
        t = bundle.schema.get("target", {"mean": 0.0, "std": 1.0})
        preds = [max(0.0, float(v) * t["std"] + t["mean"]) for v in y]
        return {"predictions": [round(p, 1) for p in preds],
                "variant": variant, "model_version": bundle.version}

    def drift_snapshot(self) -> dict:
        return {name: m.snapshot for name, m in self.monitors.items()}

    def recompute_drift(self) -> dict:
        return {name: m.compute() for name, m in self.monitors.items()}


class ModelUnavailable(Exception):
    def __init__(self, model: str):
        super().__init__(f"model {model} has no loaded artifact")
        self.model = model
