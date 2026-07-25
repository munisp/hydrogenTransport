"""Neo4j bridge: graceful skip + adjacency npz contract for the GNN."""

from __future__ import annotations

import os

import numpy as np

from training.export_graph import export_neo4j


def test_neo4j_skips_gracefully_without_uri(monkeypatch):
    monkeypatch.delenv("NEO4J_URI", raising=False)
    graph = {"node_names": np.array(["a", "b"]), "adjacency": np.eye(2)}
    assert export_neo4j(graph) is False


def test_synth_graph_npz_matches_gnn_contract(tiny_synth_dir):
    graph = np.load(os.path.join(tiny_synth_dir, "graph.npz"))
    adj = graph["adjacency"]
    x = graph["node_features"]
    assert adj.shape == (len(graph["node_names"]),) * 2
    assert (adj == adj.T).all()                      # undirected
    assert x.shape[0] == adj.shape[0] and x.shape[1] == 7
    assert graph["delay_target"].shape == (adj.shape[0],)
    assert graph["energy_target"].shape == (adj.shape[0],)
