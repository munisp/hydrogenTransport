# infra-api

Domain 2 API — Infrastructure & Safety (SPEC §3.4 `infra` schema). Port **8082**
(gateway prefix `/api/infra/*`). Each route group is gated behind its module toggle;
a disabled module returns **404** (fail-closed).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| GET   | `/v1/stations` | `refueling-stations` | — |
| GET   | `/v1/stations/{id}` | `refueling-stations` | — |
| POST  | `/v1/stations` | `refueling-stations` | JWT |
| PATCH | `/v1/stations/{id}/status` → publishes `station.status.changed` | `refueling-stations` | JWT |
| GET   | `/v1/stations/{id}/queue` — active entries with position + estimated wait | `refueling-stations` | — |
| POST  | `/v1/stations/{id}/queue` — join (waiting, or serving immediately when empty) | `refueling-stations` | JWT |
| POST  | `/v1/stations/{id}/queue/{entry}/complete` — `{dispensed_kg}` decrements station `available_kg`; promotes next waiting bus | `refueling-stations` | JWT (operator) |
| POST  | `/v1/stations/{id}/queue/{entry}/leave` — waiting/serving → left; promotes next waiting bus | `refueling-stations` | JWT |
| GET   | `/v1/incidents?status=` | `leak-detection` | — |
| POST  | `/v1/incidents` | `leak-detection` | JWT |
| POST  | `/v1/incidents/{id}/ack` | `leak-detection` | JWT |
| POST  | `/v1/incidents/{id}/resolve` | `leak-detection` | JWT |
| POST  | `/v1/safety/leak` — sensor webhook: opens incident, publishes `safety.leak.detected`, signals Temporal workflow `incident-{id}` | `leak-detection` | `X-Sensor-Token` (when `LEAK_INGEST_TOKEN` set) or JWT |
| GET   | `/v1/dispatch/jobs?status=&driver_sub=` | `dispatch-workforce` | — |
| POST  | `/v1/dispatch/jobs` (`starts_at`/`ends_at` window, overlap → 409) → publishes `dispatch.job.assigned`, signals workflow `dispatch-{id}` | `dispatch-workforce` | JWT (operator) |
| POST  | `/v1/dispatch/jobs/{id}/accept` | `dispatch-workforce` | JWT (driver) |
| POST  | `/v1/dispatch/jobs/{id}/cancel` → signals `job-cancelled` to the workflow | `dispatch-workforce` | JWT (operator) |
| GET   | `/v1/compliance/reports`, `/v1/compliance/reports/{id}` | `compliance-reporting` | — |
| POST  | `/v1/compliance/reports/generate?days=` (default 30, 1..365; sections: incidents by status/severity, unresolved leaks, MTTR, maintenance predictions, open work orders, fleet availability, station inventory; scheduled via `COMPLIANCE_REPORT_INTERVAL`) | `compliance-reporting` | JWT |
| GET   | `/v1/depot/bays` | `depot-management` | — |
| POST  | `/v1/depot/bays/{id}/assign`, `/v1/depot/bays/{id}/release` | `depot-management` | JWT (operator) |
| GET   | `/v1/depot/work-orders?status=` | `depot-management` | — |
| POST  | `/v1/depot/work-orders`, `/v1/depot/work-orders/{id}/assign`, `.../start`, `.../hold`, `.../close` (lifecycle: open → assigned → in_progress ↔ on_hold → closed) | `depot-management` | JWT (operator) |
| GET   | `/healthz` | — | — |

Supplemental tables (`infra.compliance_reports`, `infra.work_orders`,
`infra.dispatch_jobs`) are created idempotently at startup; core tables come
from `infra/sql` migrations.

## Configuration (env, SPEC §3.5)

| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8082` | |
| `DATABASE_URL` | — | required |
| `TOGGLE_URL` | — | fail-closed when unset |
| `KAFKA_BROKERS` | — | comma-separated; no-op logging publisher when unset |
| `KEYCLOAK_ISSUER` | — | in-network realm URL; `KEYCLOAK_ISSUER_ALT` (default `http://localhost:8088/realms/h2fleet`) also accepted as `iss` |
| `TEMPORAL_HOST` | — | e.g. `temporal:7233`; no-op signaler when unset |
| `LEAK_INGEST_TOKEN` | — | shared sensor token for the leak webhook |
| `COMPLIANCE_REPORT_INTERVAL` | — | e.g. `24h`; when parseable, compliance reports are also generated on this schedule (manual POST always works) |

## Run

```sh
go run ./cmd/server
docker build -f services/go/infra-api/Dockerfile -t h2fleet/infra-api .   # context = repo root
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
