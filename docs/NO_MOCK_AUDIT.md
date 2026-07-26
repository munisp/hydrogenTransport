# NO_MOCK Audit — H2Fleet platform

**Date:** 2026-07-25
**Method (mandated):** raw line-number inventory from a repo-wide grep, then
direct READ of the exact hit lines + surrounding context, classification, and
one-by-one fixes. Scan command:

```sh
grep -rn -iE 'mock|stub|fake|simulat|placeholder|TODO|FIXME|XXX|HACK|not.?implemented|unimplemented|REPLACE_ME|dummy|hardcod|sample.?data|fallback|for now|in a real|production would' . \
  --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=target \
  --exclude-dir=__pycache__ --exclude-dir=dist --exclude='*.lock' \
  --exclude='package-lock.json' -I
```

**Raw inventory: 905 hits.** Distribution by area and by matched token:

| Area | Hits | | Token | Hits |
|---|---|---|---|---|
| test code (`*_test.go`, `*.test.ts`, `tests/`, fixtures) | 408 | | `mock` | 301 |
| docs (`*.md`, plans) | 162 | | `simulat*` | 233 |
| `services/` (non-test) | 135 | | `fake` | 144 |
| `infra/` | 107 | | `fallback` | 128 |
| `apps/` (PWA + mobile UI) | 78 | | `placeholder` | 93 |
| `packages/` | 9 | | `REPLACE_ME` | 56 |
| repo root | 6 | | `xxx` (all are account-range `1xxx`/`2xxx` strings) | 45 |
| | | | `stub` | 25 |
| | | | `hardcod` | 11 |
| | | | `TODO` | 5 |
| | | | `unimplemented` / `not implemented` | 8 |
| | | | `dummy` / `for now` / `FIXME` / `sample data` / `in a real` | 6 |

Every distinct code path was read at its exact file:line; repetitive groups
(18 identical apisix.yaml client_secret lines, 13 identical k8s `:REPLACE_ME`
image tags, UI `placeholder=` input attributes) were sampled plus
pattern-verified as a class.

---

## Classification summary

| Classification | Count (hits) | Notes |
|---|---|---|
| REAL (comment/string/doc only, or real production logic) | ~830 | test mocks/fakes are test-scoped by construction; docs describe behavior |
| DEV-ONLY-FALLBACK, env-gated (verified) | ~50 | see gate table below |
| DEV-ONLY-FALLBACK, **silently default → FIXED** | 4 code paths | F-1…F-4 below |
| Secret-substitution gap → FIXED | 2 paths | F-5, F-6 below |
| EXTERNAL-SYSTEM-BOUNDARY placeholders (verified substituted/fail-fast) | ~50 | apisix/k8s/realm REPLACE_ME class |

---

## Fixes applied (file:line before → after)

### F-1 — Simulated TigerBeetle ledger was the silent default (MONEY PATH)
**Before:** `services/go/commerce-api/internal/ledger/ledger.go:82-86` —
`New()` returned the in-memory `simulated` ledger whenever `TIGERBEETLE_ADDR`
was unset, with only a log warning. A payment/wallet/trade deployment missing
one env var would silently settle fabricated balances.
**After:** `ledger.go:80-93` — `New()` fails closed:
`TIGERBEETLE_ADDR is required … (set H2_SIMULATED_LEDGER=true to opt into the
in-memory dev ledger)`. `cmd/server/main.go:52-55` already `log.Fatal`s on
that error, so the service refuses to start rather than fabricate money
movement. Simulated ledger kept (same invariants) behind explicit opt-in
`H2_SIMULATED_LEDGER=true`, loud warn on selection. Package doc updated
(`ledger.go:1-7`), simulated-type doc updated (`ledger.go:185-189`).

### F-2 — Mojaloop leg returned a fabricated transfer id by default (MONEY PATH)
**Before:** `services/go/commerce-api/internal/handlers/payments.go:281-285` —
with `MOJALOOP_ENDPOINT` unset, `use_mojaloop` payments got a
`ml-simulated-<uuid>` transfer id and status `settled`, i.e. a fabricated
success on a payment rail.
**After:** `payments.go:274-296` — without an endpoint the leg returns a
classified `mojaloop.Error{Kind: KindUnavailable}` → payment status
`mojaloop_unavailable` + 502 (never a transfer id). The labelled simulated id
exists only behind explicit `H2_SIMULATED_MOJALOOP=true`, logged at Warn.
Comments updated at `payments.go:56-61`, `payments.go:159-164`,
`internal/mojaloop/client.go:18-20`.

