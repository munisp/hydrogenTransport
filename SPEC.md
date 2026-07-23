# SPEC.md — H2Fleet: Unified Hydrogen Bus Platform (Single Source of Truth)

## 1. Overview
H2Fleet is a unified, module-toggleable platform for managing a 50-bus hydrogen fleet.
20 capability ideas are grouped into **4 domains**, each domain exposing **toggleable modules**.
A module can be turned ON/OFF per deployment via the central **Feature Toggle Service**; when OFF,
its API routes return 404, its UI nav entries disappear, its Kafka consumers/Temporal workflows
do not start, and its Dapr components are not loaded.

### The 20 ideas by domain
**Domain 1 — Fleet Operations & Telematics (`fleet`)**
1. `telematics` — real-time vehicle telemetry ingestion & live map
2. `predictive-maintenance` — ML failure prediction on fuel-cell/battery/H2 systems
3. `digital-twin` — per-bus digital twin state (Rust hot path)
4. `fuel-monitoring` — H2 tank levels, consumption, range prediction
5. `route-energy-optimizer` — route + refueling schedule optimization

**Domain 2 — Infrastructure & Safety (`infra`)**
6. `refueling-stations` — station status, queue mgmt, inventory
7. `leak-detection` — H2 leak sensor ingestion, alarms, incident workflow
8. `dispatch-workforce` — driver scheduling & dispatch (Temporal workflows)
9. `compliance-reporting` — regulatory & safety compliance reports
10. `depot-management` — depot bays, charging/fueling assets, work orders

**Domain 3 — Citizen & Engagement (`citizen`)**
11. `passenger-pwa` — citizen PWA: arrivals, journey planner, service alerts
12. `mobile-app` — native mobile (Expo) citizen + driver apps
13. `demand-responsive` — DRT on-demand shuttle requests
14. `carbon-credits` — CO2 avoided accounting + credit issuance
15. `open-data-portal` — GTFS/GTFS-RT + open datasets + OpenSearch API

**Domain 4 — Commerce & Finance (`commerce`)**
16. `fare-payments` — fare collection (Mojaloop rails, TigerBeetle ledger)
17. `loyalty-marketplace` — citizen rewards, local business marketplace
18. `energy-trading` — surplus H2/energy trading ledger
19. `gov-dashboard` — city KPI dashboard (cost, emissions, ridership, uptime)
20. `advertising` — on-bus/digital ad inventory & campaigns

## 2. Monorepo Layout
```
/services
  go/
    toggle-service/      # Feature toggle control plane (REST + Redis cache)   [ALL]
    fleet-api/           # Domain 1 API: vehicles, telemetry queries, twin access
    infra-api/           # Domain 2 API: stations, incidents, depot, compliance
    commerce-api/        # Domain 4 API: fares, ledger, marketplace, ads, KPIs
    citizen-api/         # Domain 3 API: passenger, DRT, carbon, open-data
  rust/
    telemetry-ingest/    # Kafka/Fluvio consumer → Postgres/TigerBeetle writes [fleet]
    digital-twin/        # Twin state engine (Redis + Postgres)                 [fleet]
  python/
    predictive-maintenance/  # ML: failure-risk scoring service                [fleet]
    route-optimizer/         # OR-Tools route/energy optimization              [fleet]
    carbon-analytics/        # CO2 accounting + credit batch jobs              [citizen]
    lakehouse-etl/           # Spark + Apache Sedona geospatial ETL → lakehouse [all]
/apps
  pwa/                   # React 18 + TS + Vite PWA: all 4 domain dashboards + citizen app
  mobile/                # Expo React Native skeleton (citizen + driver)
/infra
  docker-compose.yml     # full middleware stack + all services
  k8s/                   # kustomize base + per-domain overlays
  dapr/components/       # pubsub, statestore, bindings yaml
  apisix/                # gateway routes config
  keycloak/              # realm export
  permify/               # authorization schema
  sql/                   # Postgres init schemas
/packages
  events/                # AsyncAPI event catalog + JSON schemas
  toggle-client/         # Go + TS + Python toggle client SDK (same contract)
/docs                    # ARCHITECTURE.md, MODULES.md, API.md, DEPLOYMENT.md
Makefile  SPEC.md  plan.md  README.md  .github/workflows/ci.yml
```

## 3. Shared Contracts (SACRED — implement exactly)

### 3.1 Module identifiers (exact strings)
`telematics predictive-maintenance digital-twin fuel-monitoring route-energy-optimizer
refueling-stations leak-detection dispatch-workforce compliance-reporting depot-management
passenger-pwa mobile-app demand-responsive carbon-credits open-data-portal
fare-payments loyalty-marketplace energy-trading gov-dashboard advertising`

### 3.2 Feature Toggle contract
- toggle-service REST:
  - `GET /v1/toggles` → `{ "toggles": { "<module>": true|false, ... } }`
  - `GET /v1/toggles/{module}` → `{ "module": "<id>", "enabled": true, "domain": "<domain>" }`
  - `PUT /v1/toggles/{module}` body `{ "enabled": bool }` (admin only, Keycloak role `platform-admin`)
- Storage: Postgres table `feature_toggles(module text pk, domain text, enabled bool, updated_at timestamptz)`; Redis cache key `toggles:<module>` TTL 30s; publishes `toggle.changed` to Kafka on change.
- Every service calls `GET /v1/toggles/{module}` (or SDK) at startup AND subscribes/caches; disabled → service's domain routes return 404 and consumers idle.
- toggle-client SDK (Go/TS/Python identical semantics): `isEnabled(module) -> bool`, 5s local cache, fail-open=false (default disabled on error).

