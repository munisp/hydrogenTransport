"""Pure-PyTorch GCN over the route/station/depot graph (no torch_geometric).

Adjacency is normalised manually: A_hat = D^-1/2 (A + I) D^-1/2, and each GCN
layer computes A_hat @ X @ W. Node features (GRAPH_NODE_FEATURES) describe
current operational state; heads predict per-node delay propagation (minutes)
and energy (H2 kg) impact. ~1k params.
"""

from __future__ import annotations

import numpy as np
import torch
from torch import nn

GRAPH_NODE_FEATURES: list[str] = [
    "node_type_station",
    "node_type_depot",
    "node_type_route_terminus",
    "current_delay_min",
    "queue_len",
    "available_h2_kg_norm",
    "throughput_buses_per_h",
]


def normalize_adjacency(adj: np.ndarray) -> torch.Tensor:
    """Symmetric normalisation with self loops: D^-1/2 (A+I) D^-1/2."""
    a = np.asarray(adj, dtype=np.float64)
    a = a + np.eye(a.shape[0])
    deg = a.sum(axis=1)
    deg_inv_sqrt = np.where(deg > 0, deg ** -0.5, 0.0)
    d = np.diag(deg_inv_sqrt)
    return torch.tensor(d @ a @ d, dtype=torch.float32)


class GCNLayer(nn.Module):
    def __init__(self, in_dim: int, out_dim: int):
        super().__init__()
        self.lin = nn.Linear(in_dim, out_dim)

    def forward(self, x: torch.Tensor, adj_norm: torch.Tensor) -> torch.Tensor:
        return adj_norm @ self.lin(x)


class FleetGCN(nn.Module):
    """Two-layer GCN -> per-node (delay_propagation_min, h2_impact_kg)."""

    def __init__(self, n_features: int = len(GRAPH_NODE_FEATURES), hidden: int = 16):
        super().__init__()
        self.gc1 = GCNLayer(n_features, hidden)
        self.gc2 = GCNLayer(hidden, hidden)
        self.delay_head = nn.Linear(hidden, 1)
        self.energy_head = nn.Linear(hidden, 1)

    def forward(self, x: torch.Tensor, adj_norm: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
        """x: (N, F), adj_norm: (N, N) -> (delay (N,), h2_impact (N,))."""
        h = torch.relu(self.gc1(x, adj_norm))
        h = torch.relu(self.gc2(h, adj_norm))
        delay = torch.nn.functional.softplus(self.delay_head(h)).squeeze(-1)
        energy = self.energy_head(h).squeeze(-1)
        return delay, energy