### F-3 — Simulated Keycloak admin was the silent default (IDENTITY PATH)
**Before:** `services/go/admin-api/internal/keycloak/keycloak.go:61-65` —
unset `KEYCLOAK_ADMIN_CLIENT_ID/SECRET` silently selected the in-memory
`simulated` admin client: onboarding "created users" that do not exist.
**After:** `keycloak.go:59-77` — `New()` now returns `(AdminClient, error)`
and fails closed (`KEYCLOAK_ADMIN_CLIENT_ID/SECRET are required … set
H2_SIMULATED_KEYCLOAK=true to opt into the simulated dev client`).
`cmd/server/main.go:55-59` `log.Fatal`s on the error. Simulated client kept
behind explicit opt-in (`simulated.go:15-20`). Callers updated:
`internal/server/server_test.go:133-138` (test opts in via `t.Setenv`).
`internal/config/config.go:17` and `internal/users/users.go:4-6` comments
updated.

### F-4 — Dev wallet top-up default tied to "env unset"
**Before:** `services/go/commerce-api/internal/handlers/wallets.go:24-29` —
`TopUpEnabled` defaulted ON whenever `TIGERBEETLE_ADDR` was unset.
**After:** `wallets.go:17-30` — default ON only when the simulated ledger was
explicitly opted into (`H2_SIMULATED_LEDGER=true`); `WALLET_TOPUP_ENABLED`
explicit override unchanged. Behavior in every shipped deployment (compose +
k8s set `TIGERBEETLE_ADDR`) is unchanged: top-up stays OFF. Test updated:
`wallets_test.go:80-100`.

### F-5 — Dev compose: apisix never received the Keycloak services secret
**Before:** `infra/docker-compose.yml` apisix service (was lines 455-457)
exported only `APISIX_ADMIN_KEY` + `PWA_ORIGIN`; all 18
`${{KEYCLOAK_SERVICES_CLIENT_SECRET:=REPLACE_ME_…}}` entries in
`infra/apisix/apisix.yaml` therefore resolved to the literal REPLACE_ME
default instead of the realm secret rendered by `keycloak-realm-init`.
**After:** apisix service now exports
`KEYCLOAK_SERVICES_CLIENT_SECRET: ${KEYCLOAK_SERVICES_CLIENT_SECRET:-h2fleet-services-secret-change-me}`
(same default as the realm renderer), with a comment explaining the coupling.

### F-6 — Prod route sync pushed REPLACE_ME placeholders into etcd
**Before:** `infra/prod/apisix/sync_routes.py:30-44` substituted only
`PWA_ORIGIN`; the 18 openid-connect `client_secret` placeholders and the
`REPLACE_ME_PROVISION_PER_PARTNER` consumer key were pushed to etcd verbatim
(APISIX expands `${{VAR}}` only in config.yaml, never in etcd objects) — a
publicly-known placeholder secret on every authenticated route, and a working
placeholder credential for the data-partner consumer.
**After:** `sync_routes.py` `load()` now (a) requires
`KEYCLOAK_SERVICES_CLIENT_SECRET` (refuses to push without it) and renders it
into all client_secret placeholders; (b) renders `H2_PARTNER_API_KEY` into
the data-partner consumer or **drops the consumer fail-closed** when unset;
(c) asserts no `REPLACE_ME`/`${{` remains post-render. Env wired in
`infra/prod/docker-compose.prod.yml` (`KEYCLOAK_SERVICES_CLIENT_SECRET`
required via `${VAR:?}`, `H2_PARTNER_API_KEY` optional). Functionally tested:
3 scenarios (no secret → refuse; secret only → 19 routes rendered, consumer
dropped; partner key set → consumer kept with real key).

