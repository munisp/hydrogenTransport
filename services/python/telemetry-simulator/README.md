# telemetry-simulator

Headless asyncio producer that stands in for the physical bus fleet (and, in
production, the Fluvio edge bridge — see `docs/ARCHITECTURE.md`). It publishes
plausible telemetry for **all 50 seeded buses** to Kafka topic
`telemetry.raw` in the SPEC §3.3 CloudEvents-ish envelope, matching
`packages/events/schemas/telemetry.raw.json`.

## What it does

1. Loads every bus from `fleet.vehicles` (id, fleet_no, status, position) and
   the depots from `infra.stations` (Postgres, via asyncpg).
2. Assigns each bus a synthetic route (`R1`..`R6`) and depot, then writes
   `HSET bus:meta:<id> {fleet_no, route_id, depot_id, heading_deg}` to Redis —
   the enrichment lookup consumed by `telemetry-ingest` when it produces
   `telemetry.enriched`.
3. Every `SIM_INTERVAL_SECONDS` (default 5) advances a random-walk movement
   model and publishes one envelope per bus to `telemetry.raw`:
   - heading-jittered movement around the seeded grid positions;
   - speed 0–50 kph, mean-reverting with occasional stops (buses seeded with
     status `depot`/`maintenance` stay parked but keep reporting);
   - `h2_level_pct` drains with distance (`SIM_H2_DRAIN_PCT_PER_KM`, default
     0.35 %/km) and resets to ~full on a refuel event at
     `SIM_REFUEL_THRESHOLD_PCT` (default 15 %);
   - `odometer_km`, `fuel_cell_kw`, `battery_soc_pct` evolve consistently.

## Configuration (env, SPEC §3.5 contract + SIM_*)

| Var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | `postgres://h2:h2pass@localhost:5432/h2fleet?sslmode=disable` | fleet DB |
| `KAFKA_BROKERS` | `localhost:9094` | host-side Kafka listener (in compose: `kafka:9092`) |
| `REDIS_ADDR` | `localhost:6379` | Redis for `bus:meta:*` |
| `KAFKA_TOPIC` | `telemetry.raw` | output topic |
| `SIM_INTERVAL_SECONDS` | `5` | publish period |
| `SIM_H2_DRAIN_PCT_PER_KM` | `0.35` | tank drain rate |
| `SIM_REFUEL_THRESHOLD_PCT` | `15` | refuel trigger level |
| `SIM_SOURCE` | `telemetry-simulator` | envelope `source` field |
| `CONNECT_RETRIES` / `CONNECT_RETRY_SECONDS` | `40` / `3` | startup wait for middleware |

## Run

```bash
# in the full local stack (middleware must be up: make up)
docker compose -f infra/docker-compose.yml --profile apps up -d --build telemetry-simulator
# or: make simulate

# locally against the compose middleware
cd services/python/telemetry-simulator
pip install -r requirements.txt
python -m app.main
```

Watch the stream:

```bash
docker exec -i h2-kafka kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 --topic telemetry.raw --max-messages 2
```

Health: no HTTP port — the container healthcheck verifies the heartbeat file
written each tick stays fresh.
