# H2Fleet Deployment

## 1. Local quickstart (docker compose)

Prereqs: Docker Engine 24+ with the compose plugin, ~8 GB RAM free.

```bash
cp .env.example .env   # optional: override dev secrets (docs/SECRETS.md)
make up          # middleware only: Kafka, Temporal, Postgres/Timescale, Keycloak,
                 # Permify, Redis, OpenSearch, APISIX (standalone), Mojaloop simulator,
                 # TigerBeetle, MinIO+Iceberg, Spark, Prometheus+Grafana, backup
make up-all      # + build & run the platform services, telemetry simulator and PWA (profile apps)
make gateway-check   # smoke-test /api/<domain>/healthz through APISIX :9080
make simulate    # (re)build + start the telemetry simulator (profile apps)
make observability   # print/open Grafana, Prometheus, Alertmanager URLs
make backup      # one-off backup of both Postgres DBs + TigerBeetle -> MinIO
make logs S=fleet-api
make down
```

First boot runs `infra/sql/001_init.sql` then `002_seed.sql` (idempotent;
re-apply later with `make seed`). Forward schema changes are goose migrations
in `infra/sql/migrations` (see `infra/sql/migrations/README.md`), applied by
the one-shot `migrator` compose service — app services wait for it, and you
can re-run it any time with `make migrate`.

| Entrypoint | URL | Notes |
|---|---|---|
| API gateway (APISIX) | http://localhost:9080 | SPEC §3.6 prefix map |
| PWA | http://localhost:3000 | login with a test user below |
| Keycloak | http://localhost:8088 (and :8180) | admin/admin (default; `KEYCLOAK_ADMIN_PASSWORD` in .env), realm `h2fleet`; :8088 matches the PWA build-time default |
| Grafana | http://localhost:3001 | admin/admin (default; `GRAFANA_ADMIN_PASSWORD` in .env); H2Fleet dashboards auto-provisioned |
| Prometheus | http://127.0.0.1:9090 | targets/alerts; service /metrics handlers pending (see `infra/observability/prometheus.yml`) |
| Alertmanager | http://127.0.0.1:9093 | null receiver in dev |
| Temporal UI | http://127.0.0.1:8233 | |
| OpenSearch Dashboards | http://127.0.0.1:5601 | security plugin disabled (dev) |
| MinIO console | http://127.0.0.1:9001 | h2admin / h2adminpass (defaults; .env) |
| Spark master UI | http://localhost:8280 | submit at `spark://localhost:7077` |
| APISIX admin | **not host-published** | in-network only: `docker exec h2-apisix curl -H "X-API-KEY: $APISIX_ADMIN_KEY" localhost:9180/...` |

Dev middleware ports (5432, 6379, 9200, 5601, 9000, 9001, 7233, 8233, 3476,
3478, 9090, 9093, 3001) bind to **127.0.0.1** only.

**Test users** (realm `h2fleet`, template `infra/keycloak/realm-h2fleet.json`;
passwords below are the `.env` defaults — the realm is rendered with your
`.env` values by the `keycloak-realm-init` container at import time):

| User | Default password | Roles |
|---|---|---|
| admin | admin123 | platform-admin, operator |
| operator | operator123 | operator |
| driver | driver123 | driver |
| citizen | citizen123 | citizen |

