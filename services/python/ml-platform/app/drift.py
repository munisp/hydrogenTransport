"""Feature-drift monitor: PSI + approximate KS vs the training baseline.

Each model artifact ships a baseline in feature_schema.json ("baseline" key):
per feature, the quantile bin edges and bin proportions of the TRAINING split.
At serving time every incoming feature row is appended to a ring buffer; a
background task periodically recomputes, per feature:

* PSI  = sum((base% - curr%) * ln(base% / curr%))  over the baseline bins
         (PSI > 0.2 is the conventional 'significant shift' warning level)
* KS   — two-sample KS statistic approximated on the baseline-bin CDFs
         (exact KS needs raw baseline samples which we intentionally do not
         ship in the artifact; the binned approximation is monotone with it)

Results are exposed at GET /v1/ml/drift, surfaced as the Prometheus gauge
`ml_feature_drift_psi{model,feature}` and logged as warnings when above the
configured threshold. This is a detection aid, not an automatic retraining
trigger — promotion decisions stay with training/continuous.py.
"""

from __future__ import annotations

import logging
import math
import threading
from collections import deque

import numpy as np

log = logging.getLogger("ml-platform.drift")

_EPS = 1e-6


def psi(base_prop: np.ndarray, curr_prop: np.ndarray) -> float:
    b = np.clip(base_prop, _EPS, None)
    c = np.clip(curr_prop, _EPS, None)
    return float(np.sum((b - c) * np.log(b / c)))


class FeatureDriftMonitor:
    """Ring-buffer drift monitor for one model's input features."""

    def __init__(self, model: str, schema: dict, window: int = 512,
                 psi_warn: float = 0.2):
        self.model = model
        self.features: list[str] = schema["features"]
        self.baseline: dict = schema.get("baseline", {})
        self.psi_warn = psi_warn
        self._buf: deque = deque(maxlen=window)
        self._lock = threading.Lock()
        self._snapshot: dict = {"model": model, "status": "no-data", "features": {}}

    def observe(self, rows: np.ndarray) -> None:
        """rows: (..., F) raw (unnormalised) feature values."""
        flat = np.asarray(rows, dtype=np.float64).reshape(-1, len(self.features))
        with self._lock:
            self._buf.extend(flat.tolist())

    def compute(self) -> dict:
        with self._lock:
            data = np.asarray(self._buf, dtype=np.float64)
        n = len(data)
        if n < 32:
            self._snapshot = {"model": self.model, "status": "insufficient-data",
                              "n_observed": n, "features": {}}
            return self._snapshot
        report: dict[str, dict] = {}
        worst = 0.0
        for i, f in enumerate(self.features):
            base = self.baseline.get(f)
            if not base:
                continue
            inner = np.asarray(base["bin_edges"], dtype=np.float64)
            edges = np.concatenate([[-np.inf], inner, [np.inf]])
            base_prop = np.asarray(base["proportions"], dtype=np.float64)
            counts, _ = np.histogram(data[:, i], bins=edges)
            curr_prop = counts / max(counts.sum(), 1)
            p = psi(base_prop, curr_prop)
            ks = float(np.max(np.abs(np.cumsum(base_prop) - np.cumsum(curr_prop))))
            worst = max(worst, p)
            report[f] = {"psi": round(p, 4), "ks": round(ks, 4),
                         "drifted": bool(p > self.psi_warn)}
        status = "drift" if worst > self.psi_warn else "ok"
        self._snapshot = {"model": self.model, "status": status, "n_observed": n,
                          "worst_psi": round(worst, 4), "features": report}
        if status == "drift":
            offenders = [f for f, r in report.items() if r["drifted"]]
            log.warning("feature drift detected model=%s worst_psi=%.3f features=%s",
                        self.model, worst, offenders)
        return self._snapshot

    @property
    def snapshot(self) -> dict:
        return self._snapshot
