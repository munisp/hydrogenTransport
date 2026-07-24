# predictive-maintenance (Python, port 8090)

ML failure-risk scoring for the H2 fleet (module `predictive-maintenance`, Domain 1).

- `POST /v1/predict {"bus_id": "<uuid>"}` → risk scores (0..1) per component
  `fuel_cell | compressor | tank_valve | battery` + predicted failure time.
  Persists every run to `fleet.maintenance_predictions` (SPEC §3.4).
- Background Kafka loop consumes `telemetry.enriched` to track active buses and, every
  `SCORING_INTERVAL_S` (default 300 s), scores them and publishes `maintenance.predicted`
  (SPEC §3.3 envelope) for components with `risk_score >= HIGH_RISK_THRESHOLD` (0.7).
- Toggle-gated: when the module is OFF, API routes return 404 and the Kafka loop idles.
  APISIX: `/api/ml/*` → predictive-maintenance:8090 (SPEC §3.6).

## Model

Feature engineering lives in `app/features.py` (24h aggregates over `fleet.telemetry`:
load volatility, max/avg fuel-cell kW, H2 min/avg, refuel cycles via `LAG`, battery SoC
stats, km driven). Runtime loads `models/model.joblib` when present
(`app/model.py::SklearnModel`), otherwise falls back to a deterministic rule-based model
(`RuleModel`) — the service always runs without a trained artifact (SPEC §3.5).

### Training

```bash
pip install -r requirements.txt
python train.py --samples 5000 --out models/model.joblib
```

`train.py` trains one `RandomForestClassifier` per component on a deterministic synthetic
fleet (seeded, labels from noisy domain priors — real failure labels don't exist yet),
reports holdout ROC-AUC, and writes the artifact
`{"models", "features", "version", "trained_at", "metrics"}`.
The artifact's feature list is validated against the runtime at load.

## Configuration (env)

| env | default |
|---|---|
| `PORT` | `8090` |
| `DATABASE_URL` | `postgresql://postgres:postgres@localhost:5432/h2fleet` |
| `KAFKA_BROKERS` | `localhost:9092` |
| `TOGGLE_URL` | `http://localhost:8080` |
| `INPUT_TOPIC` / `OUTPUT_TOPIC` | `telemetry.enriched` / `maintenance.predicted` |
| `MODEL_PATH` | `models/model.joblib` |
| `FEATURE_WINDOW_HOURS` | `24` |
| `SCORING_INTERVAL_S` | `300` |
| `HIGH_RISK_THRESHOLD` | `0.7` |

## Run

```bash
uvicorn app.main:app --port 8090
# Docker (build context = repo root, for packages/toggle-client):
docker build -f services/python/predictive-maintenance/Dockerfile -t h2fleet/predictive-maintenance .
curl -X POST localhost:8090/v1/predict -H 'content-type: application/json' -d '{"bus_id":"<uuid>"}'
```
