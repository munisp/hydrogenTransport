# H2Fleet API Reference

All traffic enters through APISIX at `http://localhost:9080`. The gateway strips
`/api/<domain>` (proxy-rewrite), so a call to `/api/fleet/v1/vehicles` arrives
at fleet-api as `/v1/vehicles` (SPEC §3.6).

* Auth: `Authorization: Bearer <keycloak jwt>` for mutating routes; public GETs
  on toggles/citizen pass through unauthenticated (see `infra/apisix/apisix.yaml`).
* **Payment and DRT reads also require auth**: unauthenticated calls to
  `/v1/payments*` and `/v1/drt/requests*` return `401 unauthorized`.
* **Role-gated mutations**: routes marked `JWT (operator)` / `JWT (driver)`
  return `403 forbidden` without the realm role.
* **Downstream failures**: payment routes return `502 bad_gateway` when the
  TigerBeetle ledger or Mojaloop switch/simulator fails, and publish
  `fare.payment.failed`. The simulated ledger rejects negative balances, so
  dev payments without a reachable TigerBeetle fail with 502.
* **Python mutating endpoints require JWTs too**: `POST /api/ml/v1/predict`,
  `POST /api/optimize/v1/optimize/route`, `POST /v1/carbon/compute`
  (carbon-analytics, internal-only).
* Machine consumers: `apikey: h2fleet-partner-demo-key` on `/api/open-data/*` (demo;
  gateway rewrites to citizen-api `/v1/opendata/*`).
* Every service exposes `GET /healthz` (unauthenticated).
* Disabled modules return `404 {"error":"module disabled","module":"<id>"}`.
* Event topics beyond SPEC §3.3: `fare.payment.failed` (payment failure
  companion event) and `telemetry.raw.dlq` (dead-letter for malformed raw
  telemetry — envelope shape in `services/rust/telemetry-ingest/README.md`).
  Schemas + fixtures live in `packages/events/`.

## toggle-service — `/api/toggles` → :8080

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/toggles` | public | `{"toggles": {"<module>": bool, ...}}` |
| GET | `/v1/toggles/{module}` | public | `{"module","enabled","domain"}` |
| PUT | `/v1/toggles/{module}` | `platform-admin` | body `{"enabled": bool}` → updates PG, Redis, publishes `toggle.changed` |

## fleet-api — `/api/fleet` → :8081

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/vehicles` | public | List buses (fleet_no, status, last geom) |
| GET | `/v1/vehicles/{id}` | public | Vehicle detail |
| GET | `/v1/vehicles/{id}/telemetry?from&to` | public | Timescale hypertable window query |
| GET | `/v1/telemetry/latest` | public | Latest telemetry sample per bus (`DISTINCT ON (bus_id) ... ORDER BY bus_id, ts DESC`), JSON array |
| GET | `/v1/vehicles/{id}/twin` | public | Proxy to digital-twin for one bus |
| GET | `/v1/maintenance/predictions?bus_id=` | public | Rows from `fleet.maintenance_predictions` |
| GET | `/v1/fuel/levels` | public | Latest H2 level per vehicle + estimated range |
| POST | `/v1/optimize/route` | JWT (`operator`) | Proxy to route-optimizer |

## infra-api — `/api/infra` → :8082

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/stations` | public | Stations + `available_kg` inventory + status |
| GET | `/v1/stations/{id}` | public | Station detail |
| POST | `/v1/stations` | JWT | Create station |
| PATCH | `/v1/stations/{id}/status` | JWT (`operator`) | Set online/offline/maintenance |
| GET | `/v1/incidents` | public | List incidents |
| POST | `/v1/incidents` | JWT | Open incident (leak workflow starts in Temporal) |
| POST | `/v1/incidents/{id}/ack` | JWT (`operator`) | Acknowledge incident |
| POST | `/v1/incidents/{id}/resolve` | JWT (`operator`) | Resolve incident |
| POST | `/v1/safety/leak` | sensor token / JWT | Leak sensor webhook |
| GET | `/v1/dispatch/jobs` | public | List dispatch jobs |
| POST | `/v1/dispatch/jobs` | JWT (`operator`) | Assign job → `dispatch.job.assigned` + Temporal signal |
| POST | `/v1/dispatch/jobs/{id}/accept` | JWT | Driver accepts job (status `assigned` → `accepted`, stamps `accepted_at`) |
| GET | `/v1/compliance/reports` | public | Generated compliance reports |
| GET | `/v1/compliance/reports/{id}` | public | One report |
| POST | `/v1/compliance/reports/generate` | JWT (`platform-admin`) | Trigger report generation |
| GET | `/v1/depot/bays` | public | Depot bays (fueling/charging/parking/workshop) + occupancy |
| GET/POST | `/v1/depot/work-orders` | public / JWT | Depot work orders (from predictions/leaks) |
| POST | `/v1/depot/work-orders/{id}/close` | JWT | Close a work order |

## citizen-api — `/api/citizen` → :8083

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/passenger/stops` | public | GTFS stops |
| GET | `/v1/passenger/routes` | public | GTFS routes |
| GET | `/v1/passenger/arrivals?stop_id=` | public | Headway-based arrival predictions |
| GET | `/v1/passenger/journey?from=&to=` | public | Direct-route journey options between two stop IDs |
| GET | `/v1/passenger/alerts` | public | Active service alerts |
| GET | `/v1/mobile/config` | public | Bootstrap config for the Expo apps |
| POST | `/v1/drt/requests` | JWT (`citizen`) | Book DRT shuttle → `drt.requested` |
| GET | `/v1/drt/requests` | JWT | Own requests (row owner = `sub`); 401 unauthenticated |
| GET | `/v1/drt/requests/{id}` | JWT | One request (owner or `operator`); 401 unauthenticated |
| POST | `/v1/drt/requests/{id}/cancel` | JWT | Cancel a `requested`/`assigned` request (404 unknown, 409 not cancellable) |
| GET | `/v1/carbon/credits` | public | Issued credits + kg CO2 avoided |
| GET | `/v1/carbon/credits/summary` | public | Totals across periods |
| GET | `/v1/opendata/datasets` | public/key-auth | Open dataset catalog (via `/api/open-data/*` for key-auth consumers) |
| GET | `/v1/opendata/search?q=` | public/key-auth | OpenSearch-backed dataset search |

