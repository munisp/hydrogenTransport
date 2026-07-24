# digital-twin (Rust)

Per-bus digital twin for the H2Fleet platform (SPEC.md, Domain 1 / `digital-twin` module).

- Consumes `telemetry.enriched` (Kafka) and keeps the **hot twin** as JSON at Redis key
  `twin:<bus_id>` (TTL `TWIN_TTL_S`, refreshed per update; fleet index in set `twin:buses`).
- Publishes `twin.updated` (envelope per SPEC §3.3 / `packages/events`) on every accepted update.
- **Snapshots** all hot twins into Postgres `fleet.twin_snapshots` every `SNAPSHOT_INTERVAL_S` (default 60 s).
- Serves the read API on port **8092** and is gated on the `digital-twin` toggle:
  when the module is OFF, `/v1/twin*` returns **404** and the Kafka consumer pauses (SPEC §3.2).

## API

| route | description |
|---|---|
| `GET /healthz` | liveness + redis + toggle state |
| `GET /v1/twin/{bus_id}` | hot twin for one bus (404 when disabled or unknown) |
| `GET /v1/twin` | `{ "twins": [...], "count": n }` — all hot twins |

APISIX: `/api/twin/*` → digital-twin:8092 (SPEC §3.6).

## Twin state shape

```json
{
  "bus_id": "uuid", "ts": "rfc3339", "speed_kph": 32.4, "h2_level_pct": 61.2,
  "fuel_cell_kw": 84.0, "battery_soc_pct": 73.5, "odometer_km": 41230.7,
  "lat": 52.52, "lon": 13.405, "route_id": "R12", "depot_id": "DEPOT-NORTH",
  "heading_deg": 90.0, "status": "moving|idle|refueling", "updated_at": "rfc3339"
}
```

## Required DDL (owned by infra/sql, documented here for contract)

```sql
CREATE TABLE IF NOT EXISTS fleet.twin_snapshots (
    id      bigserial PRIMARY KEY,
    bus_id  uuid NOT NULL,
    ts      timestamptz NOT NULL DEFAULT now(),
    state   jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS twin_snapshots_bus_ts ON fleet.twin_snapshots (bus_id, ts DESC);
```

## Configuration

| env | default | notes |
|---|---|---|
| `PORT` | `8092` | |
| `KAFKA_BROKERS` | `localhost:9092` | |
| `KAFKA_GROUP_ID` | `digital-twin` | |
| `INPUT_TOPIC` | `telemetry.enriched` | |
| `OUTPUT_TOPIC` | `twin.updated` | |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/h2fleet` | |
| `REDIS_ADDR` | `localhost:6379` | |
| `TOGGLE_URL` | `http://localhost:8080` | |
| `TOGGLE_MODULE` | `digital-twin` | |
| `TOGGLE_POLL_INTERVAL_S` | `10` | |
| `SNAPSHOT_INTERVAL_S` | `60` | |
| `TWIN_TTL_S` | `900` | hot-state TTL |

## Run

```bash
cargo run --release
# or
docker build -t h2fleet/digital-twin .
docker run --rm -p 8092:8092 --env-file .env h2fleet/digital-twin
curl localhost:8092/v1/twin
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
