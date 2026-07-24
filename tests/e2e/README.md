# H2Fleet — E2E smoke suite

`smoke.sh` is the black-box production-readiness check. It talks **only to the
APISIX gateway** (plus the Keycloak token endpoint) and validates the platform
contracts end to end.

## Run

```bash
make up-all     # full compose stack (middleware + apps profile)
make smoke      # -> tests/e2e/smoke.sh
```

The script waits up to `SMOKE_GATEWAY_TIMEOUT` (default 180s) for the gateway
to come healthy, so it can be launched right after `make up-all`.

## What it checks

| # | Check | Pass condition |
|---|-------|----------------|
| a | `GET /api/{toggles,fleet,infra,citizen,commerce,ml,optimize,twin}/healthz` | **200** — a 404 means the APISIX route is missing, not that the service is down |
| b | Toggle flip: `PUT /api/toggles/v1/toggles/fuel-monitoring {enabled:false}` → `GET /api/fleet/v1/fuel/levels` → flip back ON | 200/204 on PUT; route **404** while OFF (SPEC §3.2); route recovers to **200** within the 5s SDK cache TTL (polling up to `SMOKE_FLIP_TIMEOUT`, default 30s) |
| c | `POST /api/commerce/v1/payments` with `Idempotency-Key` | **201** with `status=settled` + `tb_transfer_id`, **or 502 with an explicit error** (ledger/Mojaloop rails down) — never a fabricated success; replaying the same key returns the **same payment id** (no duplicate); a request **without** the header is **400** |
| d | `GET /api/fleet/v1/telemetry/latest` after the simulator ran (`SMOKE_SIM_WAIT`, default 30s, skipped when rows already exist) | 200 JSON array with **> 0** bus samples |
| e | `GET /api/twin/v1/twin` | **200** JSON array |

Every check prints `[PASS]`/`[FAIL]`; the run ends with a
`SMOKE SUMMARY: X passed, Y failed` line and exits **non-zero** if any check
failed.

## Configuration (env)

| Variable | Default | Purpose |
|----------|---------|---------|
| `GATEWAY` | `http://localhost:9080` | APISIX gateway base URL |
| `KEYCLOAK_URL` / `KEYCLOAK_REALM` | `http://localhost:8088` / `h2fleet` | token endpoint |
| `SMOKE_ADMIN_USER` / `SMOKE_ADMIN_PASSWORD` | `admin` / `admin123` | realm user with `platform-admin` role (password grant) |
| `SMOKE_CLIENT_ID` / `KEYCLOAK_SERVICES_CLIENT_SECRET` | `services` / `h2fleet-services-secret-change-me` | confidential client with direct access grants |
| `SMOKE_TOGGLE_MODULE` / `SMOKE_TOGGLE_ROUTE` | `fuel-monitoring` / `/api/fleet/v1/fuel/levels` | module/route pair for the flip test |
| `SMOKE_GATEWAY_TIMEOUT` | `180` | startup patience (s) |
| `SMOKE_FLIP_TIMEOUT` | `30` | toggle propagation patience (s) |
| `SMOKE_SIM_WAIT` | `30` | simulator warm-up before check (d) |

## CI

`infra/ci/workflow.yml` has an optional `compose-smoke` job that brings the
whole stack up on a runner and executes this script. It is intentionally
**manual-only** (`workflow_dispatch`) — it builds ~15 images and takes tens of
minutes.

## Notes

- The toggle flip mutates deployment state briefly; do not run against an
  environment where a few seconds of `fuel-monitoring` downtime matters.
- If the payment check reports 502-with-error as PASS, that reflects the
  documented degraded mode (TigerBeetle/Mojaloop unreachable) — the smoke
  asserts honesty (no fabricated settlement), not rail availability.
- **Rust integration tests**: the telemetry-ingest → TimescaleDB pipeline and
  digital-twin `apply_update`/`snapshot_once` paths need live Kafka, Redis and
  Postgres. Those integration tests are intentionally **not** part of
  `cargo test` (unit tests cover the pure logic: validation, envelope parsing,
  status derivation, staleness partitioning). Run them manually against this
  compose stack when changing those paths.