### 3.3 Event topics (Kafka), CloudEvents-ish JSON envelope
Envelope: `{ "id": uuid, "type": "<topic>", "source": "<service>", "time": rfc3339, "data": {...} }`
Topics: `telemetry.raw`, `telemetry.enriched`, `twin.updated`, `maintenance.predicted`,
`fuel.reading`, `safety.leak.detected`, `dispatch.job.assigned`, `drt.requested`,
`fare.payment.initiated`, `fare.payment.settled`, `carbon.credit.issued`, `energy.trade.executed`,
`toggle.changed`, `station.status.changed`

### 3.4 Core Postgres schemas (per-domain schemas: `fleet`, `infra`, `citizen`, `commerce`)
- `fleet.vehicles(id uuid pk, fleet_no text unique, vin text, model text, h2_capacity_kg numeric, status text, geom geometry(Point,4326))`
- `fleet.telemetry(bus_id uuid, ts timestamptz, speed_kph numeric, h2_level_pct numeric, fuel_cell_kw numeric, battery_soc_pct numeric, odometer_km numeric, geom geometry(Point,4326))` (TimescaleDB hypertable)
- `fleet.maintenance_predictions(id uuid pk, bus_id uuid, component text, risk_score numeric, predicted_failure_at timestamptz, model_version text, created_at timestamptz)`
- `infra.stations(id uuid pk, name text, capacity_kg numeric, available_kg numeric, status text, geom geometry(Point,4326))`
- `infra.incidents(id uuid pk, type text, severity text, bus_id uuid null, station_id uuid null, status text, opened_at timestamptz, meta jsonb)`
- `citizen.drt_requests(id uuid pk, user_sub text, pickup geometry(Point,4326), dropoff geometry(Point,4326), status text, requested_at timestamptz)`
- `citizen.carbon_credits(id uuid pk, period text, kg_co2_avoided numeric, credits numeric, issued_at timestamptz)`
- `commerce.fare_payments(id uuid pk, rider_sub text, amount_minor bigint, currency text, mojaloop_transfer_id text null, status text, created_at timestamptz)` — TigerBeetle holds double-entry ledger (accounts: RIDER_WALLET=1xxx, OPERATOR_REVENUE=2xxx, ENERGY_TRADE=3xxx, CARBON_FUND=4xxx)
- `commerce.trades(id uuid pk, kind text, quantity_kg numeric, price_minor bigint, status text, created_at timestamptz)`

### 3.5 Service standards
- Go: Go 1.22, `net/http` + chi router, Dapr sidecar optional (pubsub via Dapr or sarama direct), health `/healthz`, graceful shutdown, zap logging, env config `PORT`, `DATABASE_URL`, `KAFKA_BROKERS`, `REDIS_ADDR`, `TOGGLE_URL`, `KEYCLOAK_ISSUER`.
- Auth: Keycloak OIDC JWT (RS256) middleware on all mutating routes; Permify checks on admin routes (`permify grpc localhost:3476`).
- Rust: tokio + rdkafka, axum for any HTTP, `/healthz`.
- Python: FastAPI + uvicorn, pydantic v2, `/healthz`; ML services keep model artifacts in `models/` with a deterministic rule-based fallback so service runs without trained model.
- Every service: `Dockerfile` (multi-stage), `README.md` with run instructions, registers in APISIX routes config and docker-compose.

### 3.6 API routing (APISIX gateway, prefix → service)
`/api/toggles/*`→toggle:8080  `/api/fleet/*`→fleet-api:8081  `/api/infra/*`→infra-api:8082
`/api/citizen/*`→citizen-api:8083  `/api/commerce/*`→commerce-api:8084
`/api/ml/*`→predictive-maintenance:8090  `/api/optimize/*`→route-optimizer:8091  `/api/twin/*`→digital-twin:8092

### 3.7 PWA standards
React 18 + TS + Vite + Tailwind. Shell app with module registry: each of the 4 domains is a lazy-loaded
bundle; nav/route visibility driven by `GET /api/toggles`. Keycloak JS adapter login. Pages:
Dashboard (gov KPI), Live Fleet Map, Maintenance, Stations & Safety, Citizen/Arrivals, DRT,
Carbon, Commerce/Payments, Admin (toggle switches). Low-saturation warm palette, generous whitespace.
PWA: manifest + service worker (vite-plugin-pwa). Mobile: Expo skeleton reusing API clients.

### 3.8 Middleware usage map
Kafka (event backbone) · Dapr (pubsub/state abstraction for citizen+commerce services) ·
Fluvio (edge telemetry stream from bus gateways) · Temporal (dispatch + incident + settlement workflows) ·
Postgres/Timescale (OLTP + telemetry) · Keycloak (IAM) · Permify (ReBAC admin authz) ·
Redis (toggle cache, twin hot state, sessions) · Mojaloop (fare payment rails) ·
OpenSearch (open-data + telemetry search) · OpenAppSec (WAF on APISIX) · APISIX (gateway) ·
TigerBeetle (financial ledger) · Apache Sedona/Spark (geospatial ETL) · GeoLibre (map tiling/geodata) ·
Lakehouse (Iceberg on MinIO, analytics zone).

## 4. Quality bar
Compilable/plausible production-grade code, consistent contracts, no TODO stubs in core paths;
rule-based/simulated fallbacks allowed where hardware/ML data is unavailable; everything wired in
docker-compose with healthchecks; CI builds Go/Rust/Python/TS.
