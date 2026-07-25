# H2Fleet Security Audit — Code Exploitation Review

**Date:** 2026-07-25
**Scope:** All services under `services/` (Go, Python, Rust), `packages/`, `apps/`, `infra/` (APISIX, Keycloak, k8s, compose).
**Method:** Manual code review of every HTTP route/handler, auth middleware, proxy, and script; dependency CVE scanning via OSV (see `docs/OSS_VULNERABILITIES.md`).
**Attacker models:** (E) external unauthenticated internet user via APISIX; (A) external attacker with a valid citizen JWT (self-serve onboarding makes this free); (I) internal attacker with network/pod access.

**Companion document:** `docs/OSS_VULNERABILITIES.md` (dependency + container CVE tables).

---

## Findings summary (ranked)

| # | Severity | Finding | Location |
|---|----------|---------|----------|
| F1 | **P0** | Payment `rider_sub` spoofing → debit any rider's wallet | `services/go/commerce-api/internal/handlers/payments.go:78-90` |
| F2 | **P0** | DRT cancel has no ownership check → cancel anyone's ride | `services/go/citizen-api/internal/handlers/drt.go:150-190` |
| F3 | **P1** | Operators can mint new operator accounts via onboarding approve | `services/go/admin-api/internal/server/server.go:57-64` + `onboarding/store.go:48-58` |
| F4 | **P1** | Public citizen self-serve: email enumeration + email-bombing, no rate limit | `services/go/admin-api/internal/onboarding/handlers.go:92-126` |
| F5 | **P1** | Model-artifact poisoning → RCE (`torch.load` CVE-2025-32434 path + `joblib.load`) | `services/python/predictive-maintenance/app/lstm_model.py:114`, `model.py:127`, `ml-platform/app/registry.py:66` |
| F6 | **P2** | Live fleet twin/telemetry readable without authentication | `services/go/fleet-api/cmd/server/main.go:77`, `services/rust/digital-twin/src/api.rs:30-36`, `infra/apisix/apisix.yaml` (`/api/twin/*`) |
| F7 | **P2** | Rate-limit coverage gaps at APISIX (commerce/fleet/infra/toggles/ml/optimize/twin) | `infra/apisix/apisix.yaml` |
| F8 | **P2** | Unbounded JSON request bodies outside admin-api | citizen-api, commerce-api, infra-api, toggle-service handlers |
| F9 | **P2** | Weak/default credentials shipped in compose + `.env.example` | `infra/docker-compose.yml:241-262,454,586-587`, `.env.example:11-77` |
| F10 | **P2** | TLS absence: all in-cluster traffic plaintext; APISIX TLS disabled; Postgres `sslmode=disable` | `infra/docker-compose.yml:47,219,313`, `infra/apisix/config.yaml` |
| F11 | **P2** | AuthZ degrade paths: Permify role-only fallback; `KEYCLOAK_AUDIENCE` optional | `packages/go-auth/permify.go:13-18`, `packages/go-auth/jwt.go:15-19` |
| F12 | **P2** | Any citizen JWT can open safety incidents (false-alarm flooding) | `services/go/infra-api/cmd/server/main.go:111` |
| F13 | **P2** | Mutable/unpinned image tags (`:latest`, `neo4j:5-community`, `daprio/daprd:latest`) | `infra/docker-compose.yml`, `infra/k8s/base/*.yaml` |
| F14 | **P3** | Backend `/metrics` reachable through public APISIX prefixes | `infra/apisix/apisix.yaml` proxy-rewrite + service `/metrics` routes |

---

## P0 — must fix before any production exposure

### F1. Payment `rider_sub` spoofing — theft of funds (IDOR)

**Location:** `services/go/commerce-api/internal/handlers/payments.go:78-90` (`CreatePayment`), exposed at `services/go/commerce-api/cmd/server/main.go:75` (`POST /v1/payments`, `RequireAuth` only).

**Defect:** The request body's `rider_sub` is trusted verbatim. It only defaults to the JWT subject when absent:

```go
if req.RiderSub == "" {
    req.RiderSub = auth.Subject(r.Context())
}
```

There is no check that a caller-supplied `rider_sub` equals `auth.Subject(r.Context())` (compare with `ListPayments`, which does scope correctly, `payments.go:297-299`).