### Documentation updates (fallback gates + READMEs)
`.env.example` — new sections documenting `H2_SIMULATED_KEYCLOAK`,
`H2_SIMULATED_LEDGER`, `H2_SIMULATED_MOJALOOP`, `H2_PARTNER_API_KEY` (DEV ONLY
warnings). `services/go/commerce-api/README.md` — endpoint contract + env
table rows for `TIGERBEETLE_ADDR` (required/fail-closed),
`H2_SIMULATED_LEDGER`, `WALLET_TOPUP_ENABLED`, `MOJALOOP_ENDPOINT`,
`H2_SIMULATED_MOJALOOP`. `services/go/admin-api/README.md` — env table +
dev-fallback paragraph. `docs/MIDDLEWARE.md`, `docs/MOJALOOP.md`,
`docs/DEPLOYMENT.md` — fallback descriptions corrected to env-gated
fail-closed semantics.

---

## Fallback env-gate table (DEV-ONLY-FALLBACK, all verified)

| Fallback | Gate | Default in compose/k8s | Fabricates success? | Verified at |
|---|---|---|---|---|
| Simulated TigerBeetle ledger | `H2_SIMULATED_LEDGER=true` (opt-in; unset addr ⇒ startup Fatal) | Real TigerBeetle (`TIGERBEETLE_ADDR` pinned) | No (F-1) | `ledger/ledger.go:80-93` |
| Simulated Mojaloop transfer id | `H2_SIMULATED_MOJALOOP=true` (opt-in; else `mojaloop_unavailable` + 502) | Real `mojaloop/simulator` service via `MOJALOOP_ENDPOINT` | No (F-2) | `handlers/payments.go:274-296` |
| Simulated Keycloak admin client | `H2_SIMULATED_KEYCLOAK=true` (opt-in; else startup Fatal) | Real Admin REST (`KEYCLOAK_ADMIN_CLIENT_ID/SECRET` pinned + k8s secret) | No (F-3) | `keycloak/keycloak.go:59-77` |
| Dev wallet top-up endpoint | `WALLET_TOPUP_ENABLED`, default ON only with `H2_SIMULATED_LEDGER=true` | OFF (real TigerBeetle) | No (F-4) | `handlers/wallets.go:17-30` |
| PWA mock platform-admin identity | Vite `import.meta.env.DEV` (build-time dev-only; prod build throws), UI banner "Dev auth (mock admin)", no access token ⇒ backend 401s | Real Keycloak OIDC | No — never in prod bundle | `apps/pwa/src/auth/keycloak.ts:93-104`, `Layout.tsx:169-171` |
| Permify role-only fallback | `PERMIFY_GRPC` unset ⇒ realm-role check only (warn once); checks themselves fail closed (502 error / 403 deny) | Documented rollout contract (README, package doc) | No — degrades to enforced realm roles, never grants silently | `packages/go-auth/permify.go:9-15,127-177` |
| Predictive-maintenance rule model | Artifact missing ⇒ deterministic `RuleModel` (version `rules-v1` reported); LSTM → sklearn → rules preference order | Labelled model version in every response | No — degraded model is disclosed | `predictive-maintenance/app/model.py:28-35,114-137` |
| Route-optimizer seed fleet/stops | Empty DB ⇒ deterministic seeded problem, response source labelled `"seed"` vs `"database"` | Real fleet tables seeded by migration 0005 | No — source label disclosed | `route-optimizer/app/data.py:28-57,98-143` |
| ML synth training data | `--source synth` CLI flag on the offline trainer; metrics record `SYNTHETIC bootstrap data` | `postgres`/`iceberg` sources for real runs | No — training-time only, labelled | `ml-platform/training/train.py:437,460`, `training/datasets.py:161-164` |
| geo_enrich synthetic zones | Lakehouse reference tables absent ⇒ deterministic zones; migration 0005 seeds the real tables so prod joins real data | Real reference data | No — documented fallback | `lakehouse-etl/jobs/geo_enrich.py:25,73`, `migrations/0005:382-383` |
| Telemetry simulator | Dedicated service (stands in for 50 physical buses); envelopes labelled `"source": "telemetry-simulator"`; `make simulate` opt-in target | Runs in `apps` profile as the documented bus-fleet stand-in | No — it IS the sensor fleet boundary, labelled | `telemetry-simulator/app/config.py:14-17` |
| No-op event publisher | Only when neither `DAPR_GRPC_PORT` nor `KAFKA_BROKERS` set; loud Warn at startup | Real Kafka pinned in compose | No — ledger/DB writes are real; events logged | `citizen-api/internal/pubsub/pubsub.go:37-67` |
| Seed data (stops/routes, toggles) | Migration 0005 + `EnsureSchemaAndSeed` idempotent seeds | Applied at deploy | No — reference data | `migrations/0005_missing_schemas.sql:308-351` |
| EnsureSchema dev-DDL | Idempotent `CREATE/ALTER IF NOT EXISTS` for service-owned supplemental tables | Migrations are the source of truth; DDL is additive/defensive | No | `commerce-api/internal/handlers/common.go:29-117` |
| Fluvio edge tier | Compose profile `edge` — "simulates ONE bus gateway on a laptop" | Documented edge bridge (noted not-implemented in docs) | No — explicit profile | `infra/fluvio-edge/docker-compose.edge.yml:2` |

