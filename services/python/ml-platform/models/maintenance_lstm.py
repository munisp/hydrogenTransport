"""LSTM over telemetry windows -> per-component failure risk + time-to-failure.

Input: (batch, T, F) windows of the ordered SEQ_FEATURES below (resampled to a
fixed step, z-normalised with the training feature stats). Outputs per
component in COMPONENTS: risk score in [0,1] (sigmoid head) and predicted
days-to-failure (softplus head). ~7k params -> CPU training in minutes.
"""

from __future__ import annotations

import torch
from torch import nn

#: Ordered per-timestep telemetry features (SPEC fleet.telemetry + ambient temp).
SEQ_FEATURES: list[str] = [
    "h2_level_pct",
    "fuel_cell_kw",
    "battery_soc_pct",
    "speed_kph",
    "ambient_temp_c",
]

#: Components scored per window (SPEC fleet.maintenance_predictions.component).
COMPONENTS: list[str] = ["fuel_cell", "compressor", "tank_valve", "battery"]


class MaintenanceLSTM(nn.Module):
    def __init__(self, n_features: int = len(SEQ_FEATURES), hidden: int = 32,
                 n_layers: int = 1, n_components: int = len(COMPONENTS)):
        super().__init__()
        self.n_components = n_components
        self.lstm = nn.LSTM(n_features, hidden, num_layers=n_layers, batch_first=True)
        self.risk_head = nn.Linear(hidden, n_components)      # logits -> sigmoid
        self.horizon_head = nn.Linear(hidden, n_components)   # -> softplus days

    def forward(self, x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
        """x: (B, T, F) -> (risk (B, C) in [0,1], days_to_failure (B, C) >= 0)."""
        out, _ = self.lstm(x)
        h = out[:, -1, :]
        risk = torch.sigmoid(self.risk_head(h))
        days = torch.nn.functional.softplus(self.horizon_head(h))
        return risk, days


def count_parameters(model: nn.Module) -> int:
    return sum(p.numel() for p in model.parameters() if p.requires_grad)
