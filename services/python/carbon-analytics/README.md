# carbon-analytics (Python — batch/CLI + optional read API on 8094)

CO2-avoidance accounting and credit issuance (module `carbon-credits`, Domain 3 / citizen).

## Method

Fleet distance per period = sum over buses of `max(odometer_km) - min(odometer_km)`
from `fleet.telemetry` within `[period start, period end)`. H2 buses have zero tailpipe
CO2, so:

```
kg_co2_avoided = total_km * 1.2            # diesel baseline (SPEC/mission)
credits        = kg_co2_avoided / 1000     # 1 credit = 1 tonne (CREDIT_KG_CO2)
```

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
| `POST /v1/carbon/compute {"period":"YYYY-MM","publish":true}` | recompute + republish |

Toggle-gated: module OFF → routes 404. Consumed by citizen-api `/api/citizen/*`
(this service is internal; not directly in the APISIX prefix table of SPEC §3.6).

## Configuration (env)

| env | default |
|---|---|
| `PORT` / `ENABLE_API` | `8094` / `true` |
| `DATABASE_URL` | `postgresql://postgres:postgres@localhost:5432/h2fleet` |
| `KAFKA_BROKERS` | `localhost:9092` |
| `TOGGLE_URL` | `http://localhost:8080` |
| `OUTPUT_TOPIC` | `carbon.credit.issued` |
| `DIESEL_BASELINE_KG_CO2_PER_KM` | `1.2` |
| `CREDIT_KG_CO2` | `1000.0` |

## Run

```bash
uvicorn app.main:app --port 8094
# Docker (build context = repo root):
docker build -f services/python/carbon-analytics/Dockerfile -t h2fleet/carbon-analytics .
docker run --rm h2fleet/carbon-analytics python -m app.cli --period 2025-01
```
