"""Anomaly autoencoder over H2 leak-sensor vectors.

Input vector (LEAK_SENSOR_FEATURES): h2 ppm readings across the tank bay /
fuel-cell bay / cabin / station dispenser zones plus pressure and flow-rate
deltas. Trained on normal-operation vectors only; reconstruction error at
inference is the leak anomaly score. Symmetric dense AE, ~2k params.
"""

from __future__ import annotations

import torch
from torch import nn

LEAK_SENSOR_FEATURES: list[str] = [
    "h2_ppm_tank_bay",
    "h2_ppm_fuelcell_bay",
    "h2_ppm_cabin",
    "h2_ppm_dispenser",
    "tank_pressure_bar",
    "pressure_drop_bar_per_min",
    "flow_rate_kg_per_min",
    "ambient_temp_c",
]


class LeakAutoencoder(nn.Module):
    def __init__(self, n_features: int = len(LEAK_SENSOR_FEATURES), bottleneck: int = 4):
        super().__init__()
        self.encoder = nn.Sequential(
            nn.Linear(n_features, 16), nn.ReLU(),
            nn.Linear(16, 8), nn.ReLU(),
            nn.Linear(8, bottleneck),
        )
        self.decoder = nn.Sequential(
            nn.Linear(bottleneck, 8), nn.ReLU(),
            nn.Linear(8, 16), nn.ReLU(),
            nn.Linear(16, n_features),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.decoder(self.encoder(x))

    def anomaly_score(self, x: torch.Tensor) -> torch.Tensor:
        """Per-row mean squared reconstruction error (higher = more anomalous)."""
        with torch.no_grad():
            recon = self.forward(x)
            return ((x - recon) ** 2).mean(dim=1)
