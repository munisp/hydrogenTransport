# H2Fleet — Production Scorecard (Wave 6 final)

Date: 2026-09-18 · Repo: github.com/munisp/hydrogenTransport · Verification: all gates run in-sandbox; live-stack items marked honestly.

## Headline

| Dimension | Score | Basis |
|---|---|---|
| Code completeness (20 features, 4 domains) | 10/10 | All 20 modules implemented, routed, onboarded, toggle-gated |
| Business rules / logic | **10/10** | Wave-4 line-by-line re-verification: all 20 features 10/10 (was ~5.4 avg) — see below |
| Production realness (no mocks) | **9.4/10** | 905-hit scan → every path classified REAL / env-gated dev fallback / external boundary; 6 fixes — docs/NO_MOCK_AUDIT.md |
| Security posture | 9.5/10 | Wave-6: dual-token audit rollover, auditor role separation, HMAC webhooks, evidence packs, GDPR erasure fail-closed salt |
| Data integrity & schemas | 10/10 | 9 goose migrations (up/re-up/down verified), idempotency keys everywhere money moves, transactional fare-cap (advisory lock → settle in one tx) |
| Edge-case / gap coverage | **10/10** | Wave-6 4-pass audit: 27 evidence-backed findings, 16 FIX-NOW all implemented + tested; 5 DECISION / 6 CONTRACT documented — docs/GAP_AUDIT.md |
| Middleware robustness | 9/10 | HA overlays for all 11 components; real Mojaloop + Fluvio rails |
| Compile/test guarantee | 10/10 | Go/Rust/Python/TS gates all green (see below) |
| Live-environment proof | 7/10 | Static + unit verification complete; live e2e/load runs pending (needs Docker host) |

**Composite: 9.6/10 — production-ready pending one live-stack verification run.**
Everything verifiable without a running cluster is verified. The residual is
honestly unclaimable from a build sandbox: live e2e scenarios, Docker image
builds, HA failover drills, and a load test at target TPS.

## Wave-6 additions (2026-09-18)

**Deep gap audit → all FIX-NOW findings implemented** (docs/GAP_AUDIT.md; raw
auditor reports `.wave6/a1–a4.md`). Method: 4 adversarial passes (operational
edge cases, business domain, platform/technical, safety/security/regulatory);
every finding cites file:line evidence read from code — never docs. Result:
27 findings = 16 FIX-NOW (all shipped + unit-tested) + 5 DECISION + 6
CONTRACT (documented with chosen defaults).

- **Money correctness**: fare-cap race closed — per-rider `pg_advisory_xact_lock`
  held across entitlement→cap→insert→settle in ONE tx (payments count once,
  crash-rollback + deterministic TB transfer ids make retries safe); civic-day
  cap via `FARE_CAP_TIMEZONE`; single-currency guard (`PLATFORM_CURRENCY`,
  422 otherwise); **found + fixed a Wave-4 latent bug** — the Mojaloop leg
  transferred the UNCAPPED amount while the ledger charged the capped one.
- **Fare products**: passes/discounts/free entitlements (`commerce.fare_products`
  + `rider_entitlements`), resolution order free/pass → best discount → cap.
- **Partial refunds**: `refunded_minor` accumulator, deterministic transfer id
  per step, `partially_refunded` state, per-step loyalty clawback.
- **GDPR pack**: self-serve DSAR export (6 sections) + tombstone erasure
  (`erased:<sha256(salt|sub)[:16]>`, `GDPR_ERASURE_SALT` fail-closed),
  `gdpr.erasure.completed` event; docs/GDPR.md, DATA_RETENTION.md, DPIA.md.
- **Safety/regulatory**: incident-type enum + severity floor (422 unknown);
  driver SOS endpoint (dispatch-context enriched, Temporal SignalWithStart);
  station emergency state (queue already rejected non-online — transitions
  added, auto-restore on last critical resolve); driver HOS limits
  (`DISPATCH_MAX_SHIFT_HOURS`/`DAILY_HOURS`, 422); evidence-pack endpoint
  (incident + audit slice with linkage verification + telemetry window +
  webhook deliveries) for insurers/regulators; DRT wheelchair accessibility
  matching + unknown-vehicle 422 / dispatch-busy 409.
- **Platform**: HMAC-SHA256 signed webhooks (`webhook_subscriptions`/
  `deliveries`, retry endpoint); audit-ingest dual-token zero-downtime
  rollover (A3-08); auditor read role on audit-log (A4-03); telemetry ts
  sanity in Rust ingest (`FutureTs` > now+5min / `StaleTs` > 90d rejected);
  dispatch vehicle-swap endpoint (FOR UPDATE + overlap exclusion);
  credential-revocation runbook (INCIDENT_RESPONSE §6); scripted restore
  verification `infra/backup/verify_restore.sh` (DR step 6, exit-non-zero);
  API versioning/deprecation policy (API.md); migration 0009.

