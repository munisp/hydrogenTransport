"""Anomaly autoencoder over safety-domain sensor vectors.

Two domains share the same symmetric dense-AE architecture (reconstruction
error = anomaly score, trained on normal-operation vectors only):

* ``h2``        — LEAK_SENSOR_FEATURES: h2 ppm readings across the tank bay /
  fuel-cell bay / cabin / station dispenser zones plus pressure and flow-rate
  deltas (model name ``leak_autoencoder``, ~0.6k params).
* ``ev_thermal`` — EV_THERMAL_FEATURES: battery-pack telemetry (cell
  temperature / cell voltage / pack current / ambient) scored for
  thermal-runaway precursors (model name ``ev_thermal_autoencoder``,
  n_features=4 via the artifact's model_config).
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

#: ev_thermal anomaly domain: battery-pack telemetry vector. Mirrors the
#: leak_autoencoder feature_schema.json conventions (feature list + train
#: stats + drift baseline + extra.anomaly_threshold).
EV_THERMAL_FEATURES: list[str] = [
    "cell_temp_c",
    "cell_voltage_v",
    "pack_current_a",
    "ambient_c",
]

#: anomaly domain -> model name (serving + endpoint validation share this).
ANOMALY_DOMAIN_MODELS: dict[str, str] = {
    "h2": "leak_autoencoder",
    "ev_thermal": "ev_thermal_autoencoder",
}

#: anomaly domain -> feature list.
ANOMALY_DOMAIN_FEATURES: dict[str, list[str]] = {
    "h2": LEAK_SENSOR_FEATURES,
    "ev_thermal": EV_THERMAL_FEATURES,
}


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
