"""Export the route/station/depot graph from Postgres to (a) Neo4j and (b) the
GNN adjacency npz.

Graph construction (no routes table exists in the schema yet, SPEC §3.4):
  * station nodes  from infra.stations (features: status, available/capacity)
  * depot nodes    the two depot HRS flagged in the seed conventions
  * terminus nodes K-means clusters (k=12) over fleet.vehicles geom — a proxy
                   for route termini until a routes table lands
  * edges          haversine nearest-neighbour: terminus -> nearest station +
                   depot, station -> nearest depot (undirected)
  * node features  GRAPH_NODE_FEATURES order; delay/queue default to 0 until
                   the twin/dispatch services publish them

Neo4j: when NEO4J_URI is set AND the `neo4j` bolt driver is installed, nodes
and edges are MERGEd (idempotent). Otherwise the step is skipped gracefully —
the npz export always runs, so the GNN never depends on Neo4j.

Usage: python -m training.export_graph --out data/synth_out/graph.npz
Env:   DATABASE_URL, NEO4J_URI (e.g. bolt://neo4j:7687), NEO4J_USER, NEO4J_PASSWORD
"""

from __future__ import annotations

import argparse
import logging
import os

import numpy as np

from models import GRAPH_NODE_FEATURES

log = logging.getLogger("ml-platform.export_graph")

K_TERMINI = 12


def _haversine_km(lon1, lat1, lon2, lat2):
    r = 6371.0
    p1, p2 = np.radians(lat1), np.radians(lat2)
    dp = np.radians(lat2 - lat1)
    dl = np.radians(lon2 - lon1)
    a = np.sin(dp / 2) ** 2 + np.cos(p1) * np.cos(p2) * np.sin(dl / 2) ** 2
    return 2 * r * np.arcsin(np.sqrt(a))


def build_graph_from_postgres(database_url: str) -> dict[str, np.ndarray]:
    import psycopg
    with psycopg.connect(database_url) as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT fleet_no, ST_X(geom), ST_Y(geom), status FROM fleet.vehicles")
            vehicles = cur.fetchall()
            cur.execute("SELECT name, ST_X(geom), ST_Y(geom), status, "
                        "capacity_kg, available_kg FROM infra.stations")
            stations = cur.fetchall()
    if not stations or not vehicles:
        raise RuntimeError("fleet.vehicles / infra.stations empty — seed the DB first")

    st_names = [s[0] for s in stations]
    st_xy = np.array([[s[1], s[2]] for s in stations])
    st_feat = np.array([
        [1.0 if s[3] == "online" else 0.0, float(s[5] or 0) / max(float(s[4] or 1), 1.0)]
        for s in stations])

    veh_xy = np.array([[v[1], v[2]] for v in vehicles])
    # K-means (numpy-only, deterministic seed) for route-terminus proxies.
    rng = np.random.default_rng(42)
    cent = veh_xy[rng.choice(len(veh_xy), K_TERMINI, replace=False)].copy()
    for _ in range(30):
        d = ((veh_xy[:, None, :] - cent[None, :, :]) ** 2).sum(-1)
        assign = d.argmin(1)
        for k in range(K_TERMINI):
            if (assign == k).any():
                cent[k] = veh_xy[assign == k].mean(0)
    counts = np.bincount(assign, minlength=K_TERMINI).astype(np.float32)

    dep_idx = [i for i, n in enumerate(st_names) if "Depot" in n] or [0]
    dep_names = [st_names[i] for i in dep_idx]
    dep_xy = st_xy[dep_idx]

    names = st_names + dep_names + [f"terminus-{k:02d}" for k in range(K_TERMINI)]
    n_st, n_dep, n_rt = len(st_names), len(dep_names), K_TERMINI
    n = len(names)
    adj = np.zeros((n, n), dtype=np.float32)

    def dist(a, b):
        return _haversine_km(a[0], a[1], b[0], b[1])

    for i in range(n_st):                       # station -> nearest depot
        j = int(np.argmin([dist(st_xy[i], d) for d in dep_xy]))
        adj[i, n_st + j] = adj[n_st + j, i] = 1.0
    for k in range(n_rt):                       # terminus -> nearest station + depot
        i = int(np.argmin([dist(cent[k], s) for s in st_xy]))
        j = int(np.argmin([dist(cent[k], d) for d in dep_xy]))
        node = n_st + n_dep + k
        adj[node, i] = adj[i, node] = 1.0
        adj[node, n_st + j] = adj[n_st + dep, node] = 1.0

    node_type = np.zeros((n, 3), dtype=np.float32)
    node_type[:n_st, 0] = 1
    node_type[n_st:n_st + n_dep, 1] = 1
    node_type[n_st + n_dep:, 2] = 1
    delay = np.zeros(n, dtype=np.float32)       # twin/dispatch feed pending
    queue = np.zeros(n, dtype=np.float32)
    h2_norm = np.zeros(n, dtype=np.float32)
    h2_norm[:n_st] = st_feat[:, 1]
    h2_norm[n_st:n_st + n_dep] = h2_norm[np.array(dep_idx)]
    throughput = np.concatenate([
        np.full(n_st, 3.0), np.full(n_dep, 6.0), counts / max(counts.max(), 1.0) * 4.0,
    ]).astype(np.float32)
    x = np.column_stack([node_type, delay, queue, h2_norm, throughput]).astype(np.float32)
    return {"adjacency": adj, "node_names": np.array(names), "node_features": x,
            "features_order": np.array(GRAPH_NODE_FEATURES)}


def export_neo4j(graph: dict[str, np.ndarray]) -> bool:
    """MERGE nodes/edges into Neo4j. Returns True when exported."""
    uri = os.environ.get("NEO4J_URI", "")
    if not uri:
        log.info("NEO4J_URI unset — skipping Neo4j export (npz still written)")
        return False
    try:
        from neo4j import GraphDatabase
    except ImportError:
        log.warning("neo4j driver not installed — skipping Neo4j export")
        return False
    names = [str(x) for x in graph["node_names"]]
    adj = graph["adjacency"]
    auth = (os.environ.get("NEO4J_USER", "neo4j"),
            os.environ.get("NEO4J_PASSWORD", "password"))
    with GraphDatabase.driver(uri, auth=auth) as driver:
        with driver.session() as s:
            for i, name in enumerate(names):
                s.run("MERGE (n:FleetNode {name: $name}) SET n.idx = $idx",
                      name=name, idx=i)
            for i in range(len(names)):
                for j in range(i + 1, len(names)):
                    if adj[i, j] > 0:
                        s.run(
                            "MATCH (a:FleetNode {name: $a}), (b:FleetNode {name: $b}) "
                            "MERGE (a)-[:CONNECTS]-(b)", a=names[i], b=names[j])
    log.info("exported %d nodes to Neo4j at %s", len(names), uri)
    return True


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--database-url", default=os.environ.get("DATABASE_URL", ""))
    ap.add_argument("--out", default="data/synth_out/graph.npz")
    args = ap.parse_args()
    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    if not args.database_url:
        raise SystemExit("--database-url or DATABASE_URL required")
    graph = build_graph_from_postgres(args.database_url)
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    np.savez(args.out, **graph)
    neo4j_ok = export_neo4j(graph)
    print({"npz": args.out, "neo4j": neo4j_ok, "nodes": len(graph["node_names"])})


if __name__ == "__main__":
    main()
