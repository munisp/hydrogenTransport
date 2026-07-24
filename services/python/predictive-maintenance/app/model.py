"""Risk scoring: sklearn model when models/model.joblib exists, deterministic
rule-based fallback otherwise (SPEC.md §3.5 — service must run without a
trained model)."""

from __future__ import annotations

import os
from dataclasses import dataclass

import joblib

from .features import FEATURES, feature_vector

COMPONENTS: list[str] = ["fuel_cell", "compressor", "tank_valve", "battery"]


def _clip(x: float) -> float:
    return max(0.0, min(1.0, x))


@dataclass(frozen=True)
class ComponentRisk:
    component: str
    risk_score: float
    horizon_days: int


class RuleModel:
    """Deterministic heuristic model. Weights encode domain priors for H2 buses:
    fuel-cell load volatility and sustained high load drive membrane wear;
    compressor wear tracks fuel-cell demand; tank-valve wear tracks refuel
    cycles and run-dry events; battery risk tracks deep discharge and high
    SoC swing."""

    version = "rules-v1"

    def predict_all(self, features: dict[str, float]) -> list[ComponentRisk]:
        f = features
        hours = max(f.get("n_samples", 0.0), 1.0)  # ~1 sample/min => proxy for window hours*60
        refuels_per_day = f.get("refuel_events", 0.0) / max(hours / 60.0 / 24.0, 1.0 / 24.0)

        scores = {
            "fuel_cell": _clip(
                0.05
                + (0.35 if f.get("std_fuel_cell_kw", 0.0) > 25.0 else 0.10 * f.get("std_fuel_cell_kw", 0.0) / 25.0)
                + (0.25 if f.get("max_fuel_cell_kw", 0.0) > 110.0 else 0.0)
                + (0.15 if f.get("avg_fuel_cell_kw", 0.0) > 70.0 else 0.0)
                + (0.20 if f.get("min_h2_level_pct", 100.0) < 8.0 else 0.0)
            ),
            "compressor": _clip(
                0.05
                + 0.30 * min(f.get("avg_fuel_cell_kw", 0.0) / 90.0, 1.0)
                + (0.25 if f.get("max_fuel_cell_kw", 0.0) > 110.0 else 0.0)
                + 0.20 * min(f.get("avg_speed_kph", 0.0) / 55.0, 1.0)
                + 0.10 * min(f.get("km_driven", 0.0) / 400.0, 1.0)
            ),
            "tank_valve": _clip(
                0.05
                + 0.25 * min(refuels_per_day / 4.0, 1.0)
                + (0.35 if f.get("min_h2_level_pct", 100.0) < 5.0 else 0.0)
                + 0.15 * min(f.get("km_driven", 0.0) / 400.0, 1.0)
            ),
            "battery": _clip(
                0.05
                + (0.40 if f.get("min_battery_soc_pct", 100.0) < 10.0 else 0.0)
                + (0.20 if f.get("std_battery_soc_pct", 0.0) > 25.0 else 0.0)
                + (0.15 if f.get("avg_battery_soc_pct", 0.0) > 90.0 else 0.0)
                + 0.10 * min(f.get("max_speed_kph", 0.0) / 90.0, 1.0)
            ),
        }
        return [
            ComponentRisk(component=c, risk_score=round(scores[c], 4), horizon_days=_horizon(scores[c]))
            for c in COMPONENTS
        ]


class SklearnModel:
    """Wraps the joblib artifact produced by train.py:
    {"models": {component: classifier}, "features": [...], "version": str}."""

    def __init__(self, artifact: dict):
        self._models: dict = artifact["models"]
        self._features: list[str] = artifact["features"]
        self.version: str = str(artifact.get("version", "sklearn-unknown"))

    def predict_all(self, features: dict[str, float]) -> list[ComponentRisk]:
        if self._features != FEATURES:
            # Defensive: never silently score on mismatched semantics.
            raise ValueError(
                f"model artifact features {self._features} != runtime features {FEATURES}"
            )
        x = [feature_vector(features)]
        out: list[ComponentRisk] = []
        for comp in COMPONENTS:
            clf = self._models.get(comp)
            if clf is None:
                continue
            proba = float(clf.predict_proba(x)[0][1])
            out.append(ComponentRisk(component=comp, risk_score=round(proba, 4), horizon_days=_horizon(proba)))
        # Any component missing from the artifact falls back to rules.
        have = {r.component for r in out}
        if have != set(COMPONENTS):
            rules = RuleModel().predict_all(features)
            out.extend(r for r in rules if r.component not in have)
        out.sort(key=lambda r: COMPONENTS.index(r.component))
        return out


def _horizon(risk: float) -> int:
    """Days until predicted failure: high risk => imminent, floor 3 days."""
    return max(3, int(round(60 * (1.0 - risk))))


def load_model(path: str) -> RuleModel | SklearnModel:
    """Load the trained artifact if present and valid; else rule fallback."""
    if os.path.exists(path):
        try:
            artifact = joblib.load(path)
            model = SklearnModel(artifact)
            return model
        except Exception:
            # Corrupt/incompatible artifact must not take the service down.
            import logging

            logging.getLogger("predictive-maintenance").exception(
                "failed to load model artifact at %s; using rule fallback", path
            )
    return RuleModel()
