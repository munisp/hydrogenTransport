# H2Fleet — Top-10 Stakeholder Scenarios

End-to-end workflows that exercise the platform the way real stakeholders do.
Each scenario is a self-contained bash script in `tests/e2e/scenarios/` that
drives the public API surface through the APISIX gateway, plus a
machine-readable `manifest.yaml` that maps every step to an OpenAPI operation
so CI can validate coverage **without** a live stack.

## The 10 scenarios

| # | Scenario | Script | Personas / services exercised |
|---|----------|--------|-------------------------------|
| S1 | Morning-rush telemetry surge | `s01_morning_rush_telemetry_surge.sh` | fleet-api, digital-twin (Rust) — 50 buses × burst telemetry, twin freshness |
| S2 | Predictive maintenance → work order → depot dispatch | `s02_predictive_maintenance_work_order.sh` | fleet-api, predictive-maintenance (ML), infra-api depot + dispatch |
| S3 | H₂ leak → incident → escalation → compliance report | `s03_leak_incident_compliance.sh` | infra-api safety/incidents/compliance |
| S4 | Citizen signup → journey → DRT request → driver accept | `s04_citizen_drt.sh` | admin-api onboarding, citizen-api DRT, infra-api dispatch |
| S5 | Fare payment (idempotent) → loyalty accrual → marketplace redeem | `s05_fare_loyalty.sh` | commerce-api payments/loyalty/marketplace (TigerBeetle ledger) |
| S6 | Carbon period close → credits → government dashboard | `s06_carbon_close_gov.sh` | carbon-analytics (internal), citizen-api credits, commerce-api gov KPIs |
| S7 | Admin toggles advertising OFF → PWA nav reacts → ON | `s07_toggle_advertising.sh` | admin-api toggle proxy, toggle-service, commerce-api ads |
| S8 | NOC wallboard → alert fires → runbook restart → green | `s08_noc_wallboard_restart.sh` | admin-api health sweep + alerts proxy (Alertmanager) |
| S9 | Advertiser campaign → revenue KPI | `s09_advertiser_campaign.sh` | commerce-api ads, admin-api KPI aggregation |
| S10 | Energy-surplus trade → ledger balanced | `s10_energy_trade.sh` | commerce-api energy trades + gov KPIs (TigerBeetle double-entry) |

## Running

```bash
# Static validation (no stack required) — CI runs this:
make validate-scenarios

# Live execution against a running stack (make up-all first):
make scenarios          # = tests/e2e/scenarios/run_all.sh
```

`run_all.sh` executes S1–S10 sequentially, prints a per-scenario PASS/FAIL
summary, and exits non-zero on the first failed expectation when
`STOP_ON_FAIL=1`. Scripts source `lib.sh` (token helpers per persona, `req`
wrapper with retries, `expect_status`, `wait_json` polling).

## How validation works

`validate_scenarios.py` checks, for every step in `manifest.yaml`:

1. the referenced service has a committed `openapi.yaml`;
2. `(method, path)` exists as an operation in that spec (path templates
   normalized, so `/v1/x/{id}` matches `/v1/x/{anything}`);
3. the step's `body` satisfies the operation's `requestBody` schema — all
   `required` properties present, unknown properties flagged (`$ref`/`allOf`
   resolved locally);
4. **drift guard** — the step's path literally appears in the scenario's
   `s*.sh` script, so scripts and manifest cannot silently diverge;
5. every `s*.sh` on disk is covered by a manifest entry.

Current status: **64 checks green** (10 scenarios, 43 steps).

## Scale notes

- S1 is the load path: telemetry ingest is Kafka-first (franz-go consumer
  group, batch upsert with `UNIQUE(bus_id, ts)` dedup on the TimescaleDB
  hypertable). Horizontal scale = more telemetry-ingest replicas + Kafka
  partitions; see `docs/MIDDLEWARE_HARDENING.md` for the throughput math and
  `infra/prod/` for the HA overlays (3-broker Kafka, PG replica, Redis
  Sentinel).
- S5/S10 are the money paths: TigerBeetle enforces double-entry invariants;
  idempotency keys map to deterministic transfer IDs, so retried POSTs are
  safe at any concurrency. Mojaloop settlement rails are async and
  back-pressured via Kafka — see `docs/MOJALOOP.md`.
- S8 assumes Prometheus/Alertmanager are up (default profile); the wallboard
  degrades gracefully (per-service `degraded` flags) when a dependency is
  down — that degradation is part of the scenario assertion.
