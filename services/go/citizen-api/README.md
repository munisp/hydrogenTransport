# citizen-api

Domain 3 API — Citizen & Engagement (SPEC §3.4 `citizen` schema). Port **8083**
(gateway prefix `/api/citizen/*`). Each route group is gated behind its module
toggle; a disabled module returns **404** (fail-closed).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| GET  | `/v1/passenger/stops`, `/v1/passenger/routes` (GTFS-static style seed) | `passenger-pwa` | — |
| GET  | `/v1/passenger/arrivals?stop_id=` (headway timetable) | `passenger-pwa` | — |
| GET  | `/v1/passenger/journey?from=&to=` (rule-based direct-route planner) | `passenger-pwa` | — |
| GET  | `/v1/passenger/alerts` (GTFS-RT style service alerts) | `passenger-pwa` | — |
| GET  | `/v1/mobile/config` (app bootstrap + citizen module states) | `mobile-app` | — |
| POST | `/v1/drt/requests` `{pickup{lat,lon}, dropoff{lat,lon}, pickup_label, dropoff_label, passengers}` → row in `citizen.drt_requests` + publishes `drt.requested` | `demand-responsive` | JWT |
| GET  | `/v1/drt/requests`, `/v1/drt/requests/{id}` | `demand-responsive` | JWT (list) |
| POST | `/v1/drt/requests/{id}/cancel` (owner, or operator/platform-admin; non-owners get 404) | `demand-responsive` | JWT |
| POST | `/v1/drt/requests/{id}/assign` `{vehicle_id, driver_sub}` — requested → assigned, publishes `drt.assigned` | `demand-responsive` | JWT (operator) |
| POST | `/v1/drt/requests/{id}/start`, `/v1/drt/requests/{id}/complete` — assigned → enroute → completed | `demand-responsive` | JWT (driver\|operator) |
| GET  | `/v1/carbon/credits?period=`, `/v1/carbon/credits/summary` | `carbon-credits` | — |
| GET  | `/v1/opendata/datasets` (catalog) | `open-data-portal` | — |
| GET  | `/v1/opendata/search?q=` (proxy → OpenSearch `_search`) | `open-data-portal` | — |
| GET  | `/v1/opendata/gtfs` (full GTFS static feed as zip), `/v1/opendata/gtfs/{file}` (`stops.txt`, `routes.txt`, `trips.txt`, `stop_times.txt`, `frequencies.txt` as CSV) | `open-data-portal` | — |
| GET  | `/healthz` | — | — |

## Event publishing (SPEC §3.5)

Events use the **Dapr pubsub building block** (component `h2pubsub`) when
`DAPR_GRPC_PORT` is set; otherwise they fall back to **direct Kafka** via
`KAFKA_BROKERS`; otherwise a logging no-op publisher.

## Configuration (env, SPEC §3.5)

| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8083` | |
| `DATABASE_URL` | — | optional; DRT/carbon endpoints return 503 when unset |
| `TOGGLE_URL` | — | fail-closed when unset |
| `KEYCLOAK_ISSUER` | — | in-network realm URL; `KEYCLOAK_ISSUER_ALT` also accepted |
| `DAPR_GRPC_PORT` | — | enables Dapr pubsub (`DAPR_PUBSUB_NAME`, default `h2pubsub`) |
| `KAFKA_BROKERS` | — | direct-Kafka fallback |
| `OPENSEARCH_URL` | — | e.g. `http://opensearch:9200` |
| `OPENSEARCH_INDEX` | `h2fleet-open` | |

## Run

```sh
go run ./cmd/server
docker build -f services/go/citizen-api/Dockerfile -t h2fleet/citizen-api .   # context = repo root
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