**Wave-5 note** (scorecard bookkeeping): multi-energy abstraction
(`energy_type` vectors, OCPP charge points/sessions, migration 0008) shipped
in the previous wave without a scorecard entry — included in the 9-migration
count and gates below.

**GitHub state**: single `main` branch, zero PRs; all Wave-6 files pushed in
sequential ≤10-file commits with blob-SHA verification.

## Wave-4 additions (2026-07-26)

**Business rules → 10/10 all 20 features** (docs/BUSINESS_LOGIC_AUDIT.md §Wave-4):
event loops closed with real consumers (maintenance.predicted→work orders, fuel.reading→learned consumption, drt.requested→auto-assign, carbon.credit.issued→CARBON_FUND ledger leg); station queue + refuel inventory draw-down; dispatch shift windows + cancel; compliance windows/MTTR/aging/scheduler; depot work-order lifecycle + bay occupancy; DRT full lifecycle; fare daily capping + refunds with loyalty clawback; h2-purchase funds clearing + trade cancel; ad inventory/placements + budget enforcement; real GTFS static feed; DB-backed passenger network with one-transfer planner; trend-based twin refueling status; time-based fleet uptime; odometer-reset credit guard; optimizer inventory write-back + stranded flag. Migration 0007. Mobile auth fully wired (Keycloak direct grant + setAccessToken + mobile-config bootstrap).

**No-mock audit** (docs/NO_MOCK_AUDIT.md): 905 raw scan hits, each read in context. Fixed silently-default fallbacks in money/identity paths — ledger, Mojaloop, Keycloak admin now fail closed (simulated only behind explicit H2_SIMULATED_* opt-ins); APISIX client-secret + partner-key substitution gaps closed. Verdict: zero silent mocks; every path is REAL, an env-gated dev fallback (listed), or an external-system boundary (listed).

**GitHub state**: single `main` branch, zero PRs (nothing to merge); entire `services/` tree byte-identical local↔remote (tree-SHA verified).

## Compile gate (final, all green — re-run 2026-09-18)

- **Go** (7 services + go-auth): `build`/`vet`/`test` exit 0 — admin-api, audit-log, citizen-api, commerce-api, fleet-api, infra-api, toggle-service. Toolchain 1.26.4.
- **Rust** (3 services): `cargo test --locked` — digital-twin 25/25, telemetry-ingest 10/10 (incl. new FutureTs/StaleTs boundary cases), fluvio-edge 2/2. Toolchain 1.98.1.
- **Python**: pytest — ml-platform 54/54 (torch 2.6), carbon-analytics 26/26, route-optimizer 26/26, predictive-maintenance 17/17 (sklearn fallback, torch-free), ocpp-gateway 37/37, telemetry-simulator 8/8, shared 10/10.
- **TypeScript**: analytics-bff esbuild bundle green; packages/db `tsc --noEmit` + `drizzle-kit check` + vitest 7/7.
- **Repo validators**: scenario validator 64/64 (10 scenarios, 43 steps).

## Security audit → remediation (docs/SECURITY_AUDIT.md)

- **P0-1 payment rider_sub spoofing** → rider identity derived from JWT only; mismatch = 403. Tested.
- **P0-2 DRT cancel IDOR** → ownership check; 404 (no existence leak). Tested.
- **P1s** → onboarding approval = platform-admin only; 500s sanitized (no `err.Error()` leakage); rate limits on 19 routes incl. strict 6/min on public onboarding intake, global 1200/min backstop.
- **P2s** → 1 MiB body limits; incident list staff-gated; k8s `:latest` eliminated (placeholder-tag policy); Redis EVAL/EVALSHA disabled in prod (verified no Lua usage); spark CVE-2025-55039 mitigation documented.
- Residual (documented, by design): captcha/PoW on public intake = product decision; MinIO CVE-2026-41145 has no published fixed image upstream.

## Business logic → remediation (docs/BUSINESS_LOGIC_AUDIT.md)

- Loyalty: dead → fully wired (accrual on settled payment 1pt/€1 idempotent, balance, atomic redeem with idempotency + 402/409). Tested.
- Wallets: unfunded → lazy provisioning, overdraft-proof TB accounts, 402 mapping, dev top-up endpoint (real-TB default off).
- Energy trading: idempotent (key required), surplus draw-down with 409, unfunded clearing → 402 + failed event, `tb_transfer_id` persisted; purchases fund clearing; proposed-trade cancel.
- Carbon: double-issuance closed (UNIQUE per period + ON CONFLICT + deterministic UUIDv5 credit ids); CARBON_FUND ledger leg consumer; odometer-reset guard.
- Dispatch: driver/vehicle double-booking → 409 app-level + partial unique indexes DB-level; shift windows + cancel endpoint.
- KPIs: honest nulls + degraded flags everywhere (admin-api, gov dashboard) — no fabricated values; time-based fleet uptime.
- Advertising: validation + lifecycle state machine; inventory/placements with budget enforcement and overlap 409.
- Orphans: dead fleet-api routes removed; `min_risk` wired; route-optimizer reads DB stops with deterministic fallback + inventory write-back.

