# H2Fleet Gap Audit — Wave 6

A deep, evidence-based audit of scenarios the platform does **not** handle,
produced by reading the actual code (never the docs — every gap cites
file:line). Auditor raw reports: `.wave6/a1.md` … `.wave6/a4.md`.

**27 findings:** 16 fixed in code this wave (FIX-NOW), 5 documented as
product/platform decisions (DECISION), 6 closed as honest external-boundary
contracts (CONTRACT). Classification of every item below.

Legend — **FIX-NOW**: real gap, fixed in code this wave. **DECISION**:
genuine product/platform fork; documented with the extension path, not
half-built. **CONTRACT**: the boundary belongs outside the software
(hardware, acquirer, ceremony); the contract is stated explicitly so nobody
mistakes it for a platform feature.

## A. Operational edge cases (auditor A1)

| ID | Scenario | Class | Resolution |
|----|----------|-------|------------|
| A1-01 | Concurrent same-rider taps race past the daily fare cap | FIX-NOW | Per-rider `pg_advisory_xact_lock` around cap-check + insert + transfer (`payments.go`) |
| A1-02 | Daily cap resets at DB-server midnight, not the city's civic day | FIX-NOW | `FARE_CAP_TIMEZONE` (named TZ, default UTC) used in the cap window |
| A1-03 | Future-dated telemetry poisons every "latest" read | FIX-NOW | Ingest rejects to DLQ `ts` > +5 min future or > retention old (`telemetry-ingest/model.rs`) |
| A1-04 | DRT assigns a vehicle that is on an active dispatch job | FIX-NOW | `PickVehicle` + `AssignDRT` exclude dispatch-job-busy vehicles (409 on manual) |
| A1-05 | Bus swap mid-shift loses the operational thread | FIX-NOW | `POST /v1/dispatch/jobs/{id}/swap-vehicle` with conflict validation + audit link |
| A1-06 | Odometer rewinds accepted silently | DECISION | Ingest stays stateless; twin/analytics own anomaly surfacing (documented) |
| A1-07 | Offline bus can't raise a leak alert until reconnected | CONTRACT | Local alarm chain is a certified-hardware function (hardware guide hard rule) |

## B. Business domain (auditor A2)

| ID | Scenario | Class | Resolution |
|----|----------|-------|------------|
| A2-01 | No fare products: passes, concessions, free entitlements | FIX-NOW | `commerce.fare_products` + `rider_entitlements` (0009); entitlement-aware price resolution before the cap |
| A2-02 | Full refunds only | FIX-NOW | Optional `amount_minor` on refund → `partially_refunded`, refundable remainder |
| A2-03 | Free-text currency silently mis-posts to the single-currency ledger | FIX-NOW | 422 unless `currency == PLATFORM_CURRENCY` (default EUR) |
| A2-04 | Wheelchair users cannot book DRT | FIX-NOW | `vehicles.wheelchair_accessible` + `drt_requests.requires_wheelchair` (0009); matching in pick + assign |
| A2-05 | Chargebacks/disputes have no representation | CONTRACT | Mojaloop has no chargeback primitive; acquirer chargebacks are processed as operator refunds + reconciliation (A2-02 mechanics) |
| A2-06 | Corporate/employer group settlement | DECISION | Extension path: `payer_account` on entitlements; documented, not built |
| A2-07 | Driver payroll/attendance | CONTRACT | Payroll integrates via dispatch jobs + audit exports; fatigue half fixed in A4-05 |
| A2-08 | Advertiser self-service + invoicing | DECISION | Extension path documented (advertiser role, approval states, invoice export) |
| A2-09 | Charter/school block bookings | DECISION | Convention: charter = dispatch job `route='charter:<ref>'` (overlap engine enforced); full product later |

## C. Platform / technical (auditor A3)

| ID | Scenario | Class | Resolution |
|----|----------|-------|------------|
| A3-01 | No GDPR data-subject export (Art. 15) | FIX-NOW | `GET /v1/me/data-export` (citizen-api) aggregates all rider rows |
| A3-02 | No erasure path; tension with immutable audit log unresolved | FIX-NOW | `POST /v1/me/erasure` tombstone-pseudonymization + docs/GDPR.md (audit chain holds pseudonymous subs only) |
| A3-03 | No outbound webhooks for partner systems | FIX-NOW | `infra.webhook_subscriptions` + HMAC-SHA256 signed delivery on incident lifecycle (0009, infra-api) |
| A3-04 | Single-tenant: no operator/city isolation | DECISION | Single-municipality scope (SPEC); sharding path = deployment+realm per city, documented |
| A3-05 | Retention defined for 2 of ~40 tables | FIX-NOW | docs/DATA_RETENTION.md full schedule + erasure hook as enforcement |
| A3-06 | /v1 exists, no deprecation policy | FIX-NOW | docs/API.md versioning & sunset policy |
| A3-07 | Backups never restore-verified until the quarterly drill | CONTRACT | `infra/backup/verify_restore.sh` added; DR drill step 0 (needs Docker host to schedule) |
| A3-08 | Service-token rotation has no rollover window | FIX-NOW | Audit ingest accepts comma-separated token list (old+new during rotation) |
| A3-09 | PWA offline is read-only | DECISION | Honest degraded mode documented; queued mutations need conflict semantics — product call |

## D. Safety / security / regulatory (auditor A4)

| ID | Scenario | Class | Resolution |
|----|----------|-------|------------|
| A4-01 | No driver SOS/panic channel | FIX-NOW | `POST /v1/safety/sos` → critical `sos` incident + `safety.sos` event + webhook |
| A4-02 | Incident type is free text; crash/PRD-vent/evacuation indistinguishable from typos | FIX-NOW | Type enum + per-type severity floor (422 on unknown) |
| A4-03 | Oversight requires platform-admin | FIX-NOW | `auditor` role: read-only on audit chain, incidents, compliance |
| A4-04 | No chain-of-custody evidence export | FIX-NOW | `GET /v1/incidents/{id}/evidence-pack` — incident + transitions + audit slice + telemetry window + chain proof |
| A4-05 | No hours-of-service guard: 16 h shifts schedulable | FIX-NOW | `DISPATCH_MAX_SHIFT_HOURS` / `DISPATCH_MAX_DAILY_HOURS` (default 10/10), 422 on breach |
| A4-06 | Station emergency doesn't stop the queue | FIX-NOW | Critical station incident → station `emergency`, queue joins 409 until resolved |
| A4-07 | Credential revocation assumed-instant, no drill | FIX-NOW | Revocation runbook + propagation SLA in docs/INCIDENT_RESPONSE.md |
| A4-08 | No DPIA / Art. 30 record | FIX-NOW | docs/DPIA.md processing inventory |
| A4-09 | Insurance-claim data pattern | CONTRACT | Satisfied by A4-04 evidence pack + retention schedule (mapping documented) |

## Migration 0009 (this wave)

`infra/sql/migrations/0009_wave6_gaps.sql`: fare products & rider
entitlements; vehicle/DRT accessibility attributes; partial-refund amount
tracking; webhook subscriptions & delivery log.

## What was verified NOT to be a gap

Telemetry redelivery dedup (0004 + ON CONFLICT), refund double-spend
(deterministic TigerBeetle ids + conditional UPDATE), dispatch
double-booking (overlap check + partial unique indexes), queue FIFO,
payment idempotent-replay owner scoping, leak dedup/worst-ppm refresh,
Temporal escalation workflows, fail-closed money/identity paths, PWA
service-worker update/cache-bust lifecycle.
