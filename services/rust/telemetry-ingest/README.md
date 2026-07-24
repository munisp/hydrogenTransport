# telemetry-ingest (Rust)

Consumes `telemetry.raw` from Kafka, validates/enriches each record, batch-writes
the TimescaleDB hypertable `fleet.telemetry`, and republishes `telemetry.enriched`.
Gated on the **`telematics`** module toggle. The only HTTP surface is `/healthz` (port 8093).

## Pipeline

```
telemetry.raw ──► parse envelope ──► validate ──► enrich (Redis HGETALL bus:meta:<bus_id>)
              ──► batch INSERT fleet.telemetry (unnest arrays) ──► republish telemetry.enriched
              ──► commit offsets (async, at-least-once)
```

- **Batching**: up to `BATCH_SIZE` (default 500) records or `BATCH_MAX_WAIT_MS` (default 500 ms),
  whichever comes first. Single round-trip insert via `unnest($1::uuid[], …)`.
- **Backpressure-safe**: the consumer simply stops polling while a batch flushes — Kafka TCP
  flow control + `max.poll.interval.ms` bound memory; there is no unbounded queue. Offsets are
  committed only after the TimescaleDB write succeeds (DB failures retry with capped exponential
  backoff; at-least-once semantics).
- **Poison records**: malformed envelopes / implausible values are logged, dropped, and their
  offset committed so the consumer never wedges.
- **Toggle gate**: polls `GET $TOGGLE_URL/v1/toggles/telematics` every `TOGGLE_POLL_INTERVAL_S`
  (default 10 s). When disabled (or toggle-service errors — fail closed, SPEC §3.2) the
  subscription is paused (`consumer.pause`) while still heartbeating, so group membership is kept.
- **Enrichment contract**: `HSET bus:meta:<bus_id> route_id <id> depot_id <id> [heading_deg <deg>]`
  (populated by fleet-api/seed jobs). Redis outages degrade to null enrichment, never block ingestion.

## Fluvio edge bridge

Bus gateways publish to **Fluvio** at the edge (SPEC §3.8). A lightweight bridge mirrors the
Fluvio topic `bus-telemetry` into Kafka `telemetry.raw`:

```
bus gateway ──Fluvio SPU──► topic bus-telemetry ──bridge──► Kafka telemetry.raw ──► this service
```

Run it with the Fluvio CLI (or deploy `fluvio-connector` kafka-sink):

```bash
# one-shot mirror (edge cluster shell)
fluvio consume bus-telemetry -B --format=json \
  | kafka-console-producer --bootstrap-server "$KAFKA_BROKERS" --topic telemetry.raw

# or declarative connector (fluvio connector catalog: kafka-sink)
fluvio connector create --config kafka-sink-telemetry.yaml
# kafka-sink-telemetry.yaml:
#   meta: { name: telemetry-kafka-sink, type: kafka-sink, topic: bus-telemetry }
#   kafka: { url: "kafka:9092", topic: telemetry.raw }
```

Envelope contract is unchanged across the bridge (SPEC §3.3, `packages/events`).

## Configuration

| env | default | notes |
|---|---|---|
| `PORT` | `8093` | /healthz port |
| `KAFKA_BROKERS` | `localhost:9092` | |
| `KAFKA_GROUP_ID` | `telemetry-ingest` | consumer group |
| `INPUT_TOPIC` | `telemetry.raw` | |
| `OUTPUT_TOPIC` | `telemetry.enriched` | |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/h2fleet` | TimescaleDB |
| `REDIS_ADDR` | `localhost:6379` | host:port or redis:// URL |
| `TOGGLE_URL` | `http://localhost:8080` | toggle-service |
| `TOGGLE_MODULE` | `telematics` | gate module id |
| `BATCH_SIZE` / `BATCH_MAX_WAIT_MS` | `500` / `500` | flush triggers |
| `TOGGLE_POLL_INTERVAL_S` | `10` | |
| `RUST_LOG` | `info,telemetry_ingest=debug` | |

## Run

```bash
cargo run --release
# or
docker build -t h2fleet/telemetry-ingest .
docker run --rm -p 8093:8093 --env-file .env h2fleet/telemetry-ingest
curl localhost:8093/healthz
```

Registered in APISIX as health-only (no proxied API routes; internal consumer).
