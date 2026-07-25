"""Gradient-style dense net forecasting CO2 avoided (kg) for the next period.

Input features (CARBON_FEATURES) describe a reporting period (week/month) of
fleet operation; the target is kg CO2 avoided vs the diesel baseline. Plain
2-hidden-layer MLP, ~5k params.

NOTE: the net predicts the NORMALISED target (z-scored with the training
target stats stored in feature_schema.json) and therefore has no output
squashing — an earlier softplus head could not represent the zero-centred
target and plateaued at rel-MAE ~0.14 vs the ~0.02 noise floor. Serving
denormalises and clamps negatives to zero.
"""

from __future__ import annotations

import torch
from torch import nn

CARBON_FEATURES: list[str] = [
    "total_km",
    "h2_consumed_kg",
    "total_ridership",
    "active_buses",
    "avg_temp_c",
    "weekday_frac",
    "period_days",
    "prev_kg_co2_avoided",
]


class CarbonForecaster(nn.Module):
    def __init__(self, n_features: int = len(CARBON_FEATURES), hidden: int = 64):
        super().__init__()
        self.net = nn.Sequential(
            nn.Linear(n_features, hidden), nn.ReLU(),
            nn.Linear(hidden, hidden), nn.ReLU(),
            nn.Linear(hidden, 1),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        """x: (B, F) -> (B,) predicted NORMALISED kg CO2 avoided."""
        return self.net(x).squeeze(-1)
