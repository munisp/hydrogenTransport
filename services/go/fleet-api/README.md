# fleet-api

Domain 1 API — Fleet Operations & Telematics (SPEC §3.4 `fleet` schema). Port **8081**
(gateway prefix `/api/fleet/*`). Every route group is gated behind its module toggle;
a disabled module returns **404** (fail-closed via the toggle-client SDK).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| GET  | `/v1/vehicles` | `telematics` | — |
| GET  | `/v1/telemetry/latest` (latest reading per bus) | `telematics` | — |
| GET  | `/v1/maintenance/predictions?bus_id=&min_risk=` | `predictive-maintenance` | — |
| GET  | `/v1/fuel/levels` (latest H2 % + range estimate from `consumption_kg_per_100km` — learned per bus from `fuel.reading` events into `fleet.fuel_consumption`, else the 8 kg/100 km default; `consumption_source` = learned\|default) | `fuel-monitoring` | — |
| GET  | `/healthz` | — | — |

Wave 5 (migration 0008, multi-energy fleets): every vehicle response carries
`energy_type` (`h2`\|`battery`\|`diesel`\|`cng`, default `h2` — the existing
fleet is unchanged). Telemetry and fuel responses additionally expose the
generic `energy_level_pct` / `powertrain_kw` / `energy_remaining` fields
alongside the `h2_*` ones; for H2 buses the generic fields mirror the H2
readings, for battery/diesel/cng buses they carry the native readings. The
learned consumption rate is per bus (`fleet.fuel_consumption`), which already
keys it per energy vector (one bus = one `energy_type`); `energy_type` names
the native unit of `energy_remaining` (kg for h2/cng, kWh for battery,
liters for diesel).

Removed orphan routes (docs/BUSINESS_LOGIC_AUDIT.md — zero PWA/mobile
callers): `GET /v1/vehicles/{id}`, `GET /v1/vehicles/{id}/telemetry`, and
the digital-twin/route-optimizer proxies (`/v1/vehicles/{id}/twin`,
`/v1/optimize/route`). Twin and optimizer calls go directly to those
services via APISIX (`/api/twin/*`, `/api/optimize/*`).

## Configuration (env, SPEC §3.5)

| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8081` | |
| `DATABASE_URL` | — | required (Postgres + PostGIS/TimescaleDB) |
| `TOGGLE_URL` | — | toggle-service base URL; fail-closed when unset |
| `KEYCLOAK_ISSUER` | — | reserved (no authenticated routes currently) |

## Run

```sh
go run ./cmd/server
# container (build context = repo root, toggle-client SDK is COPYed in):
docker build -f services/go/fleet-api/Dockerfile -t h2fleet/fleet-api .
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