**Exploit (attacker A — any citizen account, free via F4):**

```
POST /api/commerce/v1/payments
Authorization: Bearer <attacker-citizen-jwt>
Idempotency-Key: atk-001
{"rider_sub": "<victim-sub>", "amount_minor": 100000, "currency": "EUR"}
```

The insert records the victim as the payer (`payments.go:90`), then `riderAccount(req.RiderSub)` resolves the **victim's** TigerBeetle wallet and `ledger.Transfer(..., account, OperatorRevenueAccount, amount, ...)` debits it into operator revenue (`payments.go:131-140`). The Mojaloop leg likewise names the victim as `payer` (`payments.go:252`). Repeat with fresh idempotency keys to drain the wallet.

**Required fix:** In `CreatePayment`, ignore the body field for non-privileged callers:

```go
if !auth.HasAnyRole(r.Context(), "operator", "platform-admin") || req.RiderSub == "" {
    req.RiderSub = auth.Subject(r.Context())
}
```

Add a regression test in `payments_test.go` asserting a citizen-supplied foreign `rider_sub` results in a payment owned by the caller.

---

### F2. DRT cancel IDOR — cancel any citizen's ride

**Location:** `services/go/citizen-api/internal/handlers/drt.go:150-190` (`CancelDRTRequest`), route `services/go/citizen-api/cmd/server/main.go:84` (`RequireAuth` only).

**Defect:** `GetDRTRequest` correctly enforces ownership (`drt.go:217-222`) and `ListDRTRequests` scopes to the caller (`drt.go:115-121`), but `CancelDRTRequest` loads and updates the row **by id only** — `SELECT status ... WHERE id = $1` (drt.go:163) and `UPDATE ... WHERE id = $1` (drt.go:181). No `user_sub` comparison anywhere in the handler.

**Exploit (attacker A):** Obtain a request id (IDs are guessable/leakable via events, logs, or sibling endpoints; UUIDs are not an authorization boundary), then:

```
POST /api/citizen/v1/drt/requests/<victim-request-id>/cancel
Authorization: Bearer <attacker-jwt>
```

→ victim's ride cancelled. Mass-cancel is scriptable. The handler also leaks existence and current status of foreign requests via the 409 `"request cannot be cancelled in status <status>"` response (drt.go:173-176), which `GetDRTRequest` deliberately avoided.

**Required fix:** Mirror the owner check from `GetDRTRequest`: select `user_sub` with the status, and when `user_sub != auth.Subject(ctx) && !HasAnyRole(ctx, "operator", "platform-admin")` return **404** (not 403/409). Alternatively make the UPDATE conditional: `WHERE id = $1 AND user_sub = $2 AND status IN (...)` for non-operators, returning 404 on no rows. Add a test cancelling another user's request expecting 404.

---

## P1 — fix this cycle

### F3. Operators can mint new operator accounts (privilege self-replication)

**Location:** `services/go/admin-api/internal/server/server.go:57-64` (approve/reject gated by `RequireAnyRole("operator", "platform-admin")`) combined with `services/go/admin-api/internal/onboarding/store.go:48-58`:

```go
PersonaOperator:     "operator",
PersonaStationStaff: "operator",   // station staff operate stations
```

**Defect:** Approval of a pending `operator` or `station-staff` intake provisions a Keycloak user with the **operator** realm role (`handlers.go:294`, `RealmRole(persona)`). Because approval authority itself only requires the operator role, any single operator account (e.g. one compromised driver-ops credential) can mint unlimited additional operator accounts — silent persistence with no platform-admin involvement. Newly minted operators can then create stations, trades, offers, ad campaigns, dispatch jobs and close work orders.

**Required fix:** Split the route gate: personas whose `RealmRole` is `operator` (i.e. `operator`, `station-staff`) may only be approved by `platform-admin`. Concretely, register a second group with `d.JWT.RequireRole("platform-admin")` and inside `Approve` (or the router) reject `operator`-role approvers when `RealmRole(req.Persona) == "operator"`. Test: operator approving an `operator` intake → 403.

---

### F4. Public citizen self-serve — account enumeration, email bombing, DB spam

