# H2Fleet Architecture

Unified, module-toggleable platform for a 50-bus hydrogen fleet. 20 capability
modules in 4 domains share one gateway, one event backbone, one identity plane
and one data platform — instead of 20 siloed builds.

## 1. Middleware map (SPEC §3.8)

| Middleware | Role in H2Fleet | Where it lives (local) |
|---|---|---|
| **Apache Kafka** | Event backbone; all domain events (SPEC §3.3 topics) | `kafka:9092` (host `localhost:9094`) |
| **Dapr** | Pub/sub + state abstraction for citizen & commerce services; cron bindings | components in `infra/dapr/components/` |
| **Fluvio** | Optional edge telemetry stream from bus gateways (bridged to `telemetry.raw`) | compose profile `fluvio` |
| **Temporal** | Long-running workflows: dispatch, incident response, fare settlement | `temporal:7233`, UI `:8233` |
| **Postgres 16 + TimescaleDB + PostGIS** | OLTP for all 4 domain schemas; telemetry hypertable | `postgres:5432` |
| **Keycloak** | IAM — OIDC RS256 JWTs, realm `h2fleet` | `localhost:8180` |
| **Permify** | ReBAC authorization on admin routes | gRPC `:3476`, HTTP `:3478` |
| **Redis** | Toggle cache (`toggles:<module>`, 30 s TTL), twin hot state, sessions | `:6379` |
| **Mojaloop** | Fare payment rails (FSPIOP). Local dev: `mojaloop/simulator` | `:8444` (prod: mojaloop/helm on k8s) |
| **OpenSearch** | Open-data portal datasets + telemetry search | `:9200`, dashboards `:5601` |
| **OpenAppSec** | WAF on the APISIX gateway | compose profile `waf` (nano-agent sidecar) |
| **APISIX** | API gateway — single entrypoint, prefix routing, OIDC/key-auth | `:9080` (admin `:9180`) |
| **TigerBeetle** | Double-entry financial ledger (fares, trades, credits) | `:3000` |
| **Apache Sedona / Spark** | Geospatial ETL into the lakehouse | master UI `:8280`, submit `:7077` |
| **GeoLibre** | Map tiling / geodata server | manual build, see compose note (optional) |
| **Lakehouse** | Iceberg REST catalog on MinIO (`s3://h2-lakehouse`) | MinIO `:9000/:9001`, catalog `:8181` |

## 2. Data flow: telemetry → twin → ML → lakehouse

```
Bus gateways ──(Fluvio, edge, optional)──▶ Kafka topic telemetry.raw
                                                    │
                              telemetry-ingest (Rust, rdkafka)
                                    ┌───────────────┴────────────────┐
                                    ▼                                ▼
                    fleet.telemetry (Timescale hypertable)   telemetry.enriched (Kafka)
                                                                 │
                                              digital-twin (Rust, axum)
                                    ┌───────────────┬──────────────┴───────────┐
                                    ▼               ▼                          ▼
                          Redis hot state   twin.updated (Kafka)   fleet.twin_snapshots (PG)
                                    │               │
                       fleet-api reads for          ├─▶ predictive-maintenance (FastAPI)
                       /api/fleet live map          │     risk scores → fleet.maintenance_predictions
                                                    │     → maintenance.predicted (Kafka)
                                                    └─▶ fuel-monitoring views, fuel.reading (Kafka)

Nightly/scheduled: lakehouse-etl (Spark + Apache Sedona)
  Postgres (all schemas) ──geospatial ETL──▶ Iceberg tables (REST catalog) on MinIO
                                            ──▶ OpenSearch indexes (open-data portal)
                                            ──▶ gov-dashboard KPIs
```

* **telemetry-ingest** consumes `telemetry.raw`, validates/enriches, batch-inserts
  into the `fleet.telemetry` hypertable and republishes `telemetry.enriched`.
  H2-relevant financial micro-events (e.g. fuel purchases) are written to
  TigerBeetle as ledger entries in the same pass.
* **digital-twin** consumes `telemetry.enriched`, keeps the latest per-bus state
  in Redis (hot path, read by `/api/twin/*`), emits `twin.updated` and
  periodically snapshots into `fleet.twin_snapshots`.
* **predictive-maintenance** scores components (fuel-cell/battery/H2 system) from
  recent telemetry; ML model with deterministic rule-based fallback
  (SPEC §3.5). Predictions land in `fleet.maintenance_predictions` and on
  `maintenance.predicted`.
