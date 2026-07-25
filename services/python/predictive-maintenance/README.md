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
- Auth: `POST /v1/predict` requires a Keycloak RS256 Bearer token (SPEC §3.5) verified
  via the shared `h2fleet_auth` package (`services/python/shared`); `/healthz` stays public.

## Model

Preference order at startup (`app/model.py::load_model`):

1. **Trained LSTM artifact** from the ml-platform
   (`MODEL_ARTIFACTS_DIR/maintenance_lstm/<champion>/`, champion resolved via
   `registry.json`) — `app/lstm_model.py::LSTMScorer`. Consumes a resampled
   48-step raw telemetry window (`app/features.py::fetch_sequence`,
   5 features: h2 level, fuel-cell kW, battery SoC, speed, seasonal ambient
   temp estimate) instead of aggregate features. Requires `torch` (CPU wheel;
   optional — see requirements.txt).
2. **Legacy sklearn joblib** at `MODEL_PATH` (`SklearnModel`, 24h aggregates
   from `app/features.py`).
3. **Deterministic rule-based model** (`RuleModel`) — the service always runs
   without a trained artifact (SPEC §3.5).

The HTTP API contract is unchanged: `POST /v1/predict` still returns
per-component `risk_score` + `predicted_failure_at`, and the Kafka loop
(`maintenance.predicted`) works with any of the three backends. Set
`MODEL_ARTIFACTS_DIR` to the shared ml-platform artifacts volume to enable (1).

The aggregate feature engineering lives in `app/features.py` (24h aggregates over
`fleet.telemetry`: load volatility, max/avg fuel-cell kW, H2 min/avg, refuel
cycles via `LAG`, battery SoC stats, km driven).

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
| `KEYCLOAK_ISSUER` / `KEYCLOAK_ISSUER_ALT` | unset / `http://localhost:8088/realms/h2fleet` | JWKS source + accepted issuers; unset issuer ⇒ guarded routes 503 |
| `INPUT_TOPIC` / `OUTPUT_TOPIC` | `telemetry.enriched` / `maintenance.predicted` |
| `MODEL_PATH` | `models/model.joblib` |
| `MODEL_ARTIFACTS_DIR` | `artifacts` | ml-platform artifact root (shared volume); champion `maintenance_lstm` preferred when present |
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

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