**Location:** `services/go/admin-api/internal/server/server.go:55` (public, no JWT) → `services/go/admin-api/internal/onboarding/handlers.go:92-126` (`CitizenSelfServe`).

**Defects:**
1. **Email/account enumeration:** on duplicate email, Keycloak's `CreateUser` error is echoed to the client — `"identity provisioning failed: "+err.Error()` (`handlers.go:119`). The Keycloak 409 message differs from success, disclosing whether an address is registered.
2. **Email bombing / relay abuse:** each call immediately provisions the user and sends a Keycloak VERIFY_EMAIL/UPDATE_PASSWORD actions email (`handlers.go:283-302`) to an **arbitrary** address. Unauthenticated and (for this route) unrate-limited, the platform becomes an email-bombing tool against third parties and a vector to burn SES/mail quotas.
3. **Unbounded intake spam:** `POST /v1/onboarding/{key}` (`server.go:56`) inserts a Postgres row per call with no auth, captcha, dedupe, or per-IP throttle. The APISIX `limit-count` on `/api/admin/*` (300 req/min/IP, `apisix.yaml:170-176`) is per-remote-addr only and trivially rotated behind NATs/botnets.

**Note:** direct-Keycloak registration bypass is correctly closed — `infra/keycloak/realm-h2fleet.json:7` sets `"registrationAllowed": false`.

**Required fix:** (a) Return a constant generic message on provisioning failure; never echo `err.Error()` to the client (log it instead). (b) Add a dedicated strict `limit-req` APISIX route for `POST /api/admin/v1/onboarding/*` (e.g. 5 r/min/IP), mirroring the leak-ingest route pattern. (c) Defer user creation until email verification, or dedupe by email with a silent no-op response. (d) Consider proof-of-work/captcha on the public intake.

---

### F5. Model-artifact poisoning → RCE in ML services (internal + supply-chain chain)

**Locations:**
- `services/python/predictive-maintenance/app/lstm_model.py:114` — `torch.load(..., weights_only=True)`
- `services/python/predictive-maintenance/app/model.py:127` — `joblib.load(path)` (sklearn fallback)
- `services/python/ml-platform/app/registry.py:66` — `torch.load(..., weights_only=True)`
- `services/python/ml-platform/training/train.py:130` — same

**Defect:** `requirements.txt` pins `torch==2.5.1` in both `ml-platform` and `predictive-maintenance`. torch 2.5.1 is vulnerable to **CVE-2025-32434 / GHSA-53q9-r3pm-6pq6 (CRITICAL)**: `torch.load` RCE *even with* `weights_only=True` (fixed in 2.6.0). `weights_only=True` here is **not** a mitigation. Separately, `joblib.load` on a sklearn artifact is pickle execution by design.

**Exploit (attacker I):** anyone who can write to the shared model-artifacts volume or the backing object store (MinIO with default creds `h2admin/h2adminpass`, F9; MLflow 2.14.1 artifact endpoints with their own CVEs — see OSS report) replaces `weights.pt` / the joblib artifact. On next service start/reload, code executes as the ml-platform / predictive-maintenance pod identity, which holds `DATABASE_URL` and Kafka access.

**Required fix:** bump `torch` to ≥2.6.0 (prefer current 2.7+/2.8) in `services/python/ml-platform/requirements.txt` and `services/python/predictive-maintenance/requirements.txt`; store artifacts with integrity hashes (record sha256 in `registry.json` and verify before load); make the artifacts volume/bucket read-only to serving pods; remove default MinIO credentials (F9).

---

## P2 — hardening backlog

### F6. Unauthenticated read access to live fleet data
- `GET /api/fleet/v1/vehicles/{id}/twin` — fleet-api `main.go:77` has only the module gate, **no** `RequireAuth`.
- `GET /api/twin/v1/twin` and `/v1/twin/{id}` — digital-twin (`services/rust/digital-twin/src/api.rs:30-36`) implements no auth at all, and APISIX `/api/twin/*` uses `unauth_action: pass`.
- All telematics GETs (`/v1/vehicles`, `/v1/telemetry/latest`, `/v1/vehicles/{id}/telemetry`, `main.go:70-76`) are likewise public.

