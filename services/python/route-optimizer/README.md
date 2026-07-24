# route-optimizer (Python, port 8091)

Route + refuel planning with OR-Tools VRP and hydrogen range constraints
(module `route-energy-optimizer`, Domain 1). APISIX: `/api/optimize/*` → route-optimizer:8091.

## `POST /v1/optimize/route`

```json
{ "bus_ids": ["<uuid>", ...] | null, "date": "2025-01-15" }
```

Response: per-bus plan with ordered legs (cumulative km), refuel events
(station, kg, insertion point), feasibility notes, plus solver status and any
unassigned stops. `data_source` is `"database"` or `"seed"`.

### Model

- **Phase 1 — VRP (CP-SAT)**: buses start at their live positions (latest
  `fleet.telemetry`, fallback `fleet.vehicles.geom`) and end at the depot.
  A per-vehicle `Range` dimension caps cumulative route distance at
  `h2_kg_on_board / H2_CONSUMPTION_KG_PER_KM` (default 0.08 kg/km) plus one
  refill of headroom when stations have stock. Stops that fit no range budget
  are dropped with a penalty and reported in `unassigned_stops`.
- **Phase 2 — refuel insertion (deterministic)**: walks each route and inserts
  the nearest safely-reachable station (never below `RANGE_SAFETY_KM`,
  default 20 km) with sufficient stock; station inventory is decremented so
  simultaneous refuels respect `infra.stations.available_kg`.

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
