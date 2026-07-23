# H2Fleet Deployment

## 1. Local quickstart (docker compose)

Prereqs: Docker Engine 24+ with the compose plugin, ~8 GB RAM free.

```bash
make up          # middleware only: Kafka, Temporal, Postgres/Timescale, Keycloak,
                 # Permify, Redis, OpenSearch, APISIX+etcd, Mojaloop simulator,
                 # TigerBeetle, MinIO+Iceberg, Spark
make up-all      # + build & run the 9 platform services and the PWA (profile apps)
make gateway-check   # smoke-test /api/<domain>/healthz through APISIX :9080
make logs S=fleet-api
make down
```

First boot runs `infra/sql/001_init.sql` then `002_seed.sql` (idempotent;
re-apply later with `make seed`).

| Entrypoint | URL | Notes |
|---|---|---|
| API gateway (APISIX) | http://localhost:9080 | SPEC §3.6 prefix map |
| PWA | http://localhost:3000 | login with a test user below |
| Keycloak | http://localhost:8088 (and :8180) | admin/admin, realm `h2fleet`; :8088 matches the PWA build-time default |
| Temporal UI | http://localhost:8233 | |
| OpenSearch Dashboards | http://localhost:5601 | security plugin disabled (dev) |
| MinIO console | http://localhost:9001 | h2admin / h2adminpass |
| Spark master UI | http://localhost:8280 | submit at `spark://localhost:7077` |
| APISIX admin | http://localhost:9180 | key `h2fleet-admin-key-change-me` |

**Test users** (realm `h2fleet`, see `infra/keycloak/realm-h2fleet.json`):

| User | Password | Roles |
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
  -d client_id=services -d client_secret=h2fleet-services-secret-change-me \
  -d grant_type=password -d username=admin -d password=admin123 | jq -r .access_token
```

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

## 4. CI

`.github/workflows/ci.yml` (shipped as `infra/ci/workflow.yml` — see note inside): per-service Go build/vet (+test), `cargo check` for
Rust, pip install + compileall (+pytest when tests exist) for Python, PWA
`npm ci && tsc --noEmit && vite build`, and docker-compose config validation
across all profiles.

## 5. Production hardening checklist

- Rotate: APISIX admin key, Keycloak admin + `services` client secret, MinIO
  root creds, Postgres password.
- Enable OpenSearch security plugin + TLS; enable Redis AUTH; TLS on Kafka.
- Permify: switch from `memory` to postgres engine.
- Keycloak: `start --optimized` with Postgres instead of `start-dev`/dev-file.
- Restrict APISIX `allow_admin` CIDR; put OpenAppSec in `prevent` mode.
- Replace Mojaloop simulator with the real switch (mojaloop/helm).
