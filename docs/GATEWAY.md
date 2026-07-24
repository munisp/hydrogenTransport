# Gateway (APISIX) Operations Guide

APISIX is the single ingress for all `/api/*` traffic (SPEC §3.6). It runs in
**standalone mode**: route definitions are loaded from
`infra/apisix/apisix.yaml` (`deployment.role_traditional.config_provider: yaml`
in `infra/apisix/config.yaml`) — no etcd round-trip, the file is hot-reloaded.

| File | Purpose |
| --- | --- |
| `infra/apisix/config.yaml` | Node config: listen ports, admin lockdown, TLS block, prometheus exporter |
| `infra/apisix/apisix.yaml` | Routes, consumers, global rules (CORS / WAF / metrics) |

## Route map

| Prefix | Upstream | Auth at edge |
| --- | --- | --- |
| `/api/toggles/*` | toggle-service:8080 | OIDC bearer (pass-through) |
| `/api/fleet/*` | fleet-api:8081 | OIDC bearer (pass-through) |
| `/api/infra/*` | infra-api:8082 | OIDC bearer (pass-through) |
| `POST /api/infra/v1/safety/leak` | infra-api:8082 | OIDC pass-through + `limit-req` 10 r/s (burst 20) |
| `/api/citizen/*` | citizen-api:8083 | OIDC pass-through + `limit-count` 600/min |
| `/api/commerce/*` | commerce-api:8084 | OIDC bearer (pass-through) |
| `/api/ml/*` | predictive-maintenance:8090 | OIDC bearer (pass-through) |
| `/api/optimize/*` | route-optimizer:8091 | OIDC bearer (pass-through) |
| `/api/twin/*` | digital-twin:8092 | OIDC bearer (pass-through) |
| `/api/open-data/*` | citizen-api:8083 | `key-auth` (header `apikey`) + `limit-count` 600/min |

**carbon-analytics:8094 is internal-only.** It is deliberately *not* mapped to
any `/api/*` prefix and must never be exposed through the gateway; it is
reachable only by other services on the internal network. Do not add a route
for it.

## Authentication — defense-in-depth model

Every `/api/*` route (except key-auth open-data) runs the `openid-connect`
plugin with `bearer_only: true` **and `unauth_action: pass`**:

- Requests **without** a token are passed through instead of rejected with 401
  at the edge. This keeps public reads (arrivals, alerts, toggles) and
  `/healthz` probes working.
- Requests **with** a bearer token have it validated against Keycloak
  (realm `h2fleet`) at the gateway, and the token is forwarded upstream.
- Every backend service enforces its **own** JWT middleware on all mutating
  routes (Permify ReBAC on admin routes), so an unauthenticated request that
  slips through the gateway still gets 401/403 from the service.

The gateway is therefore an accelerator and token validator, not the sole
authorization boundary. The machine-to-machine open-data surface is the
exception: it requires a per-partner API key at the edge (`key-auth`).

## Rate limiting

| Scope | Plugin | Limit |
| --- | --- | --- |
| `/api/citizen/*` | `limit-count` | 600 req/min per `remote_addr` (429) |
| `/api/open-data/*` | `limit-count` | 600 req/min per `remote_addr` (429) |
| `POST /api/infra/v1/safety/leak` | `limit-req` | 10 r/s, burst 20, per `remote_addr` (429) |

The leak-ingest route is declared **ahead of** the generic `/api/infra/*` route
so sensor traffic gets the stricter policy.

## CORS

A global rule attaches the `cors` plugin to every route. The allowed origin is
env-configurable: set `PWA_ORIGIN` on the apisix container (APISIX expands
`${{PWA_ORIGIN:=http://localhost:3000}}`). The default matches the local
compose stack where the PWA is served at `http://localhost:3000`. Credentials
are allowed, so the origin must stay explicit — never `*`.

## Admin API lockdown & key rotation

- `deployment.admin.admin_listen` binds `127.0.0.1:9180` and
  `deployment.admin.allow_admin` is `127.0.0.1/32` only. The Admin API is
  unreachable from other containers/hosts; **do not publish `:9180`** in
  docker-compose.
- The admin key is env-injected:
  `key: ${{APISIX_ADMIN_KEY:=REPLACE_ME_ROTATE_THIS_ADMIN_KEY}}`.
  Set `APISIX_ADMIN_KEY` to a long random value in the environment (compose
  `environment:` or, better, a secrets backend).

**Rotation procedure:**