Real-time position, H2 fill level and health of every bus is thus public. If this is intentional open data, document it and strip precise GPS/driver linkage; otherwise add `jwtmw.RequireAuth` to the fleet-api telematics/twin routes and either remove the direct APISIX `/api/twin/*` route or put key-auth on it.

### F7. Rate-limit coverage gaps (APISIX)
Only the leak route (10 r/s), citizen (600/min), admin (300/min) and open-data (600/min) routes are limited (`infra/apisix/apisix.yaml`). **commerce, fleet, infra, toggles, mlplatform, ml, optimize and twin have no limit plugin.** `POST /v1/payments` and `POST /v1/optimize/route` (OR-Tools solver — CPU-heavy) are the most abusable. Add a global `limit-count`/`limit-req` rule in `global_rules` (it already wraps every route) and stricter per-route limits on compute-heavy POSTs.

### F8. Unbounded request bodies
Only admin-api wraps decoders with `http.MaxBytesReader` (e.g. `onboarding/handlers.go:80`, `users/users.go:54`). citizen-api (`drt.go:63`), commerce-api (`payments.go:70`), infra-api (`incidents.go:83`, `dispatch.go:82`), toggle-service (`toggles.go:196`) decode `json.NewDecoder(r.Body)` with no cap. The 30 s chi `middleware.Timeout` bounds time, not memory. Wrap all decoders in `http.MaxBytesReader(w, r.Body, 1<<20)` (a shared helper in `packages/go-auth` or each service's `common.go`).

### F9. Default/weak credentials
Compose defaults (`infra/docker-compose.yml`): `KEYCLOAK_ADMIN_PASSWORD:-admin` (l.262), `KEYCLOAK_OPERATOR_PASSWORD:-operator123`, `KEYCLOAK_DRIVER_PASSWORD:-driver123`, `KEYCLOAK_CITIZEN_PASSWORD:-citizen123` (l.243-245), `KEYCLOAK_SERVICES_CLIENT_SECRET:-h2fleet-services-secret-change-me` (l.241), `APISIX_ADMIN_KEY:-h2fleet-admin-key-change-me` (l.454), `MINIO_ROOT_USER/PASSWORD:-h2admin/h2adminpass` (l.586-587), Grafana `admin`. `.env.example` repeats them (l.11-77). Any copy-paste deployment is fully compromised (Keycloak master admin on :8088 = mint any token = full platform takeover). Make all of these `${VAR:?required}` (no default) except for a clearly-labelled dev profile, and add a CI check that fails if `change-me` strings reach a non-dev overlay.

### F10. TLS absence matrix
| Hop | State |
|---|---|
| Client → APISIX :9080 | **HTTP** (TLS block commented out, `infra/apisix/config.yaml`; k8s ingress has `tls:` with placeholder secret, `infra/k8s/base/ingress.yaml:22-24`) |
| APISIX → services | HTTP |
| Services → Keycloak/JWKS | HTTP (`http://keycloak:8080`) |
| Services → Postgres | `sslmode=disable` (`docker-compose.yml:47,219,313`) |
| Services → Permify gRPC | `insecure.NewCredentials()` (`packages/go-auth/permify.go:85`) |
| Services → Kafka / Redis / OpenSearch / MinIO / Temporal | plaintext |

Acceptable only for local dev. For staging/prod: enable the APISIX `ssl:` block or LB termination, turn Postgres to `sslmode=require`, and document mTLS/service-mesh as the in-cluster control. k8s NetworkPolicies (`infra/k8s/base/networkpolicy.yaml`, default-deny + in-namespace allow) partially compensate on k8s but not on the compose network.

### F11. AuthZ degrade paths (verify in every deployment)
- **Permify:** when `PERMIFY_GRPC` is unset, `permify.Require` degrades to Keycloak-role-only with a single warn log (`packages/go-auth/permify.go:137-144`). Fail-closed on check errors is correctly implemented (502 on error, `permify.go:160-166`). Fail the service at startup instead of degrading when `ENV=production`.
- **Audience:** when `KEYCLOAK_AUDIENCE` is unset, `aud` is not validated (`packages/go-auth/jwt.go:15-19`) — any token for any client of the realm is accepted at every service. Set `KEYCLOAK_AUDIENCE` (compose already defaults it to `services`, `docker-compose.yml:54`; keep it in k8s configmaps).
- JWKS pinning is env-derived only; RS256 enforced via `jwt.WithValidMethods` + RSA type assertion (`jwt.go:203-211`) — **no alg confusion found**. Python verifier mirrors this correctly (`services/python/shared/h2fleet_auth/jwt.py:120-135`).

### F12. Incident false-alarm flooding
`POST /v1/incidents` requires only `RequireAuth` (`infra-api/cmd/server/main.go:111`) with no role, rate limit, or payload validation beyond a non-empty `Type` (`incidents.go:83`). Any citizen token can flood the safety-incident queue and trigger Temporal workflows. Gate to `driver`/`operator` roles, or rate-limit + validate `Type` against an enum.

### F13. Mutable/unpinned image tags
All h2fleet k8s deployments use `:latest` (`infra/k8s/base/*.yaml`); compose uses `daprio/daprd:latest` and `neo4j:5-community` (moving major tag). A stale `:latest` silently runs old code; a moving major tag silently upgrades. Pin immutable tags/digests (see OSS report for per-component targets; daprd must be ≥1.17.5 anyway for GHSA-85gx-3qv6-4463).

### F14. `/metrics` via public prefixes (P3)
`proxy-rewrite` strips `/api/<domain>` for the whole prefix, so `/api/fleet/metrics`, `/api/commerce/metrics`, etc. reach the services' Prometheus handlers unauthenticated. Low impact (operational metadata, Go/runtime stats) but leaks internal state; restrict `*/metrics` at APISIX or move metrics to a separate listener not routed by the gateway.

---

## Verified non-findings (checked, no defect)

- **SQL injection:** every pgx query uses `$n` placeholders. The only `fmt.Sprintf` in query construction is `depot.go:44-48`, which interpolates the **placeholder index**, not values. Python services use asyncpg parameter binding.
- **OpenSearch passthrough:** `opendata.go:78-87` builds a fixed `multi_match` DSL with `size:20`; user input is a JSON string value, not raw query — no DSL injection; response capped at 4 MiB.
- **SSRF via fleet proxy:** upstream hosts are fixed env URLs (`proxy.go:24-29`); `{id}` is a single chi path segment (no `/`), so no host/URL smuggling; **Authorization header is NOT forwarded upstream** — good.
- **Leak webhook token:** compared with `subtle.ConstantTimeCompare` (`infra-api/cmd/server/main.go:88`) — timing-safe; falls back to JWT when token unset.
- **Idempotency keys:** cross-user replay returns 404, keys scoped per owner (`payments.go:98-108`); TB transfer ID derived deterministically from key (`ledger.go:43-45`) — no double-post.
- **JWT:** RS256 enforced (Go `jwt.go` and Python `h2fleet_auth/jwt.py`); `exp` required; issuer allow-list; JWKS fetched only from the in-network issuer; stale-key-on-outage behavior sane. No alg-confusion or JWKS-poisoning path found.
- **Shell scripts:** `infra/backup/backup.sh` fully quoted; `substitute-realm.sh` rejects sed metacharacters in secrets — no command injection.
- **PWA/mobile:** no `dangerouslySetInnerHTML`/`innerHTML`/`eval` sinks in `apps/pwa/src` or `apps/mobile/src`.
- **Mutating-route auth coverage:** every POST/PUT/PATCH/DELETE route in all 6 Go services and all Python FastAPI services carries `RequireAuth`/`RequireRole` (Go) or `jwt_verifier.require_auth` (Python) at the service level — no `unauth_action: pass` route reaches an unguarded mutating handler.
- **Keycloak self-registration bypass:** `registrationAllowed: false` in the realm import.
- **k8s secrets:** `infra/k8s/base/secret.yaml` contains only `REPLACE_ME` placeholders — no committed credentials.
- **Rust services:** telemetry-ingest exposes only `/healthz`+`/metrics`; digital-twin is read-only; no mutating unauthenticated HTTP surface.

## Test gaps
- No tests assert cross-user access for `CancelDRTRequest` or `CreatePayment.rider_sub` (add with F1/F2 fixes).
- No test for operator-cannot-approve-operator (add with F3 fix).
- No fuzz/negative test for onboarding duplicate-email response equality (add with F4 fix).
