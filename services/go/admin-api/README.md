# admin-api

Admin & onboarding backend for H2Fleet. Port **8085** (gateway prefix
`/api/admin/*`). Owns stakeholder onboarding (Keycloak provisioning),
platform user management, the unified cross-service KPI aggregation and the
NOC/SOC operations feed (health sweep, alerts, enriched toggles).

Error envelope everywhere: `{"error": "<message>"}` (same shape as
`packages/go-auth`).

## API

### Stakeholder onboarding

Storage: Postgres `platform.onboarding_requests` (migration
`infra/sql/migrations/0011_wave8_onboarding.sql`; an idempotent
`EnsureSchema` backstop remains at boot): `id uuid pk, persona, email,
display_name, org, status[pending|approved|rejected|completed|expired],
keycloak_sub, meta jsonb, created_at, decided_at, decided_by`. A partial
unique index on `(persona, email) WHERE status='pending'` backstops
application-level dedup.

| Method | Path                              | Auth                                | Description |
|--------|-----------------------------------|-------------------------------------|-------------|
| POST   | `/v1/onboarding/citizen`          | public                              | Citizen self-serve: validates intake, provisions the Keycloak user with realm role `citizen`, sends the VERIFY_EMAIL+UPDATE_PASSWORD actions email, records `status=completed` immediately → `201 {"request": {...}, "message": "..."}`. If a previous attempt left an orphaned `pending` row (Keycloak outage), the retry adopts and completes it → `200` |
| POST   | `/v1/onboarding/{persona}`        | public                              | Intake for `driver`, `operator`, `station-staff`, `advertiser`, `data-partner`, `gov-viewer` → `201 {"request": {...}}` with `status=pending`. Re-filing the same `(persona, email)` while pending replays the original → `200 {"request": {...}, "deduplicated": true}` |
| GET    | `/v1/onboarding/status/{id}`      | public                              | Applicant status check (capability URL): `{id, persona, status, created_at, decided_at}` only — never email/name/org/meta; unknown ids 404 |
| GET    | `/v1/onboarding?status=&persona=` | role `platform-admin` or `operator` | List → `{"requests": [...]}` (`status=` accepts `expired` too) |
| GET    | `/v1/onboarding/{id}`             | role `platform-admin` or `operator` | Single request → `{"request": {...}}` |
| POST   | `/v1/onboarding/{id}/approve`     | role `platform-admin`               | Provisions the Keycloak user (mapped realm role via idempotent `EnsureRealmRole`, temp password, actions email) → `status=completed` |
| POST   | `/v1/onboarding/{id}/reject`      | role `platform-admin`               | Optional body `{"reason": "..."}` (merged into `meta.reject_reason`) → `status=rejected` |
| POST   | `/v1/onboarding/reconcile`        | role `platform-admin`               | Self-heal sweep: re-asserts the persona realm role on every `completed` request with a `keycloak_sub` → `200 {"checked","ensured","failed","failed_ids"}` (failures reported, never hidden) |

Intake body: `{"email": "...", "display_name": "...", "org": "...", "meta": {...}}`
— `email` + `display_name` required for all personas; **`org` required for
every approval-gated persona** (Wave-8); **`driver` additionally requires
`meta.license_no`** (4–64 chars).

Approving/rejecting a non-`pending` request → `409`. Keycloak failure on
approve → `502` and the request stays `pending` (safe to retry). A request
still `pending` after `ONBOARDING_PENDING_TTL_DAYS` (default 30) is expired:
the decide path refuses with `409` and a boot + daily sweep flips stragglers
to `status=expired` (`decided_by='system:ttl-expiry'`).

Abuse controls on the public intake (all fail closed, Wave-8):
per-IP `limit-req` at the APISIX route **plus** an application-level cap of
**5 requests per email per 24 h** (`429`, thwarts address-targeted spam from
rotating IPs), and an optional captcha — set `ONBOARDING_CAPTCHA_VERIFY_URL`
+ `ONBOARDING_CAPTCHA_SECRET` (siteverify-compatible: hCaptcha / Cloudflare
Turnstile) and every intake must carry a valid `captcha_token` (`400` bad/
missing token, `502` verifier unreachable).

#### Persona → Keycloak realm-role mapping

The `h2fleet` realm defines `platform-admin`, `operator`, `driver`,
`station-staff` (Wave-8) and `citizen`; read-only portal personas map onto
`citizen`:

| Persona        | Realm role     | Notes |
|----------------|----------------|-------|
| `citizen`      | `citizen`      | self-serve, provisioned immediately |
| `driver`       | `driver`       | dispatch job acceptance |
| `operator`     | `operator`     | back-office operations |
| `station-staff`| `station-staff`| Wave-8: own role (was `operator`); station create/status/queue routes accept `operator` **or** `station-staff` |
| `advertiser`   | `citizen`      | read-only portal access |
| `data-partner` | `citizen`      | read-only; open-data API keys are APISIX consumers, not roles |
| `gov-viewer`   | `citizen`      | read-only dashboard access |

### User management (all require role `platform-admin`)

| Method | Path                            | Description |
|--------|---------------------------------|-------------|
| GET    | `/v1/users?role=&q=`            | List Keycloak users with realm roles → `{"users": [{id, username, email, first_name, last_name, enabled, roles}]}`. `role=` filters via `/roles/{role}/users`, `q=` free-text search |
| POST   | `/v1/users`                     | Body `{email, display_name, roles?}` → `201 {"id": "..."}` + actions email |
| PUT    | `/v1/users/{id}/roles`          | Body `{add: [...], remove: [...]}` — assign/revoke realm roles |
| POST   | `/v1/users/{id}/disable`        | `enabled=false` |
| POST   | `/v1/users/{id}/enable`         | `enabled=true` |
| POST   | `/v1/users/{id}/reset-password` | Sends UPDATE_PASSWORD actions email |

### Unified KPI aggregation

`GET /v1/admin/kpis` (role `platform-admin` or `operator`) — server-side
fan-out across the four domains with a 3s per-source timeout and partial
degradation. Sources: fleet availability + telemetry rate (Postgres `fleet`),
open incidents (`infra`), DRT requests today + carbon total (`citizen`),
payments/revenue 30d (`commerce`), per-domain module enabled counts
(toggle-service HTTP).

```json
{
  "generated_at": "2026-07-25T00:00:00Z",
  "fleet":    {"vehicles_total": 50, "vehicles_available": 44, "telemetry_points_per_min": 120.0},
  "infra":    {"open_incidents": 2},
  "citizen":  {"drt_requests_today": 7, "carbon_kg_co2_total": 1234.5},
  "commerce": {"payments_30d": 12, "revenue_30d_minor": 45600, "currency": "EUR"},
  "toggles":  {"modules_enabled": 18, "modules_total": 20,
               "domains": {"fleet": {"enabled": 4, "total": 5}, "infra": {"enabled": 5, "total": 5},
                           "citizen": {"enabled": 5, "total": 5}, "commerce": {"enabled": 4, "total": 5}}},
  "meta": {"partial": false, "degraded": []}
}
```

A failed/timed-out source yields `null` for its section and is named in
`meta.degraded` (with `meta.partial: true`); the endpoint still returns 200.

### NOC/SOC operations feed (role `platform-admin` or `operator`)

| Method | Path                          | Description |
|--------|-------------------------------|-------------|
| GET    | `/v1/admin/health`            | Concurrent sweep: `/healthz` of all 10 platform services (toggle-service, fleet-api, infra-api, citizen-api, commerce-api, admin-api, predictive-maintenance, route-optimizer, digital-twin, carbon-analytics) + TCP checks of kafka:9092, postgres:5432, redis:6379, opensearch:9200, temporal:7233, tigerbeetle:3000 (2s per-check timeout) → `{"generated_at", "checks": [{name, kind, target, status, latency_ms}], "summary": {up, down}}` |
| GET    | `/v1/admin/alerts`            | Proxies Alertmanager `GET /api/v2/alerts` verbatim; returns `[]` (200) gracefully when Alertmanager is down |
| GET    | `/v1/admin/toggles`           | Enriched toggle list → `{"toggles": [{module, domain, enabled, owning_services}]}` (sorted by domain, then module) |
| PUT    | `/v1/admin/toggles/{module}`  | **role `platform-admin`** — proxies to toggle-service `PUT /v1/toggles/{module}` forwarding the caller's JWT (toggle-service owns `feature_toggles` and re-enforces the role) |

### Meta

| Method | Path        | Auth  | Description |
|--------|-------------|-------|-------------|
| GET    | `/healthz`  | none  | `{"status":"ok"}` (503 when Postgres unreachable) |
| GET    | `/metrics`  | none  | Prometheus scrape endpoint |