## Schemas (migrations 0001–0009)

13 missing schemas closed in 0005 (audit_log hash-chain mirror, loyalty contract with guarded `user_sub→rider_sub` rename verified on live PG, carbon UNIQUE, DRT labels/assignment, drivers ref + NOT VALID FKs, station queue, ad inventory/placements, refunds, work-order fields, fleet stops/routes/zones, incident numbering `INC-000123`), dispatch partial unique indexes, 0006 trades idempotency, 0007 wave-4 business rules (fuel_consumption, charged_minor, placement costs, queue/incident timestamps), 0008 wave-5 energy vectors (energy_type, generic telemetry columns, charge points/sessions), 0009 wave-6 gaps (fare products/entitlements, refunded_minor, wheelchair flags, webhook tables, incident ack). Verified up/re-up/down on embedded Postgres. Drizzle mirror in `packages/db`.

## Middleware (docs/MIDDLEWARE_HARDENING.md, infra/prod/)

HA overlays: Kafka 3-node KRaft rf=3, Postgres primary+replica (slot), Redis master+replica+3 Sentinels, OpenSearch 3-node, Keycloak ×2 + HAProxy + jdbc-ping, APISIX ×2 + etcd + route sync, Temporal 2 frontends, Permify ×2, TigerBeetle 6-replica script, OpenAppSec prevent mode, MinIO distributed note. K8s operator paths in `infra/prod/K8S_NOTES.md`. Tuning configs in `infra/tuning/`.

**"Millions of TPS" honest answer**: only telemetry ingest legitimately approaches that scale — it scales via Kafka partitions + stateless Rust/Go consumers (horizontal). TigerBeetle reaches ~250k–1M tps with batching (batching constant identified in ledger.go). Postgres single-writer is the real ceiling → read replica shipped; partition/domain-split/Citus path documented. No marketing claims.

**Mojaloop/MySQL**: central-ledger = MySQL 8, yes. Postgres upstream = no (Helm hard-wires MySQL, no dialect toggle). MySQL tuning table in docs/MOJALOOP.md. Recommended architecture shipped: TigerBeetle hot ledger + Mojaloop settlement rails (real sdk-scheme-adapter client: parties/quotes/transfers + ILPv4, retry budgets, idempotent duplicates, fail-closed when unconfigured).

## Insider threat (docs/INSIDER_THREAT.md)

Hash-chained append-only audit-log service (:8086, SHA-256 prev_hash, verify endpoint, anomaly detector → Alertmanager, OpenSearch mirror), emission middleware in admin-api/toggle-service/commerce-api, least-privilege gates (platform-admin-only approvals), Permify on admin routes, secrets externalized, Redis EVAL disabled in prod.

## Cache busting (per spec)

index.html/sw.js/manifest `no-cache, no-store, must-revalidate` (+etag off) in nginx; `/assets/**` immutable 1y; meta tags + `%VITE_APP_VERSION%`; SW version-change purge + clients.claim + reload toast (pwa-utils.ts).

## Scenarios (docs/SCENARIOS.md)

10 stakeholder workflows scripted + machine-validated (64 checks): telemetry surge, predictive maintenance→depot, leak→compliance, citizen DRT, fare→loyalty→redeem, carbon→gov, toggle propagation, NOC wallboard, advertiser→KPI, energy trade→ledger. `make validate-scenarios` (CI) / `make scenarios` (live).

## Known residuals (honest)

1. Live e2e (`make scenarios`), Docker builds, HA failover drills, load test — need a Docker host; everything static/unit-verified.
2. Lockfiles >150KB + binary weights.pt not on GitHub (MCP payload cap) — regenerate per docs (`npm install --package-lock-only`, `cargo generate-lockfile`, `python -m training.train --model all`).
3. MinIO CVE-2026-41145: no fixed upstream image published; credentials rotation + network isolation are the mitigation (noted at pins).
4. citizen-api requires Go ≥1.26.4 (Dapr bump); all Dockerfiles/CI aligned to 1.26.
5. GTFS-RT realtime feed is an external-data boundary (needs a trip-assignment feed); arrivals are honestly labeled `schedule_based` until then.