## commerce-api — `/api/commerce` → :8084

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/payments` | JWT (`citizen`) + `Idempotency-Key` header (400 without) | Initiate fare, body `{"amount_minor": 250, "currency": "EUR"}`: Mojaloop transfer + TB ledger entry → `fare.payment.initiated`; same key replays the original payment; 502 + `fare.payment.failed` on ledger/switch failure |
| GET | `/v1/payments` | JWT | List payments (own for `citizen`, all for `operator`); 401 unauthenticated |
| GET | `/v1/payments/{id}` | JWT | Payment detail; 401 unauthenticated |
| GET | `/v1/loyalty/balance` | JWT | Caller loyalty point balance |
| POST | `/v1/loyalty/redeem` | JWT (`citizen`) | Redeem an offer, body `{"offer_id": "..."}` → `{redeemed_offer_id, points_spent, remaining_points}` |
| GET | `/v1/marketplace/offers` | public | Loyalty marketplace offers |
| POST | `/v1/marketplace/offers` | JWT | Publish an offer |
| GET/POST | `/v1/energy/trades` | public / JWT (`operator`) + `Idempotency-Key` header (400 without) | Energy/H2 trade, body `{"kind": "h2-sale\|h2-purchase\|energy-export", "quantity_kg": 25, "price_minor": 84000}`: proposed → station-surplus backing check (409 `insufficient_surplus`) → TigerBeetle settlement (402 `insufficient_funds` when energy clearing is unfunded) → `executed` + `energy.trade.executed`; same key replays the trade |
| GET | `/v1/gov/kpis` | public | Gov dashboard KPIs (each rollup independently nullable; failed sources are named in `degraded` + `partial: true`): `revenue_30d_minor`, `settled_payments_30d`, `ridership_estimate_30d`, `kg_co2_avoided_total`, `carbon_credits_total`, `vehicles_total`, `vehicles_active`, `fleet_active_ratio_pct`, `fleet_uptime_pct` (null until a time-based source exists), `stations_available_kg`, `open_incidents` |
| GET/POST | `/v1/ads/campaigns` | public / JWT | Ad inventory & campaigns |
| GET/PATCH | `/v1/ads/campaigns/{id}` | public / JWT | Campaign detail / update |

## predictive-maintenance — `/api/ml` → :8090

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/predict` | JWT | body `{"bus_id": "<uuid>"}` → `{bus_id, model_version, feature_window_hours, predictions: [{component, risk_score, predicted_failure_at}]}` |

## route-optimizer — `/api/optimize` → :8091

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/optimize/route` | JWT | body `{"bus_ids": [...]?, "date": "YYYY-MM-DD"}` → `{date, data_source, solver_status, unassigned_stops, plans: [BusPlan]}` |

## carbon-analytics — internal-only (NOT routed via APISIX)

`carbon-analytics` is an internal batch/service component: it is deliberately
absent from the gateway prefix map (SPEC §3.6) and reachable only in-network
on :8094. `POST /v1/carbon/compute` requires a service JWT.

## digital-twin — `/api/twin` → :8092

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/twin` | public | All twins (Redis hot state), `{"twins": [...], "count": n}` |
| GET | `/v1/twin/{bus_id}` | public | One twin: latest state + `updated_at` |

## Error format

`{"error": "<code>", "message": "<human readable>", "module?": "<id>"}` —
codes: `unauthorized` (401), `forbidden` (403, role/Permify deny),
`module disabled` (404), `validation` (400), `not_found` (404),
`internal` (500), `bad_gateway` (502 — ledger/Mojaloop/OpenSearch downstream
failure).
