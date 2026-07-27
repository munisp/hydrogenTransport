# carbon-analytics (Python — batch/CLI + optional read API on 8094)

CO2-avoidance accounting and credit issuance (module `carbon-credits`, Domain 3 / citizen).

## Method

Fleet distance per period = sum over buses of `max(odometer_km) - min(odometer_km)`
from `fleet.telemetry` within `[period start, period end)`, grouped by
`fleet.vehicles.energy_type` (migration 0008; pre-0008 databases fall back to
the legacy all-h2 aggregate). Credits use a **diesel-reference baseline per
energy_type**:

```
h2:      avoided = km * 1.2                                   # zero tailpipe
battery: avoided = km * (1.2 - EV_KWH_PER_KM * GRID_CO2_KG_PER_KWH)
         # diesel reference minus grid-electricity footprint, floor 0
diesel:  avoided = 0        # diesel IS the reference baseline
cng:     avoided = km * (1.2 - CNG_KG_CO2_PER_KM)             # floor 0
credits  = kg_co2_avoided / CREDIT_KG_CO2   # 1 credit = 1 tonne by default
```

The per-type breakdown (`baseline_by_energy_type`) is attached to the issued
credit and the `carbon.credit.issued` event payload so the CARBON_FUND
ledger-leg consumer (commerce-api) reconciles against the right baseline per
bus energy_type. Grid factor is config-driven (`GRID_CO2_KG_PER_KWH`, default
0.35 kg CO2/kWh ≈ EU grid average; `EV_KWH_PER_KM` default 1.1 for a city bus)
until per-bus learned kWh/km exists.

Writes `citizen.carbon_credits` (**idempotent per period**: delete + insert in one
transaction) and publishes `carbon.credit.issued` (SPEC §3.3 envelope, key = period).

## Batch / CLI

```bash
python -m app.cli --period 2025-01     # explicit month
python -m app.cli                      # previous calendar month
python -m app.cli --no-publish         # DB only, no Kafka event
```

Exits 0 without work when the `carbon-credits` toggle is OFF (fail-closed).
Schedule via cron / k8s CronJob, e.g. `0 3 1 * *` (monthly).

## Read API (ENABLE_API=true, port 8094)

| route | description |
|---|---|
| `GET /healthz` | liveness + db + toggle state |
| `GET /v1/carbon/credits?period=YYYY-MM` | issued credits (all periods when omitted) |
| `POST /v1/carbon/compute {"period":"YYYY-MM","publish":true}` | recompute + republish (Keycloak RS256 Bearer token required, SPEC §3.5) |

Toggle-gated: module OFF → routes 404. Consumed by citizen-api `/api/citizen/*`
(this service is internal; not directly in the APISIX prefix table of SPEC §3.6).
Auth is verified via the shared `h2fleet_auth` package (`services/python/shared`);
`/healthz` and `GET /v1/carbon/credits` stay public. Env: `KEYCLOAK_ISSUER`
(JWKS source; unset ⇒ guarded routes 503), `KEYCLOAK_ISSUER_ALT` (extra accepted
issuers, default `http://localhost:8088/realms/h2fleet`).

## Configuration (env)

| env | default |
|---|---|
| `PORT` / `ENABLE_API` | `8094` / `true` |
| `DATABASE_URL` | `postgresql://postgres:postgres@localhost:5432/h2fleet` |
| `KAFKA_BROKERS` | `localhost:9092` |
| `TOGGLE_URL` | `http://localhost:8080` |
| `OUTPUT_TOPIC` | `carbon.credit.issued` |
| `DIESEL_BASELINE_KG_CO2_PER_KM` | `1.2` |
| `GRID_CO2_KG_PER_KWH` | `0.35` |
| `EV_KWH_PER_KM` | `1.1` |
| `CNG_KG_CO2_PER_KM` | `1.0` |
| `CREDIT_KG_CO2` | `1000.0` |

## Run

```bash
uvicorn app.main:app --port 8094
# Docker (build context = repo root):
docker build -f services/python/carbon-analytics/Dockerfile -t h2fleet/carbon-analytics .
docker run --rm h2fleet/carbon-analytics python -m app.cli --period 2025-01
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