## Feature coverage (20 modules, SPEC §3.1)

| Admin surface | Governs |
|---------------|---------|
| `GET/PUT /v1/admin/toggles` | All **20 modules** across the 4 domains: `telematics`, `predictive-maintenance`, `digital-twin`, `fuel-monitoring`, `route-energy-optimizer` (fleet); `refueling-stations`, `leak-detection`, `dispatch-workforce`, `compliance-reporting`, `depot-management` (infra); `passenger-pwa`, `mobile-app`, `demand-responsive`, `carbon-credits`, `open-data-portal` (citizen); `fare-payments`, `loyalty-marketplace`, `energy-trading`, `gov-dashboard`, `advertising` (commerce). Each entry carries `domain` + `owning_services`. |
| `GET /v1/admin/kpis` | All **4 domains**: `fleet` (availability, telemetry rate), `infra` (open incidents), `citizen` (DRT today, carbon total), `commerce` (payments 30d, revenue) + per-domain toggle counts. |
| `GET /v1/admin/health` | The 10 platform services serving those modules + the 6 middleware systems they depend on. |
| Onboarding + user mgmt | Provisions the identities/roles that operate the modules (operator/driver/citizen). |

## Configuration (env)

| Var                          | Required | Default                     | Notes |
|------------------------------|----------|-----------------------------|-------|
| `PORT`                       | no       | `8085`                      | HTTP port |
| `DATABASE_URL`               | yes      | —                           | Postgres DSN (same cluster as the domain schemas) |
| `KEYCLOAK_ISSUER`            | no       | —                           | e.g. `http://keycloak:8080/realms/h2fleet`; JWT-protected routes fail closed when unset |
| `KEYCLOAK_ADMIN_URL`         | no       | `http://keycloak:8080`      | Keycloak base URL for Admin REST |
| `KEYCLOAK_REALM`             | no       | `h2fleet`                   | realm for Admin REST + client-credentials token |
| `KEYCLOAK_ADMIN_CLIENT_ID`   | yes      | —                           | service-account client (realm-management roles); **unset ⇒ fail-closed startup** unless `H2_SIMULATED_KEYCLOAK=true` |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` | yes    | —                           | as above |
| `H2_SIMULATED_KEYCLOAK`      | no       | —                           | `true` opts into the simulated in-memory Keycloak admin client (DEV ONLY; no real users are created) |
| `ONBOARDING_PENDING_TTL_DAYS` | no      | `30`                        | pending requests older than this are expired (decide guard + boot/daily sweep) |
| `ONBOARDING_CAPTCHA_VERIFY_URL` + `ONBOARDING_CAPTCHA_SECRET` | no | — | both set ⇒ public intake requires a valid `captcha_token` (siteverify-compatible: hCaptcha / Turnstile); unset ⇒ captcha disabled |
| `TOGGLE_URL`                 | no       | `http://toggle-service:8080`| toggle-service base URL |
| `ALERTMANAGER_URL`           | no       | `http://alertmanager:9093`  | Alertmanager base URL |
| `TOGGLE_SERVICE_URL` … `CARBON_ANALYTICS_URL` | no | `http://<service>:<port>` | health-sweep service base URLs (see `internal/config`) |
| `KAFKA_TCP_ADDR`, `POSTGRES_TCP_ADDR`, `REDIS_TCP_ADDR`, `OPENSEARCH_TCP_ADDR`, `TEMPORAL_TCP_ADDR`, `TIGERBEETLE_TCP_ADDR` | no | `kafka:9092` … | health-sweep middleware TCP targets |

**Dev fallback (explicit opt-in):** when `KEYCLOAK_ADMIN_CLIENT_ID/SECRET`
are unset the service fails closed at startup. Setting
`H2_SIMULATED_KEYCLOAK=true` opts into a simulated in-memory client with loud
`SIMULATED keycloak: …` warnings — onboarding and user-management flows work
end-to-end without a privileged service account, but no real users are
created. Never set this in any shared/production environment.

In production the client needs a service account with the realm-management
roles `manage-users`, `view-users` and `manage-realm` (role assignment) in
the `h2fleet` realm.

## Run

```sh
go run ./cmd/server
# or (build context = repo root)
docker build -f services/go/admin-api/Dockerfile -t h2fleet/admin-api .
docker run -p 8085:8085 -e DATABASE_URL=postgres://... h2fleet/admin-api
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0),
hand-maintained from the actual route registrations.
