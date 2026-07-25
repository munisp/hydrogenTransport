"""Seq model forecasting next-24h ridership per route.

Input: (batch, T, F) hourly history with F = [ridership, hour_sin, hour_cos,
dow_sin, dow_cos, is_weekend, temp_c, precip_mm]. Output: (batch, 24) ridership
for the next 24 hours. GRU encoder + linear decode, ~20k params.
"""

from __future__ import annotations

import torch
from torch import nn

DEMAND_FEATURES: list[str] = [
    "ridership",
    "hour_sin",
    "hour_cos",
    "dow_sin",
    "dow_cos",
    "is_weekend",
    "temp_c",
    "precip_mm",
]

HORIZON_HOURS = 24


class DemandForecaster(nn.Module):
    def __init__(self, n_features: int = len(DEMAND_FEATURES), hidden: int = 64,
                 horizon: int = HORIZON_HOURS):
        super().__init__()
        self.gru = nn.GRU(n_features, hidden, num_layers=1, batch_first=True)
        self.head = nn.Sequential(
            nn.Linear(hidden, hidden),
            nn.ReLU(),
            nn.Linear(hidden, horizon),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        """x: (B, T, F) -> (B, horizon) non-negative ridership forecast."""
        out, _ = self.gru(x)
        return torch.nn.functional.softplus(self.head(out[:, -1, :]))