> **Issuer note (dev):** Keycloak issues tokens with `iss` equal to the URL the
> client used — `http://localhost:8088/realms/h2fleet` for browser flows,
> `http://keycloak:8080/realms/h2fleet` for in-network clients. Services fetch
> JWKS via the in-network `KEYCLOAK_ISSUER`; strict single-issuer validation
> should therefore accept both issuers (see services' `auth` package).

Get a token without the PWA:

```bash
curl -s http://localhost:8088/realms/h2fleet/protocol/openid-connect/token \
  -d client_id=services -d client_secret="${KEYCLOAK_SERVICES_CLIENT_SECRET:-h2fleet-services-secret-change-me}" \
  -d grant_type=password -d username=admin -d password="${KEYCLOAK_ADMIN_USER_PASSWORD:-admin123}" | jq -r .access_token
```

**Payments without TigerBeetle:** commerce-api refuses to start when
`TIGERBEETLE_ADDR` is unset (fail-closed money path) unless you explicitly
opt into the in-memory dev ledger with `H2_SIMULATED_LEDGER=true`. The
simulated ledger rejects negative balances, and payment routes return
`502 bad_gateway` (+ `fare.payment.failed` event) when the ledger or Mojaloop
simulator is unreachable — that is expected behaviour, not a bug. Bring
`tigerbeetle`/`mojaloop-simulator` up (they are in the default middleware
profile) before testing payments. The Mojaloop leg similarly fails closed
(`mojaloop_unavailable`) when `MOJALOOP_ENDPOINT` is unset, unless
`H2_SIMULATED_MOJALOOP=true` is set (DEV ONLY).

**Compose profiles**: default = middleware. `--profile apps` adds platform
services, `--profile all` everything, `--profile fluvio` the optional edge
streamer, `--profile etl` the one-shot Sedona/Spark ETL runner
(`make etl`), `--profile waf` the OpenAppSec nano-agent sidecar.

**Mojaloop**: local dev uses `mojaloop/simulator` (FSPIOP quotes/transfers at
`:8444`). A real switch deploys via the official Helm charts
(<https://github.com/mojaloop/helm>) — not part of compose by design.

**GeoLibre**: no published container image; build manually from
<https://github.com/opengeos/GeoLibre> and uncomment the service block in
`infra/docker-compose.yml`. The PWA falls back to public OSM tiles meanwhile.

## 2. Kubernetes (kustomize)

```bash
kubectl apply -k infra/k8s/overlays/dev   # dev / city-pilot
kubectl apply -k infra/k8s/base           # full
```

Middleware is installed via upstream Helm charts/operators into the cluster
(service DNS names in the `h2fleet-config` ConfigMap). See
`infra/k8s/README.md` for the per-deployment toggling model.

## 3. Deployment profiles (per-deployment domain scoping)

A "profile" = which of the 4 domains are *present* in a deployment. Controlled
by `TOGGLE_DOMAINS` (k8s ConfigMap) — toggle-service seeds/enables only those
modules; everything else 404s and never starts consumers/workflows.

| Profile | TOGGLE_DOMAINS | What runs | Use case |
|---|---|---|---|
| `city-pilot` | `fleet,infra` | telematics, twin, fuel, maintenance, optimizer, stations, leak, dispatch, compliance, depot | Pilot city: operations + safety only, no citizen/commerce surface |
| `ops+citizen` | `fleet,infra,citizen` | above + PWA/DRT/carbon/open-data | Public launch before payments |
| `full` | `all` | all 20 modules | Production |

Runtime flips of individual modules inside a deployed profile use the toggle
API (`PUT /api/toggles/v1/toggles/{module}` as `platform-admin`) — no redeploy.

## 4. Observability & backups

* Prometheus + Alertmanager + Grafana run in the default compose profile
  (config in `infra/observability/`). Dashboards `platform-overview` and
  `fleet` are auto-provisioned; per-service scrape jobs are pre-configured
  and light up as services ship their `/metrics` handlers (pending
  follow-up work). Alerts: `infra/observability/alerts.yml`.
* The `backup` service loops `pg_dump` of both Postgres instances plus a
  crash-consistent TigerBeetle file snapshot into MinIO `h2-backups`
  (`make backup` for a one-off run). Restore drill: `docs/DR.md`.
* Failure-mode playbooks: `docs/RUNBOOK.md`; targets: `docs/SLO.md`;
  H2 leak alarm path: `docs/INCIDENT_RESPONSE.md`.

## 5. CI

`.github/workflows/ci.yml` (shipped as `infra/ci/workflow.yml` — see note
inside): per-service Go build/vet (+test); toggle-client SDK tests
(Go/Python/TypeScript); `cargo check` for Rust (`--locked` when a Cargo.lock
is committed); pip install + compileall (+pytest) for Python incl. the
telemetry simulator; event-schema validation of `packages/events/fixtures/*`
against `packages/events/schemas/*` (`infra/ci/validate_events.py`); gosec;
gitleaks; trivy fs (non-blocking); per-service `docker build` (no push); PWA
`npm ci && tsc --noEmit && vite build`; docker-compose config validation
across all profiles. CI intentionally does not push images — see
`make push-note`.

## 6. Production hardening checklist

- Set every variable in `.env.example` to a real secret (all compose
  credentials are `${VAR:-dev-default}`; the committed defaults are dev-only).
- Rotate: APISIX admin key, Keycloak admin + `services` client secret, MinIO
  root creds, Postgres passwords, `LEAK_INGEST_TOKEN` (see docs/SECRETS.md).
- Enable OpenSearch security plugin + TLS; enable Redis AUTH; TLS on Kafka.
- Permify already runs on its own Postgres database (permify-db-init +
  permify-migrate one-shots); in prod move it to a managed instance.
- Keycloak: `start --optimized` with Postgres instead of `start-dev`/dev-file.
- Restrict APISIX `allow_admin` CIDR; put OpenAppSec in `prevent` mode.
- Replace Mojaloop simulator with the real switch (mojaloop/helm).
- Replace k8s `base/secret.yaml` with an external secrets source.


> **Note — JS lockfiles:** `package-lock.json` files for `apps/pwa`, `apps/mobile` and
> `packages/toggle-client/ts` are intentionally not committed (generated artifacts).
> Regenerate with `npm install --package-lock-only` in each directory; builds and CI
> fall back to `npm install` when no lockfile is present.
