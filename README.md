# H2Fleet — Unified Hydrogen Bus Fleet Platform

One platform for a city's fleet of **50 hydrogen buses**. Instead of building
20 separate apps, H2Fleet implements **20 capability modules in 4 domains** on a
single shared platform — one gateway, one identity plane, one event backbone,
one data platform — where every module can be toggled ON/OFF per deployment.

## The 20 ideas, 4 domains

**Fleet Operations & Telematics (`fleet`)**
real-time telematics & live map · predictive maintenance (ML) · per-bus digital
twin (Rust hot path) · H2 fuel monitoring & range · route/energy optimizer

**Infrastructure & Safety (`infra`)**
refueling station mgmt · H2 leak detection & incident workflow · driver
dispatch (Temporal) · compliance reporting · depot management

**Citizen & Engagement (`citizen`)**
passenger PWA · Expo mobile apps · demand-responsive transit (DRT) · carbon
credits · open data portal (GTFS/GTFS-RT + OpenSearch)

**Commerce & Finance (`commerce`)**
fare payments (Mojaloop + TigerBeetle ledger) · loyalty marketplace ·
energy/H2 trading · government KPI dashboard · advertising

## Why one unified platform beats 20 individual builds

* **Shared auth** — one Keycloak realm + Permify ReBAC instead of 20 login
  systems; roles (`platform-admin`, `operator`, `driver`, `citizen`) work
  everywhere.
* **Shared events** — one Kafka backbone with a fixed topic catalog
  (`telemetry.raw` → `telemetry.enriched` → `twin.updated` →
  `maintenance.predicted` …); modules compose through events, not point-to-point
  integrations.
* **Shared toggles** — one control plane flips any module per deployment:
  routes 404, UI entries disappear, consumers/workflows don't start.
* **Shared data** — one Postgres/Timescale (per-domain schemas), one Redis,
  one TigerBeetle ledger, one lakehouse (Iceberg on MinIO) — cross-domain
  questions ("emissions saved vs. fare revenue per route") are joins, not ETL
  projects.

## Architecture

```
                        ┌────────────── PWA (:3000) ──────────────┐
                        │ 4 domain bundles, toggle-driven registry │
                        └──────────────────┬───────────────────────┘
                                           ▼
   OpenAppSec WAF ▷ ┌──────────── APISIX gateway (:9080) ───────────┐
   (profile waf)    │ /api/toggles /fleet /infra /citizen /commerce  │   OIDC (Keycloak)
                    │                       /ml /optimize /twin      │ ◄── realm h2fleet
                    └──┬─────┬─────┬─────┬──────┬─────┬──────┬──────┘
                       ▼     ▼     ▼     ▼      ▼     ▼      ▼
                  toggle  fleet infra citizen comm.  ML   optim.  twin
                  :8080   :8081 :8082 :8083  :8084  :8090 :8091  :8092
                    │  (Go)            (Go, Dapr)  (Python)      (Rust)
   buses ──(Fluvio)─▶ Kafka: telemetry.raw ─▶ telemetry-ingest (Rust) ─▶ Timescale hypertable
                                     └─▶ telemetry.enriched ─▶ digital-twin ─▶ Redis hot + twin.updated
        ┌────────────┬─────────────┬───────────────┬────────────┬─────────────┐
        ▼            ▼             ▼               ▼            ▼             ▼
   Postgres/    Temporal      Mojaloop sim    TigerBeetle   OpenSearch    MinIO+Iceberg
   Timescale    workflows     (fare rails)    ledger        open data     lakehouse ◄─ Spark/Sedona ETL
        ▲                                                             ▲
        └────────── Permify (ReBAC) · Redis (toggle cache/twin) ──────┘
```

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) ·
[docs/MODULES.md](docs/MODULES.md) · [docs/API.md](docs/API.md) ·
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)

## Quickstart

```bash
make up       # middleware stack (Kafka, Temporal, PG/Timescale, Keycloak, APISIX, …)
make up-all   # + build/run all 9 services and the PWA
make gateway-check
```

* Gateway: http://localhost:9080 · PWA: http://localhost:3000
* Login: `admin/admin123` (platform-admin), `operator/operator123`,
  `driver/driver123`, `citizen/citizen123`
* Flip a module: Admin page in the PWA, or
  `curl -X PUT http://localhost:9080/api/toggles/v1/toggles/advertising \
   -H "Authorization: Bearer $TOKEN" -d '{"enabled":false}'`

## Repo map

```
/services
  go/      toggle-service · fleet-api · infra-api · citizen-api · commerce-api
  rust/    telemetry-ingest · digital-twin
  python/  predictive-maintenance · route-optimizer · carbon-analytics · lakehouse-etl
/apps
  pwa/     React 18 + TS + Vite PWA (4 domain dashboards + citizen app)
  mobile/  Expo skeleton (citizen + driver)
/infra
  docker-compose.yml (profiles: default middleware, apps, all, fluvio, etl, waf)
  k8s/     kustomize base + overlays/dev (per-deployment domain scoping)
  dapr/    pubsub-kafka, statestore-redis, cron bindings
  apisix/  gateway routes (SPEC §3.6)  keycloak/ realm  permify/ schema  sql/ init+seed
/packages
  events/  AsyncAPI event catalog + JSON schemas
  toggle-client/  toggle SDK (Go/TS/Python, same contract)
/docs      ARCHITECTURE · MODULES · API · DEPLOYMENT
Makefile · SPEC.md · .github/workflows/ci.yml
```

## Language choices

| Language | Where | Why |
|---|---|---|
| **Go 1.22** | 5 API/control services | Fast cold-start REST services, first-class concurrency for fan-out to Kafka/Redis/PG, chi + net/http keeps the control plane boring and reviewable |
| **Rust** | telemetry-ingest, digital-twin | The hot path: sustained high-rate Kafka consumption and per-bus state updates where predictable latency and zero-GC pauses matter (tokio + rdkafka) |
| **Python 3.12** | ML, optimization, analytics, ETL | The ecosystem is the feature: scikit-learn/ONNX, OR-Tools, PySpark + Apache Sedona. FastAPI keeps the API contract identical to the Go services |
| **TypeScript** | PWA, mobile | Type-safe API clients against the same OpenAPI/AsyncAPI contracts; React 18 + Vite for the dashboards, Expo for mobile reuse |

All contracts (module ids, toggle API, topics, schemas, ports, gateway map) are
pinned in [SPEC.md](SPEC.md) — the single source of truth.