---

## Secret-placeholder substitution verification (REPLACE_ME class)

| Placeholder | Location | Substituted by | Verdict |
|---|---|---|---|
| `${{KEYCLOAK_SERVICES_CLIENT_SECRET:=REPLACE_ME_…}}` ×18 | `infra/apisix/apisix.yaml:64-479` | APISIX env expansion (dev: **F-5 fix** — env was missing); `sync_routes.py` (prod: **F-6 fix** — was pushed verbatim) | FIXED |
| `REPLACE_ME_PROVISION_PER_PARTNER` | `infra/apisix/apisix.yaml:507-510` | `H2_PARTNER_API_KEY` via `sync_routes.py`; consumer dropped fail-closed when unset (F-6) | FIXED |
| `${{APISIX_ADMIN_KEY:=REPLACE_ME_ROTATE_THIS_ADMIN_KEY}}` | `infra/apisix/config.yaml:54`, `infra/prod/apisix/config.yaml.prod:55` | APISIX expands config.yaml from container env (`APISIX_ADMIN_KEY` set in both compose files) | VERIFIED |
| `${{PWA_ORIGIN:=…}}` | `infra/apisix/apisix.yaml` (CORS) | env expansion (dev) / `sync_routes.py` (prod) | VERIFIED |
| `${KEYCLOAK_*}` ×6 realm placeholders | `infra/keycloak/realm-h2fleet.json` | `infra/keycloak/substitute-realm.sh:15-31` sed loop + safety net `grep '\${KEYCLOAK_'` ⇒ exit 1 (`:33-37`); renderer refuses sed metacharacters | VERIFIED — every realm placeholder covered by the loop |
| `REPLACE_ME_provision_via_external_secrets…` ×4 | `infra/k8s/base/secret.yaml:14-19` | Documented external-secrets boundary (`infra/k8s/README.md` §Secrets): values must be provisioned via ExternalSecrets/Sealed Secrets/SOPS; base is never a real credential | VERIFIED (deploy-time contract documented) |
| `ghcr.io/munisp/h2fleet/<svc>:REPLACE_ME` ×13 | `infra/k8s/base/*.yaml` | Deliberate fail-fast tag; kustomize overlay pins `:dev`, release pipelines pin immutable tags (`infra/k8s/README.md` §Image tags) | VERIFIED |

---

## REAL verifications (sampled, no action)

- **Test mocks/fakes (408 hits):** `fakeLedger`/`fakePublisher`
  (`payments_test.go:27-49`), pgxmock pools, `mock JWKS` in
  `server_test.go`, TS `*.test.ts` mocks — all test-scoped; production
  constructors take real `*pgxpool.Pool`, TigerBeetle, HTTP clients.
- **PWA/mobile `placeholder=` attributes (60+ hits):** HTML input hints
  (e.g. `DrtPage.tsx:81-90`, `OnboardingScreen.tsx:192-228`,
  `personas.ts:70-181`); loading skeletons (`ui.tsx:305-320`); Suspense
  fallbacks (`App.tsx:43`). UI affordances, not data fabrication.
- **Rule-based planners:** `fleet-api/internal/handlers/operations.go:76`
  (range estimate fallback per SPEC §4), `citizen-api/internal/handlers/passenger.go:158`
  (fallback planner serving both stops) — real deterministic algorithms.
- **Comments referencing old hardcoded data:** `packages/db/src/schema/fleet.ts:69`,
  `migrations/0005:308-312` document that stops/routes are now DB-first.
