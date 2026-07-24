# fleet-api

Domain 1 API — Fleet Operations & Telematics (SPEC §3.4 `fleet` schema). Port **8081**
(gateway prefix `/api/fleet/*`). Every route group is gated behind its module toggle;
a disabled module returns **404** (fail-closed via the toggle-client SDK).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| GET  | `/v1/vehicles` | `telematics` | — |
| GET  | `/v1/vehicles/{id}` | `telematics` | — |
| GET  | `/v1/vehicles/{id}/telemetry?from&to` (RFC3339, default last 24h) | `telematics` | — |
| GET  | `/v1/vehicles/{id}/twin` (proxy → digital-twin) | `digital-twin` | — |
| GET  | `/v1/maintenance/predictions?bus_id=` | `predictive-maintenance` | — |
| GET  | `/v1/fuel/levels` (latest H2 % + rule-based range estimate, 8 kg/100 km) | `fuel-monitoring` | — |
| POST | `/v1/optimize/route` (proxy → route-optimizer) | `route-energy-optimizer` | Keycloak JWT |
| GET  | `/healthz` | — | — |

## Configuration (env, SPEC §3.5)

| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8081` | |
| `DATABASE_URL` | — | required (Postgres + PostGIS/TimescaleDB) |
| `TOGGLE_URL` | — | toggle-service base URL; fail-closed when unset |
| `KEYCLOAK_ISSUER` | — | required for POST routes |
| `TWIN_URL` | `http://digital-twin:8092` | digital-twin upstream |
| `OPTIMIZER_URL` | `http://route-optimizer:8091` | route-optimizer upstream |

## Run

```sh
go run ./cmd/server
# container (build context = repo root, toggle-client SDK is COPYed in):
docker build -f services/go/fleet-api/Dockerfile -t h2fleet/fleet-api .
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
