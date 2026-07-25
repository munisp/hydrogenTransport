"""Trained-LSTM scorer: loads the ml-platform maintenance_lstm artifact.

Preference order in app.model.load_model():
    1. LSTMScorer  (MODEL_ARTIFACTS_DIR/maintenance_lstm/<champion>/)
    2. SklearnModel (legacy models/model.joblib)
    3. RuleModel    (deterministic fallback — SPEC §3.5)

The LSTM consumes a RESAMPLED TELEMETRY WINDOW (T x 5 sequence, see
app.features.fetch_sequence) rather than the aggregate feature dict used by
the sklearn/rule models, so callers must attach ``features["_sequence"]``
when ``LSTMScorer.needs_sequence`` is True. torch is an optional import: if
it (or the artifact) is unavailable, loading returns None and the caller
falls through the chain — the service always runs.
"""

from __future__ import annotations

import json
import logging
import os

import numpy as np

from .model import COMPONENTS, ComponentRisk

log = logging.getLogger("predictive-maintenance.lstm")

try:  # torch is heavy; keep the service importable/runnable without it
    import torch
except ImportError:  # pragma: no cover - exercised in minimal containers
    torch = None  # type: ignore[assignment]

SEQ_FEATURES = ["h2_level_pct", "fuel_cell_kw", "battery_soc_pct", "speed_kph",
                "ambient_temp_c"]


def _build_net(n_features: int, hidden: int, n_layers: int, n_components: int):
    """Mirror of ml-platform models/maintenance_lstm.py (kept dependency-free:
    the artifact carries only a state dict, so the architecture is recreated
    here rather than importing across services)."""
    import torch.nn as nn

    class MaintenanceLSTM(nn.Module):
        def __init__(self):
            super().__init__()
            self.lstm = nn.LSTM(n_features, hidden, num_layers=n_layers,
                                batch_first=True)
            self.risk_head = nn.Linear(hidden, n_components)
            self.horizon_head = nn.Linear(hidden, n_components)

        def forward(self, x):
            out, _ = self.lstm(x)
            h = out[:, -1, :]
            return (torch.sigmoid(self.risk_head(h)),
                    torch.nn.functional.softplus(self.horizon_head(h)))

    return MaintenanceLSTM()


class LSTMScorer:
    """predict_all(features) with the same contract as RuleModel/SklearnModel
    plus features['_sequence']: (T, 5) raw telemetry window."""

    needs_sequence = True

    def __init__(self, net, schema: dict, version: str):
        self._net = net
        self._schema = schema
        self.version = version

    def predict_all(self, features: dict) -> list[ComponentRisk]:
        seq = features.get("_sequence")
        if seq is None:
            raise ValueError("LSTMScorer requires features['_sequence'] "
                             "(see app.features.fetch_sequence)")
        x = np.asarray(seq, dtype=np.float32)
        stats = self._schema["stats"]
        for i, f in enumerate(self._schema["features"]):
            x[:, i] = (x[:, i] - stats[f]["mean"]) / stats[f]["std"]
        with torch.no_grad():
            risk, days = self._net(torch.tensor(x[None, ...]))
        risk, days = risk[0].tolist(), days[0].tolist()
        return [
            ComponentRisk(component=COMPONENTS[i],
                          risk_score=round(float(risk[i]), 4),
                          horizon_days=max(1, int(round(float(days[i])))))
            for i in range(len(COMPONENTS))
        ]


def load_lstm_scorer(artifacts_dir: str) -> LSTMScorer | None:
    """Load champion maintenance_lstm artifact; None when unavailable."""
    if torch is None:
        log.info("torch not installed — LSTM scorer unavailable")
        return None
    base = os.path.join(artifacts_dir, "maintenance_lstm")
    if not os.path.isdir(base):
        return None
    registry_path = os.path.join(artifacts_dir, "registry.json")
    version = None
    if os.path.exists(registry_path):
        try:
            with open(registry_path) as f:
                version = json.load(f).get("champion", {}).get("maintenance_lstm")
        except Exception:
            log.warning("unreadable registry.json at %s", registry_path)
    versions = sorted(os.listdir(base))
    if version not in versions:
        version = versions[-1] if versions else None
    if not version:
        return None
    path = os.path.join(base, version)
    try:
        blob = torch.load(os.path.join(path, "weights.pt"), map_location="cpu",
                          weights_only=True)
        cfg = blob["model_config"]
        net = _build_net(cfg.get("n_features", len(SEQ_FEATURES)),
                         cfg.get("hidden", 32), cfg.get("n_layers", 1),
                         cfg.get("n_components", len(COMPONENTS)))
        net.load_state_dict(blob["state_dict"])
        net.eval()
        with open(os.path.join(path, "feature_schema.json")) as f:
            schema = json.load(f)
        scorer = LSTMScorer(net, schema, f"lstm-{version}")
        log.info("loaded maintenance LSTM artifact %s", path)
        return scorer
    except Exception:
        log.exception("failed to load LSTM artifact at %s", path)
        return None
