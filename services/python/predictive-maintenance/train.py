"""Train the predictive-maintenance model -> models/model.joblib.

Usage:
    python train.py [--samples 5000] [--out models/model.joblib]

Labels for real fleet telemetry do not exist yet, so training uses a
deterministic synthetic fleet: feature vectors sampled from plausible operating
ranges, labels drawn from a noisy version of the same domain priors encoded in
the rule fallback (app/model.py). The runtime artifact contract is
{"models": {component: clf}, "features": FEATURES, "version": str, ...} and is
validated at load time against app.features.FEATURES.
"""

from __future__ import annotations

import argparse
import json
import os
from datetime import datetime, timezone

import joblib
import numpy as np
from sklearn.ensemble import RandomForestClassifier
from sklearn.metrics import roc_auc_score
from sklearn.model_selection import train_test_split

from app.features import FEATURES
from app.model import COMPONENTS

ARTIFACT_VERSION = "sklearn-v1"


def sample_fleet(n: int, rng: np.random.Generator) -> np.ndarray:
    """Synthetic 24h aggregate feature vectors in plausible H2-bus ranges."""
    n_samples = rng.uniform(300, 1440, n)  # ~1 row/min
    return np.column_stack(
        [
            n_samples,                                            # n_samples
            rng.uniform(5, 45, n),                                # avg_speed_kph
            rng.uniform(20, 95, n),                               # max_speed_kph
            rng.uniform(10, 95, n),                               # avg_fuel_cell_kw
            rng.uniform(40, 140, n),                              # max_fuel_cell_kw
            rng.uniform(2, 40, n),                                # std_fuel_cell_kw
            rng.uniform(10, 80, n),                               # avg_h2_level_pct
            rng.uniform(0, 40, n),                                # min_h2_level_pct
            rng.uniform(0, 8, n),                                 # refuel_events
            rng.uniform(30, 95, n),                               # avg_battery_soc_pct
            rng.uniform(0, 60, n),                                # min_battery_soc_pct
            rng.uniform(2, 35, n),                                # std_battery_soc_pct
            rng.uniform(20, 500, n),                              # km_driven
        ]
    )


def label_component(X: np.ndarray, component: str, rng: np.random.Generator) -> np.ndarray:
    """Latent failure risk per component (domain priors + label noise)."""
    idx = {name: i for i, name in enumerate(FEATURES)}
    g = lambda name: X[:, idx[name]]
    if component == "fuel_cell":
        latent = 0.35 * (g("std_fuel_cell_kw") / 40) + 0.30 * (g("max_fuel_cell_kw") / 140) \
            + 0.20 * (1 - g("min_h2_level_pct") / 40) + 0.15 * (g("avg_fuel_cell_kw") / 95)
    elif component == "compressor":
        latent = 0.40 * (g("avg_fuel_cell_kw") / 95) + 0.25 * (g("max_fuel_cell_kw") / 140) \
            + 0.20 * (g("avg_speed_kph") / 45) + 0.15 * (g("km_driven") / 500)
    elif component == "tank_valve":
        latent = 0.45 * (g("refuel_events") / 8) + 0.35 * (1 - g("min_h2_level_pct") / 40) \
            + 0.20 * (g("km_driven") / 500)
    else:  # battery
        latent = 0.45 * (1 - g("min_battery_soc_pct") / 60) + 0.30 * (g("std_battery_soc_pct") / 35) \
            + 0.15 * (g("avg_battery_soc_pct") / 95) + 0.10 * (g("max_speed_kph") / 95)
    # Failure-within-30d label: latent risk above threshold, with observation noise.
    return (latent + rng.normal(0, 0.10, len(X)) > 0.45).astype(int)


def train(samples: int, out: str, seed: int = 42) -> dict:
    rng = np.random.default_rng(seed)
    X = sample_fleet(samples, rng)

    models: dict[str, RandomForestClassifier] = {}
    metrics: dict[str, float] = {}
    for comp in COMPONENTS:
        y = label_component(X, comp, rng)
        X_train, X_test, y_train, y_test = train_test_split(
            X, y, test_size=0.2, random_state=seed
        )
        clf = RandomForestClassifier(
            n_estimators=200, max_depth=12, min_samples_leaf=5,
            class_weight="balanced_subsample", random_state=seed, n_jobs=-1,
        )
        clf.fit(X_train, y_train)
        proba = clf.predict_proba(X_test)[:, 1]
        metrics[comp] = round(float(roc_auc_score(y_test, proba)), 4)
        models[comp] = clf

    artifact = {
        "models": models,
        "features": FEATURES,
        "version": ARTIFACT_VERSION,
        "trained_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "seed": seed,
        "samples": samples,
        "metrics": {"roc_auc_holdout": metrics},
    }
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    joblib.dump(artifact, out)
    return artifact


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--samples", type=int, default=5000)
    parser.add_argument("--out", default="models/model.joblib")
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    artifact = train(args.samples, args.out, args.seed)
    print(f"wrote {args.out} (version={artifact['version']}, samples={artifact['samples']})")
    print(json.dumps(artifact["metrics"], indent=2))


if __name__ == "__main__":
    main()
