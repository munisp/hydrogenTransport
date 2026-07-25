# ml-platform (Python, port 8095)

Real AI/ML/DL/GNN stack for H2Fleet: five pure-PyTorch models (all ≤ 200k
params, CPU-first), a synthetic-data bootstrap, a full training pipeline, an
inference server with champion/challenger A/B and drift monitoring, continuous
training with promote-only-if-better, and a Neo4j graph bridge.

> **Honest status:** the shipped artifacts are trained on **synthetic
> bootstrap data** (`data/synth.py`) because the platform has no real failure
> labels yet. Every metric in `artifacts/*/metrics.json` is measured on that
> synthetic distribution and clearly labelled (`source: synth`). Retrain with
> `--source postgres` / `--source iceberg` as real telemetry accumulates.

## Models (`models/`)

| model | arch | input | output | params |
|---|---|---|---|---|
| `maintenance_lstm` | LSTM + risk/days heads | (T,5) telemetry window: h2_level, fuel_cell_kw, battery_soc, speed, ambient temp | per component (fuel_cell, compressor, tank_valve, battery): risk 0..1 + days-to-failure | 5,256 |
| `demand_forecaster` | GRU + linear decode | (72h,8) ridership history + calendar/weather | next-24h ridership per route | 19,928 |
| `leak_autoencoder` | dense AE (8→16→8→4) | 8-dim H2 sensor vector | reconstruction-error anomaly score | 636 |
| `fleet_gcn` | 2-layer GCN, manual D^-1/2(A+I)D^-1/2 | route/station/depot graph, 7 node features | per-node delay (min) + H2 impact (kg) | 434 |
| `carbon_forecaster` | dense MLP | 8 period features | kg CO2 avoided next period | 4,801 |

## Layout

```
models/       the five nn.Modules + feature lists + adjacency normalisation
data/synth.py synthetic generator (seeded from infra/sql/002_seed.sql schema)
training/
  train.py       training CLI (--source synth|postgres|iceberg, --finetune-from,
                 --ray, MLflow when MLFLOW_TRACKING_URI set, --register)
  continuous.py  fine-tune -> evaluate vs champion -> promote if better
  export_graph.py Postgres graph -> Neo4j (graceful skip) + GNN adjacency npz
  datasets.py    source loaders + window builders + feature stats
app/          FastAPI inference server (main, serving, registry, drift, ab, schemas)
artifacts/    <model>/<version>/{weights.pt, metrics.json, feature_schema.json}
              + registry.json (champion/challenger)
tests/        pytest suite (35 tests)
```

## Quickstart

```bash
pip install torch==2.5.1 --index-url https://download.pytorch.org/whl/cpu
pip install -r requirements.txt
pip install ../shared            # h2fleet_auth

# train everything on synthetic bootstrap data (CPU, ~1 min total)
python -m training.train --model all --source synth --epochs 5 --register

# serve
uvicorn app.main:app --port 8095
curl localhost:8095/v1/ml/models
```

Tests: `python -m pytest tests -q` (from this directory; needs `../shared` on
the path — `tests/conftest.py` wires both).

## API (all scoring routes require a Keycloak RS256 Bearer token)

| endpoint | body | response |
|---|---|---|
| `POST /v1/ml/maintenance/score` | `{bus_id, window: [[5] x T]}` | per-component risk + days, variant, version |
| `POST /v1/ml/demand/forecast` | `{route_id, history: [{ts,ridership,temp_c?,precip_mm?} x >=72]}` | 24 hourly ridership points |
| `POST /v1/ml/leak/score` | `{subject, readings: [[8] x N]}` | anomaly scores, flags, threshold |
| `POST /v1/ml/fleet/propagate` | `{node_features: [[7] x N], adjacency?}` | per-node delay + H2 impact |
| `POST /v1/ml/carbon/forecast` | `{subject, periods: [[8] x N]}` | kg CO2 avoided per period |
| `GET /v1/ml/models` | — | loaded versions, metrics, params, registry |
| `GET /v1/ml/drift` | — | PSI/KS per model/feature vs training baseline |
| `GET /healthz`, `GET /metrics` | — | liveness, Prometheus |

