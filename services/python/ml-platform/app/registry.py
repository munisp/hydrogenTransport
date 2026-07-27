"""Artifact IO + champion/challenger registry, shared by training and serving.

Layout (MODEL_ARTIFACTS_DIR, default ./artifacts):

    <model>/<version>/weights.pt          {"state_dict", "model_config"}
    <model>/<version>/metrics.json        {"model","version","source","metrics",...}
    <model>/<version>/feature_schema.json {"features","stats","baseline","extra"}
    registry.json                         {"champion": {model: version},
                                           "challenger": {model: version}}

Weights are plain ``torch.save`` state dicts (weights_only-safe). Versions are
free-form strings; training stamps ``v<utc timestamp>`` unless given.
"""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone

import torch

from models import (CarbonForecaster, DemandForecaster, FleetGCN,
                    LeakAutoencoder, MaintenanceLSTM)

MODEL_CLASSES = {
    "maintenance_lstm": MaintenanceLSTM,
    "demand_forecaster": DemandForecaster,
    "leak_autoencoder": LeakAutoencoder,
    # ev_thermal safety domain pack: same AE architecture, 4-feature battery
    # telemetry input (n_features carried in the artifact's model_config).
    "ev_thermal_autoencoder": LeakAutoencoder,
    "fleet_gcn": FleetGCN,
    "carbon_forecaster": CarbonForecaster,
}

MODEL_NAMES = list(MODEL_CLASSES)


def new_version() -> str:
    return "v" + datetime.now(timezone.utc).strftime("%Y%m%d.%H%M%S")


# ---------------------------------------------------------------- artifacts --
def artifact_dir(artifacts_dir: str, model: str, version: str) -> str:
    return os.path.join(artifacts_dir, model, version)


def save_artifact(artifacts_dir: str, model: str, version: str,
                  net: torch.nn.Module, model_config: dict, metrics: dict,
                  feature_schema: dict) -> str:
    out = artifact_dir(artifacts_dir, model, version)
    os.makedirs(out, exist_ok=True)
    torch.save({"state_dict": net.state_dict(), "model_config": model_config},
               os.path.join(out, "weights.pt"))
    metrics = {"model": model, "version": version,
               "trained_at": datetime.now(timezone.utc).isoformat(), **metrics}
    with open(os.path.join(out, "metrics.json"), "w") as f:
        json.dump(metrics, f, indent=2)
    with open(os.path.join(out, "feature_schema.json"), "w") as f:
        json.dump(feature_schema, f, indent=2)
    return out


def load_artifact(artifacts_dir: str, model: str, version: str,
                  map_location: str = "cpu"):
    """-> (net in eval mode, metrics dict, feature_schema dict)."""
    path = artifact_dir(artifacts_dir, model, version)
    blob = torch.load(os.path.join(path, "weights.pt"), map_location=map_location,
                      weights_only=True)
    net = MODEL_CLASSES[model](**blob["model_config"])
    net.load_state_dict(blob["state_dict"])
    net.eval()
    with open(os.path.join(path, "metrics.json")) as f:
        metrics = json.load(f)
    with open(os.path.join(path, "feature_schema.json")) as f:
        schema = json.load(f)
    return net, metrics, schema


def list_artifacts(artifacts_dir: str) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for model in MODEL_NAMES:
        d = os.path.join(artifacts_dir, model)
        if os.path.isdir(d):
            out[model] = sorted(v for v in os.listdir(d)
                                if os.path.isdir(os.path.join(d, v)))
    return out


# ---------------------------------------------------------------- registry --
def registry_path(artifacts_dir: str) -> str:
    return os.path.join(artifacts_dir, "registry.json")


def read_registry(artifacts_dir: str) -> dict:
    path = registry_path(artifacts_dir)
    if os.path.exists(path):
        with open(path) as f:
            return json.load(f)
    return {"champion": {}, "challenger": {}}


def write_registry(artifacts_dir: str, registry: dict) -> None:
    registry["updated_at"] = datetime.now(timezone.utc).isoformat()
    tmp = registry_path(artifacts_dir) + ".tmp"
    with open(tmp, "w") as f:
        json.dump(registry, f, indent=2)
    os.replace(tmp, registry_path(artifacts_dir))


def resolve_version(artifacts_dir: str, model: str, role: str = "champion") -> str | None:
    """Version for a role; falls back to the newest artifact dir on disk."""
    reg = read_registry(artifacts_dir)
    version = reg.get(role, {}).get(model) or (
        reg.get("champion", {}).get(model) if role == "challenger" else None)
    if version:
        return version
    versions = list_artifacts(artifacts_dir).get(model, [])
    return versions[-1] if versions else None