* **lakehouse-etl** is a Spark/Sedona job that moves curated, geospatially
  indexed copies of operational data into Iceberg tables on MinIO — the
  analytics zone feeding gov-dashboard and the open-data portal.

## 3. Toggle architecture

Every one of the 20 modules can be ON/OFF per deployment. There is **one**
control plane and every runtime plane derives from it.

**Storage & control plane**
```
Admin PWA ──PUT /api/toggles/v1/toggles/{module}──▶ APISIX ─▶ toggle-service (Go)
                                                                 │
                    ┌───────────────────────┬────────────────────┼─────────────────────┐
                    ▼                       ▼                    ▼                     ▼
        public.feature_toggles (PG)   Redis toggles:<m>    Kafka toggle.changed   Permify check
        source of truth,              cache TTL 30 s       broadcast to all       (admin routes)
        updated_at trigger
```

**Sequence — flipping a module OFF (e.g. `advertising`)**

1. `platform-admin` flips the switch in the Admin page of the PWA.
2. PWA calls `PUT /api/toggles/v1/toggles/advertising {enabled:false}` with its
   Keycloak token. APISIX validates the JWT (openid-connect) and strips the
   prefix; toggle-service checks the `platform-admin` realm role and Permify
   `module:advertising#manage`.
3. toggle-service updates `public.feature_toggles`, sets
   `toggles:advertising=false` in Redis (TTL 30 s) and publishes a
   CloudEvents-ish envelope to `toggle.changed`.
4. Every service's toggle-client SDK sees the change (Kafka subscription and/or
   5 s local cache expiry → re-fetch `GET /v1/toggles/{module}`).
5. Consequences within ~seconds:
   - **APIs**: commerce-api's advertising routes return `404 module disabled`.
   - **Consumers/workflows**: advertising consumers and Temporal activities are
     not started / idle out.
   - **Dapr**: components scoped to the module are not loaded.
   - **PWA**: `GET /api/toggles` drives the module registry — the Advertising
     nav entry and lazy bundle disappear on next render.
6. Failure semantics are **fail-closed**: if toggle-service is unreachable, the
   SDK default is *disabled* (fail-open=false, SPEC §3.2).

**Deploy-time scoping** (`TOGGLE_DOMAINS`) decides which domains exist at all in
a deployment; runtime toggles flip modules within the deployed domains. See
`docs/DEPLOYMENT.md`.

## 4. Security model

```
        ┌──────────────────────────────────────────────────────────┐
        │ OpenAppSec WAF (profile waf) — L7 inspection on APISIX    │
        └───────────────────────────┬──────────────────────────────┘
                                    ▼
  APISIX :9080  ── openid-connect (Keycloak, RS256 JWT, bearer-only)
                ── key-auth for machine consumers (open-data demo)
                ── proxy-rewrite strips /api/<domain>
                                    ▼
  Services      ── Keycloak JWT middleware on all mutating routes (roles claim)
                ── Permify ReBAC checks on admin routes
                  (module/fleet/station/report/payment entities)
                ── Row ownership for citizen data via JWT `sub`
                                    ▼
  Data planes   ── Postgres per-domain schemas, least-privilege in prod
                ── TigerBeetle double-entry ledger (money never "updated", only transferred)
```

* **Identity**: Keycloak realm `h2fleet`; public PKCE client `pwa-public`
  (browser), confidential client `services` (gateway/service accounts).
  Realm roles: `platform-admin`, `operator`, `driver`, `citizen`.
* **AuthN**: JWTs verified at the gateway *and* in each service (defense in
  depth); services accept the same issuer (`KEYCLOAK_ISSUER`).
* **AuthZ**: coarse-grained via realm roles; fine-grained via Permify
  relationships (`infra/permify/schema.perm`) on admin routes — e.g. only
  `module:<id>#manage` may `PUT /v1/toggles/{module}`.
* **Ledger integrity**: all money movement is double-entry in TigerBeetle
  (account ranges: RIDER_WALLET 1xxx, OPERATOR_REVENUE 2xxx, ENERGY_TRADE
  3xxx, CARBON_FUND 4xxx); Postgres `commerce.*` tables are the query side.
* **Dev relaxations** (documented, not for prod): OpenSearch security plugin
  disabled, Permify in-memory engine, APISIX admin open to `0.0.0.0/0`,
  Keycloak dev-file storage, demo secrets.