## A/B (champion/challenger)

`artifacts/registry.json` holds `champion` and optional `challenger` version
per model. When a challenger exists, `AB_SPLIT` (default `0.1`) of traffic is
routed to it — assignment is a **stable hash** of the subject key (bus/route),
so a subject always sees the same variant. Every response carries `variant`
and `model_version`, and predictions are logged variant-tagged for offline
comparison.

## Drift monitoring

Each artifact ships quantile-baseline histograms of its training features
(`feature_schema.json -> baseline`). A background loop (`DRIFT_INTERVAL_S`,
default 60s) computes PSI + binned KS of incoming request features against
those baselines, keeps a `DRIFT_WINDOW` (512) ring buffer, warns in logs when
`PSI > DRIFT_PSI_WARN` (0.2), exposes `GET /v1/ml/drift` and the
`ml_feature_drift_psi{model,feature}` Prometheus gauge.

## Training

```bash
# from scratch on synthetic data
python -m training.train --model maintenance_lstm --source synth --epochs 8 --register

# on the live platform DB (labels from infra.incidents)
python -m training.train --model maintenance_lstm --source postgres \
    --database-url postgresql://... --finetune-from artifacts/maintenance_lstm/v1.0.0

# on lakehouse parquet (Spark/Iceberg export dir)
python -m training.train --model all --source iceberg --lakehouse-dir /mnt/lakehouse

# distributed runtime (automatic local fallback when ray isn't installed)
python -m training.train --model all --ray
```

- splits: group-aware (bus/route) for maintenance/leak/carbon, chronological
  for demand, node-holdout for the GCN — no leakage into test metrics
- checkpointing: best-on-validation weights are what get saved
- `--finetune-from <artifact-dir>` for continuous training
- MLflow: set `MLFLOW_TRACKING_URI` (+ `pip install mlflow`); params, metrics
  and the artifact bundle are logged; a no-op otherwise

## Continuous training

`training/continuous.py` pulls the newest platform data, fine-tunes from the
current champion, evaluates on held-out data, and **promotes only when the
primary metric improves** (registry.json updated + MLflow stage/alias
transition when configured). Otherwise the candidate is kept as `challenger`
(so it immediately starts receiving AB_SPLIT traffic for observation).

Suggested schedule (compose needs listed in docs/AI.md):
- cron/Temporal: nightly `02:30` → `python -m training.continuous --once --source postgres`
- weekly full retrain: `python -m training.train --model all --source postgres --epochs 20`

## Neo4j bridge

`python -m training.export_graph --database-url ...` builds the
route/station/depot graph from Postgres (stations + depot HRS + K-means
terminus proxies over vehicle positions until a routes table exists), writes
the GNN adjacency npz, and — when `NEO4J_URI` is set and the `neo4j` driver is
installed — MERGEs nodes/edges into Neo4j. **When to use which:** Neo4j for
operational graph analytics (ad-hoc Cypher: "which stations become unreachable
if this depot closes", shortest refuelling paths, ops dashboards); the GNN for
*learned* propagation (delay/energy diffusion predictions served by
`/v1/ml/fleet/propagate`). Neo4j is optional; the GNN never depends on it.

## Env

| env | default | notes |
|---|---|---|
| `PORT` | `8095` | |
| `MODEL_ARTIFACTS_DIR` | `artifacts` | shared volume with predictive-maintenance |
| `AB_SPLIT` | `0.1` | challenger traffic fraction |
| `DRIFT_INTERVAL_S` / `DRIFT_WINDOW` / `DRIFT_PSI_WARN` | `60` / `512` / `0.2` | |
| `KEYCLOAK_ISSUER` / `KEYCLOAK_ISSUER_ALT` | unset / localhost alias | unset ⇒ scoring routes 503 (fail closed) |
| `MLFLOW_TRACKING_URI` | unset | enables MLflow logging/registry |
| `DATABASE_URL` / `LAKEHOUSE_DIR` / `NEO4J_URI` | unset | training/graph sources |