1. Generate a new key (`openssl rand -hex 32`) and store it in the secret
   manager.
2. Update `APISIX_ADMIN_KEY` and restart/recreate the apisix container.
3. Revoke the old key from the secret manager and audit Admin API access logs.

## TLS

`infra/apisix/config.yaml` ships a **commented, complete** `apisix.ssl` block
(listen 9443, TLSv1.2/1.3, default cert paths under `/etc/ssl/h2fleet/`).
To enable:

1. Obtain a certificate.
   - **Kubernetes:** annotate the ingress/APISIX Deployment with cert-manager
     (`cert-manager.io/cluster-issuer: letsencrypt-prod`) so cert-manager
     issues a Let's Encrypt certificate into a TLS `Secret`; mount that Secret
     into the APISIX container at `/etc/ssl/h2fleet/`.
   - **Compose/dev:** place certs under `infra/apisix/certs/` and add read-only
     volume mounts for `tls.crt`/`tls.key`.
2. Uncomment the `ssl` block and publish `9443`.
3. Either redirect 9080 → 9443 at the edge or terminate TLS at a load balancer
   in front of APISIX and keep 9080 internal.

Per-SNI certificates can also be declared as `ssls:` objects in `apisix.yaml`.

## WAF (OpenAppSec)

The `openappsec` plugin is enabled in the global rule (applies to every route)
with **`mode: detect`** as the default — traffic is inspected/logged, nothing
is blocked. **Toggle to enforce:** change the single `mode:` value in
`apisix.yaml` → `global_rules` to `prevent`.

Requirements:

- The `openappsec-agent` nano-agent sidecar must run on the same network —
  in compose start the `waf` profile: `docker compose --profile waf up`.
- `AGENT_TOKEN` on the agent container links it to the OpenAppSec cloud
  management plane (empty = standalone local learning).
- If the agent is **not** deployed, comment the `openappsec` plugin out of the
  global rule: an unreachable agent can add latency or fail requests.

## Secrets

- **OIDC client secret** (`h2fleet-services-secret-change-me`) must match the
  Keycloak `services` client in the `h2fleet` realm; rotate both together.
- **Partner API keys** (`key-auth` consumers): the repo ships an obvious
  placeholder (`REPLACE_ME_PROVISION_PER_PARTNER`). Real keys are provisioned
  per partner — one consumer per partner, keys generated by the platform team,
  delivered out-of-band, and revocable by deleting the consumer. Prefer the
  APISIX secret reference form (`$ENV://H2_PARTNER_API_KEY` or a vault
  provider) so no real key is ever committed.

## Metrics (Prometheus)

The `prometheus` plugin is enabled via a global rule; the exporter listens on
`:9091` (`plugin_attr.prometheus.export_addr` in `config.yaml`). Publish/map
`:9091` on the apisix container and add a scrape target
`apisix:9091` (path `/apisix/prometheus/metrics`) to the Prometheus server.

## docker-compose touchpoints

The gateway expects the following compose wiring (see the orchestrator's
compose change list):

- **Unbind** the host `:9180` mapping (Admin API lockdown).
- **Publish** `:9091` for Prometheus scraping.
- **Publish** `:9443` when the TLS block is enabled, plus the cert mounts.
- `environment:` — `APISIX_ADMIN_KEY` (strong random), `PWA_ORIGIN`
  (e.g. `https://app.example.gov` in prod).
- `openappsec-agent` (profile `waf`) present with `AGENT_TOKEN` when the WAF
  plugin is active.

## Production checklist

- [ ] `config_provider: yaml` standalone routes verified (`apisix verify` / boot log lists routes).
- [ ] Admin API not reachable from outside the container (`curl :9180` from host fails); strong `APISIX_ADMIN_KEY` set.
- [ ] TLS enabled: cert-manager/Let's Encrypt (k8s) or mounted certs (compose); 9443 published; 9080 redirected or internal-only.
- [ ] WAF profile `waf` running, `AGENT_TOKEN` set, mode flipped to `prevent` after a detect-mode burn-in period.
- [ ] `PWA_ORIGIN` set to the real PWA origin (no wildcard with credentials).
- [ ] Real per-partner `key-auth` consumers provisioned; placeholder removed.
- [ ] OIDC client secret rotated from `*-change-me` values.
- [ ] `:9091` scraped by Prometheus; 429-rate alerts on `limit-count`/`limit-req` routes.
- [ ] No `/api/*` route added for carbon-analytics:8094 (internal-only).