- **Observability placeholders:** `prometheus.yml:70-77` commented-out
  kafka-exporter job; `alerts.yml:26-33` inert alert (expression final, no
  data source yet) — inert, documented, no fabricated signal.
- **`dummy` build cache trick:** `rust/telemetry-ingest/Dockerfile:10`.
- **Docs:** `docs/BUSINESS_LOGIC_AUDIT.md:261,333`, `docs/MIDDLEWARE.md:384,404,476`
  record *documented* gaps (Temporal settlement workflows, telemetry search,
  Fluvio bridge) — business-logic semantics owned by workstream A1; flagged
  to the lead, not redesigned here.
- **`xxx` token (45 hits):** every one is the account-range literal
  `1xxx/2xxx/3xxx/4xxx` from SPEC §3.4, not a marker.

---

## Gates

- **Go 1.24.5 toolchain (GOTOOLCHAIN=auto → 1.25.x, GOPROXY=goproxy.cn):**
  - `services/go/commerce-api`: `gofmt -l` clean · `go build ./...` OK ·
    `go vet ./...` OK · `go test -count=1 ./...` **ok** (handlers, ledger,
    mojaloop, gate).
  - `services/go/admin-api`: `gofmt -l` clean · `go build ./...` OK ·
    `go vet ./...` OK · `go test -count=1 ./...` **ok** (server, onboarding,
    kpi, ops).
- **Python:** `compileall` OK on `sync_routes.py`; functional render test
  3/3 (refuse-without-secret, substitute+drop-consumer, partner-key render).
- **YAML:** `docker-compose.yml`, `docker-compose.prod.yml` (`!reset`-aware),
  `apisix.yaml`, `config.yaml`, `config.yaml.prod`, `secret.yaml`,
  `alerts.yml`, `prometheus.yml`, dev kustomization — all parse.
- **Scenarios:** `python3 tests/e2e/scenarios/validate_scenarios.py` —
  **OK, 64 checks passed (10 scenarios, 43 steps)**.

---

## Production-realness scores

| Area | Score | Basis |
|---|---|---|
| Commerce & finance (payments/wallets/trades/ledger/Mojaloop) | 10 | Real TigerBeetle default + fail-closed; real FSPIOP client; fabricated ids eliminated unless explicitly opted in |
| Identity & admin (Keycloak admin, onboarding, users) | 10 | Real Admin REST default + fail-closed startup; simulated client strictly opt-in |
| Gateway & secrets (APISIX, realm render, k8s secrets) | 9 | All REPLACE_ME covered by substitution or fail-fast; partner-key consumer now fail-closed. −1: k8s base secret.yaml still relies on operator-run external-secrets step (documented, not enforced) |
| Fleet/citizen/infra APIs | 9 | Real DB + Kafka paths; rule-based planners are real algorithms, SPEC-§4-labelled. −1: documented gaps (Temporal settlement, telemetry search) owned by A1 |
| ML & analytics | 9 | Labelled model/source disclosure everywhere (rules-v1, seed, SYNTHETIC). −1: synth is the trainer's default `--source` (offline tool, labelled in metrics) |
| Telemetry ingest & simulator | 10 | Simulator is the declared sensor boundary, envelopes labelled; Rust ingest path real |
| Frontend (PWA/mobile) | 10 | Mock identity impossible in prod build; banner + no token in dev |
| Observability | 8 | Real scrape/alert configs; two documented inert placeholders pending kafka-exporter |

**Composite: 9.4 / 10**

---

## Verdict

Every code path in this repo is one of: **REAL** / **DEV-ONLY-FALLBACK**
(env-gated, listed in the gate table above — `H2_SIMULATED_LEDGER`,
`H2_SIMULATED_MOJALOOP`, `H2_SIMULATED_KEYCLOAK`, `WALLET_TOPUP_ENABLED`,
Vite-DEV mock identity, `PERMIFY_GRPC`-unset role-only, labelled ML/seed
fallbacks, telemetry-simulator boundary) / **EXTERNAL-SYSTEM-BOUNDARY**
(listed: mojaloop/simulator FSPIOP service, Keycloak realm renderer, APISIX
env expansion + etcd sync, k8s external-secrets contract, kafka-exporter
placeholder). **Zero silent mocks.**
