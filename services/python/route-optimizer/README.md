# route-optimizer (Python, port 8091)

Route + refuel planning with OR-Tools VRP and hydrogen range constraints
(module `route-energy-optimizer`, Domain 1). APISIX: `/api/optimize/*` → route-optimizer:8091.

Auth: `POST /v1/optimize/route` requires a Keycloak RS256 Bearer token (SPEC §3.5)
verified via the shared `h2fleet_auth` package (`services/python/shared`);
`/healthz` stays public. Env: `KEYCLOAK_ISSUER` (JWKS source; unset ⇒ guarded
routes 503), `KEYCLOAK_ISSUER_ALT` (extra accepted issuers, default
`http://localhost:8088/realms/h2fleet`).

## `POST /v1/optimize/route`

```json
{ "bus_ids": ["<uuid>", ...] | null, "date": "2025-01-15" }
```

Response: per-bus plan with ordered legs (cumulative km), refuel events
(station, kg, insertion point), feasibility notes, plus solver status and any
unassigned stops. `data_source` is `"database"` or `"seed"`.

### Model

Multi-energy (Wave-5): every bus has an `energy_type` (`h2|battery|diesel|cng`,
schema contract 0008) and consumption/range math is unit-labeled per type —
kg/100km for h2+cng, kWh/100km for battery, L/100km for diesel. The learned
per-bus rate from `fleet.fuel_consumption` (the Wave-4 learner that fleet-api
exposes at `GET /v1/fuel/levels` as `consumption_kg_per_100km` +
`consumption_source=learned|default`) wins over the fleet defaults.
An h2 bus with no learned rate computes **identical** numbers to the
pre-Wave-5 code.

- **Phase 1 — VRP (CP-SAT)**: buses start at their live positions (latest
  `fleet.telemetry`, fallback `fleet.vehicles.geom`) and end at the depot.
  A per-vehicle `Range` dimension caps cumulative route distance at
  `energy_on_board / consumption_per_km(energy_type)` (h2 default
  0.08 kg/km) plus one refill of headroom when stations have stock. Stops
  that fit no range budget are dropped with a penalty and reported in
  `unassigned_stops`.
- **Phase 2 — refuel insertion (deterministic)**: walks each route and inserts
  the nearest safely-reachable COMPATIBLE station (never below
  `RANGE_SAFETY_KM`, default 20 km) with sufficient stock: `station_type`
  must match the bus energy_type (`h2→h2`, `battery→ev_charger`,
  `diesel→diesel`, `cng→cng`); `mixed` stations serve all but are weighted by
  `MIXED_STATION_DETOUR_FACTOR` (default 1.25) so a dedicated station is
  preferred at comparable distance. Station inventory is decremented so
  simultaneous refuels respect stock — `available_kwh` for kWh draws (0008),
  `available_kg` otherwise. Responses are unit-labeled (`energy_type`,
  `energy_unit`, `consumption_per_100km`, `consumption_source` on each plan;
  `station_type` + `energy_unit` on each refuel event; the legacy
  `h2_start_kg`/`h2_end_kg`/`kg_taken` fields keep their names and carry the
  amount in the labeled unit).

### Data sources & fallback

- Buses: `fleet.vehicles` + latest `fleet.telemetry` (position, H2 level).
- Stations: `infra.stations` where `status='online'`.
- Stops: SPEC §3.4 has no route-stop table yet, so daily waypoints come from a
  **deterministic generator seeded by date** (`app/data.py::generate_stops`);
  swap in a `route_stops` lookup without changing the VRP contract.
- If the fleet table is empty, a fully deterministic seed fleet (5 buses,
  3 stations, 12 stops around the operating area) is used so the endpoint is
  always exercisable (`data_source="seed"`).

Toggle-gated: module OFF → routes return 404 (SPEC §3.2).

## Configuration (env)

| env | default |
|---|---|
| `PORT` | `8091` |
| `DATABASE_URL` | `postgresql://postgres:postgres@localhost:5432/h2fleet` |
| `TOGGLE_URL` | `http://localhost:8080` |
| `H2_CONSUMPTION_KG_PER_KM` | `0.08` |
| `BATTERY_CONSUMPTION_KWH_PER_KM` | `1.1` |
| `DIESEL_CONSUMPTION_L_PER_KM` | `0.40` |
| `CNG_CONSUMPTION_KG_PER_KM` | `0.30` |
| `MIXED_STATION_DETOUR_FACTOR` | `1.25` |
| `RANGE_SAFETY_KM` | `20.0` |
| `SOLVER_TIME_LIMIT_S` | `10` |
| `STOP_DROP_PENALTY` | `50000` |

## Run

```bash
uvicorn app.main:app --port 8091
curl -X POST localhost:8091/v1/optimize/route \
  -H 'content-type: application/json' -d '{"date":"2025-01-15"}'
# Docker (build context = repo root):
docker build -f services/python/route-optimizer/Dockerfile -t h2fleet/route-optimizer .
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
