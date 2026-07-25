# BUSINESS_LOGIC_AUDIT.md — H2Fleet Business-Logic & Data-Flow Audit

Scope: all 20 SPEC.md modules across go/rust/python services and the PWA/mobile API clients.
Method: full read of every service handler/consumer + client-side call inventory + schema diff
against `infra/sql/migrations/*.sql`. Every claim cites `file:line`.

Legend: **Score** = production-readiness 0–10 (10 = rule complete, wired end-to-end, persistence consistent).

---

## Master Score Table

| # | Module | Score | Top defect |
|---|--------|-------|------------|
| 1 | telematics | 8 | Redis enrichment key `bus:meta:<bus_id>` is read but never written → route/depot/heading always null |
| 2 | predictive-maintenance | 6 | `maintenance.predicted` published but never consumed → no work order, open loop |
| 3 | digital-twin | 4 | Snapshot INSERT uses column `ts` that does not exist → every Postgres snapshot fails |
| 4 | fuel-monitoring | 6 | Range math uses one fleet-wide constant (8 kg/100km); `fuel.reading` event never produced |
| 5 | route-energy-optimizer | 7 | Stops are randomly generated per date, not real route data; inventory not persisted |
| 6 | refueling-stations | 5 | No queue management at all (SPEC requires "queue mgmt") |
| 7 | leak-detection | 7 | No ppm→severity mapping; severity taken verbatim from caller; no per-sensor dedup |
| 8 | dispatch-workforce | 6 | No conflict checks: same driver/vehicle can be double-booked; `shift_end` always null |
| 9 | compliance-reporting | 6 | Report = incidents+stations rollup only; no maintenance/fleet/regulatory content |
| 10 | depot-management | 5 | Work orders open→closed only; no bay assignment endpoint (`occupied_by` never set) |
| 11 | passenger-pwa | 5 | Stops/routes/alerts hardcoded in Go source, not DB; planner has no transfers |
| 12 | mobile-app | 5 | `setAccessToken` never called → every authenticated mobile flow 401s |
| 13 | demand-responsive | 3 | No assignment logic: `drt.requested` never consumed; statuses beyond requested/cancelled unreachable |
| 14 | carbon-credits | 6 | No `UNIQUE(period)` → concurrent double issuance; recompute republishes event with new UUID |
| 15 | open-data-portal | 5 | No actual GTFS/GTFS-RT feed; catalog is 4 static entries pointing at JSON APIs |
| 16 | fare-payments | 6 | No wallet funding or balance enforcement: real TB allows unbacked spend, simulated TB fails every payment |
| 17 | loyalty-marketplace | 3 | No accrual path anywhere — balances stay 0 forever, redeem always 409s; no redemption records |
| 18 | energy-trading | 4 | Ledger direction conjures revenue (debit platform 3001 → credit 2001); surplus never checked vs stations |
| 19 | gov-dashboard | 7 | Uptime = static active/total vehicle ratio, not time-based availability |
| 20 | advertising | 3 | No ad-inventory entity/endpoints; no date/overlap/status-transition validation; budget never tracked |

---

## Domain 1 — Fleet Operations & Telematics

### 1. telematics — Score 8
**Rule accuracy.** Ingestion is genuinely production-grade: envelope type check + plausibility
validation with poison-record skip (`services/rust/telemetry-ingest/src/pipeline.rs:145-163`),
batched `unnest` insert with `ON CONFLICT DO NOTHING` dedup against `UNIQUE(bus_id, ts)`
(`store.rs:63-76`, migration `0004_telemetry_dedup.sql`), bounded retry → DLQ with offset commit
(`pipeline.rs:182-213`), backpressure via poll-stop (`pipeline.rs:1-6`). Query side: time-windowed
history (`fleet-api/internal/handlers/vehicles.go:112-152`) and per-bus latest via `DISTINCT ON`
(`vehicles.go:170-199`), consumed by `LiveFleetMapPage` (`apps/pwa/src/api/fleet.ts:25-28`).
**Defects.**
- **Redis enrichment is dead**: `telemetry-ingest` reads `HGETALL bus:meta:<bus_id>` for
  route/depot/heading (`store.rs:12-26`) but nothing in the repo ever writes that key (comment
  claims "fleet-api / seed jobs"; grep finds no writer) → `route_id`, `depot_id`, `heading_deg`
  are always null in `telemetry.enriched` and in twin state.
- `GET /v1/vehicles/{id}/telemetry` (vehicles.go:112) and `GET /v1/vehicles/{id}` (vehicles.go:83)
  have no caller (PWA uses `/v1/telemetry/latest` only; `getVehicle` in fleet.ts:21 is unused).

### 2. predictive-maintenance — Score 6
**Rule accuracy.** Real closed scoring path: Kafka loop tracks active buses from
`telemetry.enriched`, scores every `scoring_interval_s` (300s), persists
`fleet.maintenance_predictions`, publishes `maintenance.predicted` when
`risk_score >= 0.7` (`services/python/predictive-maintenance/app/events.py:61-88`,
`config.py:24-25`). Model preference LSTM → sklearn → deterministic rules with plausible H2
priors (`app/model.py:28-74`). On-demand `POST /v1/predict` persists too
(`app/main.py:127-132`). fleet-api exposes predictions sorted by risk
(`fleet-api/internal/handlers/operations.go:21-55`); PWA `MaintenancePage` lists and triggers
(`apps/pwa/src/api/fleet.ts:30-51`).
**Defects.**
- **No closed loop**: `maintenance.predicted` has zero consumers in the repo. No work order
  (`infra.work_orders`), no incident, no notification is ever created from a high-risk
  prediction. The feature ends at a list page.
- PWA sends `min_risk` filter (`fleet.ts:36`) which the backend silently ignores
  (operations.go:27-31 only honors `bus_id`).
- Consumer uses `enable_auto_commit=True` + `auto_offset_reset=latest` (events.py:94-100):
  scoring lag after downtime is silently skipped (acceptable, but worth noting).

### 3. digital-twin — Score 4
**Rule accuracy.** Redis hot state `twin:<bus_id>` with TTL, index set `twin:buses`,
`twin.updated` fan-out, staleness semantics are explicit and unit-tested: TTL-expired/corrupt/
non-UUID index entries are pruned and never snapshotted
(`services/rust/digital-twin/src/twin.rs:176-240`). Read API returns 404 for expired twins
(`api.rs:54-70`). Status derivation (`model.rs:67-75`): moving / idle / "refueling".
**Defects.**
- **P0 schema break**: snapshot insert is `INSERT INTO fleet.twin_snapshots (bus_id, ts, state)`
  (`twin.rs:226-231`) but the table is `(id, bus_id, state, updated_at)` — no `ts` column
  (`infra/sql/migrations/0001_core.sql:72-78`, same in `001_init.sql:76`). Every snapshot batch
  fails with "column ts does not exist" (logged and swallowed at `twin.rs:171`), so twin history
  is never persisted and `fleet.twin_snapshots` is a write-only-never-succeeds table that no API
  reads anyway.
- "refueling" status is really "stationary with low H2" (`model.rs:70-71`) — a stranded,
  run-dry bus is mislabeled as refueling.
- fleet-api proxies `GET /v1/vehicles/{id}/twin` (`fleet-api/internal/handlers/proxy.go:36-39`)
  but the PWA calls the twin service directly via `/api/twin` (`apps/pwa/src/api/fleet.ts:58-65`)
  → proxy route is orphaned.

### 4. fuel-monitoring — Score 6
**Rule accuracy.** Range math is dimensionally correct: `remaining_kg = pct/100 × capacity`,
`range_km = remaining_kg × 100 / 8.0` (`fleet-api/internal/handlers/operations.go:57-103`),
per-bus capacity from `fleet.vehicles.h2_capacity_kg`, latest reading via `DISTINCT ON`.
PWA `FuelMonitoringPage` wired (`apps/pwa/src/api/fleet.ts:53-56`).
**Defects.**
- Consumption is a single hardcoded fleet constant (`h2KgPer100Km = 8.0`, operations.go:59);
  no per-bus learned consumption from telemetry history, though the data exists
  (`odometer_km` deltas + `h2_level_pct` deltas in `fleet.telemetry`).
- SPEC topic `fuel.reading` (SPEC §3.3, `packages/events/asyncapi.yaml:47,212-214`,
  schema + fixture exist) is **never produced by anything and never consumed** — dead catalog
  entry; fleet-api computes levels synchronously instead.

### 5. route-energy-optimizer — Score 7
**Rule accuracy.** Real OR-Tools VRP: distance dimension capped per vehicle at
`range + one-refill headroom` (`services/python/route-optimizer/app/vrp.py:73-85`), vehicles
start at live positions and end at depot, unservable stops dropped with penalty and reported
(`vrp.py:106-130`). Phase-2 refuel planner honors `range_safety_km=20` (`config.py:16`), picks
nearest *reachable* station with stock, and decrements shared `available_kg` so concurrent
refuels across buses respect station inventory (`vrp.py:142-198`; note `main.py:88` passes the
same mutable `Station` objects per call, so the decrement is genuinely shared). H2 per bus from
latest telemetry (`data.py:33-56`), stations filtered `status='online' AND available_kg > 0`
(`data.py:59-68`). Solver runs in a thread (`main.py:126-130`). PWA wired
(`apps/pwa/src/api/fleet.ts:68-73`).
**Defects.**
- **Stops are fake**: 12 random waypoints regenerated per date (`data.py:71-85`) because no
  route-stops table exists (acknowledged in the module docstring, data.py:5-8). Optimization
  output does not reflect any real route.
- Planned refuels never write back to `infra.stations.available_kg` — inventory decrement is
  in-memory only, so consecutive optimize calls start from stale DB stock.
- `h2_end_kg` can be reported for infeasible plans without distinguishing "stranded" state
  beyond the notes list.

---

## Domain 2 — Infrastructure & Safety

### 6. refueling-stations — Score 5
**Rule accuracy.** CRUD + status PATCH with optional inventory update; publishes
`station.status.changed` (`services/go/infra-api/internal/handlers/stations.go:128-166`).
Stations feed the optimizer (`route-optimizer/app/data.py:59-68`) and gov KPIs
(`commerce-api/internal/handlers/dashboard.go:55-60`). PWA `StationsPage` lists
(`apps/pwa/src/api/infra.ts:14-17`).
**Defects.**
- **Queue management is absent** (SPEC §1: "station status, queue mgmt, inventory"): no queue
  table, no join/leave endpoints, no wait-time logic.
- `available_kg` is only ever changed by manual PATCH; nothing decrements it on refuel events
  or increments on delivery.
- `station.status.changed` has no consumer.
- `POST /v1/stations` and `PATCH /v1/stations/{id}/status` have no UI caller (orphan endpoints;
  operator-only mutations exist only as API).

### 7. leak-detection — Score 7
**Rule accuracy.** Genuinely closed loop: webhook `POST /v1/safety/leak` (sensor-token or JWT,
`infra-api/cmd/server/main.go:80-92`) → incident row → `safety.leak.detected` event → Temporal
`IncidentResponseWorkflow` via SignalWithStart (`handlers/incidents.go:171-214`,
`workflow/workflow.go:45-63`) → in_progress activity → 15-min ack escalation
(low→medium→high→critical, `workflow/activities.go:42-66`) → resolve signal closes both DB and
workflow (`incidents.go:134-156`, `workflow/workflows.go:66-115`). PWA `SafetyPage` ack/resolve
wired (`apps/pwa/src/api/infra.ts:28-39`).
**Defects.**
- **No severity mapping**: webhook takes `severity` verbatim from the caller, defaulting to
  `high` (`incidents.go:177-179`); `h2_ppm` is stored in meta but never used to derive severity
  (e.g. ppm bands) — the documented ml-platform `leak_autoencoder` (`POST /v1/ml/leak/score`,
  `services/python/ml-platform/app/main.py:118`) is never invoked by this flow.
- No dedup: every sensor event opens a new incident; a flapping sensor floods `infra.incidents`.
- `safety.leak.detected` Kafka event itself has no consumer (the workflow is driven by direct
  Temporal signals, not the topic).

### 8. dispatch-workforce — Score 6
**Rule accuracy.** Job lifecycle with real Temporal semantics: assign → 10-min accept timeout →
requeue activity → loop; cancel/accept signals (`workflow/workflows.go:124-184`,
`activities.go:84-94`); `dispatch.job.assigned` published per event schema
(`handlers/dispatch.go:97-115`). PWA `DispatchPage` lists, mobile `DriverScreen` lists+accepts
(`apps/mobile/src/api/client.ts:173-180`).
**Defects.**
- **No conflict checks**: `CreateDispatchJob` (`dispatch.go:80-95`) inserts unconditionally —
  the same driver can be assigned two overlapping jobs, the same vehicle double-booked, and
  `vehicle_id` is not validated against `fleet.vehicles`.
- `starts_at` has no `ends_at`/`shift_end` column; the event schema field `shift_end` is
  published as literal null with a code comment admitting "no source column yet"
  (dispatch.go:97-108) → overlap checking is not even expressible today.
- No cancel endpoint, although the workflow handles `job-cancelled`
  (`workflows.go:29,141-149`) → dead signal path.
- Mobile passes `?driver_sub=` filter (`client.ts:173-176`) which the backend ignores
  (dispatch.go:41-45 filters only on `status`) — drivers see everyone's jobs.

### 9. compliance-reporting — Score 6
**Rule accuracy.** Report content is real SQL, not a stub: incidents by status, incidents by
severity over 30d, station count/capacity/available (`handlers/compliance.go:69-143`), persisted
to `infra.compliance_reports` and listed/retrieved by the PWA `CompliancePage`
(`apps/pwa/src/api/infra.ts:73-84`). Generation is Permify-guarded (`cmd/server/main.go:128-131`).
**Defects.**
- Content is thin vs "regulatory & safety compliance": no maintenance-prediction backlog, no
  unresolved-leak aging/MTTR, no fleet availability, no period parameter (report is always
  "now"; the 30-day window is hardcoded, compliance.go:100).
- No scheduled generation (manual POST only).

### 10. depot-management — Score 5
**Rule accuracy.** Bays seeded with kinds (fueling/charging/parking/workshop), work orders
create/list/close with correct state guard (`handlers/depot.go:118-155`,
`handlers/common.go:58-74`). PWA `DepotPage` wired read-only (`apps/pwa/src/api/infra.ts:86-94`).
**Defects.**
- Lifecycle is `open → closed` only: no assign/start/hold statuses, no assignee, no parts, no
  `bus_id`/prediction linkage — so predictive maintenance and incidents can never spawn work
  orders structurally.
- No endpoint ever sets `depot_bays.occupied_by` or changes bay status → bay occupancy is
  static seed data forever (`common.go:67-74`).
- `POST /v1/depot/work-orders` and `POST .../close` have no UI caller (orphan endpoints).

---

## Domain 3 — Citizen & Engagement

### 11. passenger-pwa — Score 5
**Rule accuracy.** Headway-anchored schedule math is correct and deterministic
(`citizen-api/internal/handlers/passenger.go:119-144`); arrivals merge across routes, journey
planner finds direct routes serving both stops in order (`passenger.go:159-202`); PWA
`PassengerPage` + mobile `ArrivalsScreen`/`AlertsScreen` consume the same endpoints.
**Defects.**
- **The entire GTFS dataset is hardcoded Go literals** — 8 stops, 3 routes, 2 alerts
  (`passenger.go:31-49, 215-233`) — no `gtfs_*`/route tables exist; alerts' `ActiveUntil` is
  `time.Now()+30d` evaluated at process start, so they never expire while the process runs.
- Journey planner is direct-route only; no transfers, no walking legs.
- No GTFS-RT: arrivals are purely scheduled, never adjusted from live telemetry despite the
  data being available.

### 12. mobile-app — Score 5
**Rule accuracy.** Expo skeleton reuses the real API contracts (`apps/mobile/src/api/client.ts`),
screens for arrivals/alerts/DRT/carbon/driver/onboarding exist.
**Defects.**
- **P0 wiring gap**: `setAccessToken` (client.ts:97) has zero call sites — no screen ever
  authenticates, so `createDrtRequest`, `acceptDispatchJob`, `reportIncident` (all JWT-required
  server-side) will 401 in any real deployment.
- `reportIncident` sends a `description` field (client.ts:181-190) that the backend drops
  (`createIncidentRequest` has no description/meta mapping, incidents.go:72-78) — operator never
  sees the driver's text. Same for PWA `reportIncident` (`apps/pwa/src/api/infra.ts:41-52`).
- `GET /v1/mobile/config` (passenger.go:250-259) is never called by the mobile app → orphan.

### 13. demand-responsive — Score 3
**Rule accuracy.** Create/list/cancel/get with good ownership semantics (subject-scoped,
404-not-403 for non-owners, `citizen-api/internal/handlers/drt.go:190-212`; operator override
gated on role, drt.go:117-120). `drt.requested` published per schema (drt.go:89-105).
**Defects.**
- **There is no assignment logic anywhere**: `drt.requested` has no consumer; statuses
  `assigned|enroute|completed` are unreachable (cancel guard references them, drt.go:169, but
  nothing can set them). DRT is request-CRUD only — no shuttle matching, no driver linkage, no
  ETA.
- PWA sends `pickup_label`, `dropoff_label`, `passengers` (`apps/pwa/src/api/citizen.ts:52-58`)
  which the backend struct lacks → silently dropped; no columns exist (missing schema).

### 14. carbon-credits — Score 6
**Rule accuracy.** Accounting method is sound: per-bus odometer deltas per period
(`services/python/carbon-analytics/app/core.py:28-36`), diesel baseline factor 1.2 kg CO₂/km,
credit = `credit_kg_co2` kg, strict `YYYY-MM` period validation with correct month/year bounds
(`core.py:26,50-61`), delete+insert in one transaction for recompute (`core.py:101-113`),
`carbon.credit.issued` published (`core.py:116-125`). Read APIs in both citizen-api and
carbon-analytics; PWA `CarbonPage` wired.
**Defects.**
- **Double-issuance race**: `citizen.carbon_credits` has no `UNIQUE(period)`
  (`infra/sql/migrations/0001_core.sql:115-121`); two concurrent computes for the same period
  both delete zero rows and insert two credit rows. App-level idempotency only.
- Recompute republishes `carbon.credit.issued` with a **new UUID and new credit_id** every time
  (`core.py:100,64-80`) → any downstream ledger/consumer would double-count; event is not
  reconcilable with the replaced issuance.
- No TigerBeetle `CARBON_FUND=4xxx` posting despite the account being bootstrapped
  (`commerce-api/internal/ledger/ledger.go:32,85`) — the carbon-fund ledger leg of SPEC §3.4 is
  unimplemented; `carbon.credit.issued` also has no consumer.
- Odometer reset on a bus (replacement/retrofit) silently inflates distance (max−min).

### 15. open-data-portal — Score 5
**Rule accuracy.** Catalog endpoint + OpenSearch proxy with query escaping via JSON body
(`citizen-api/internal/handlers/opendata.go:60-105`); bootstrap job creates the index with
explicit mappings and seeds 4 documents idempotently
(`services/python/opensearch-bootstrap/bootstrap.py:132-180`). PWA `OpenDataPage` lists the
catalog (`apps/pwa/src/api/citizen.ts:77-80`).
**Defects.**
- SPEC promises "GTFS/GTFS-RT + open datasets": no downloadable GTFS zip, no GTFS-RT feed; the
  "GTFS static feed" catalog entry links to the routes JSON API (opendata.go:47).
- `GET /v1/opendata/search` has no PWA/mobile caller (orphan); the index only ever holds the 4
  static catalog docs, so search is circular.

---

## Domain 4 — Commerce & Finance

### 16. fare-payments — Score 6
**Rule accuracy.** Strong parts: required `Idempotency-Key` with unique index and idempotent
replay scoped to the key owner (`commerce-api/internal/handlers/payments.go:61-107`,
`common.go:37-38`); deterministic TigerBeetle transfer ID derived from the key (SHA-256
namespaced) so retries cannot double-post (`ledger/ledger.go:43-48`); per-rider wallet accounts
persisted and allocated sequentially with race retry (`payments.go:204-235`); DB-first event
ordering (payments.go:160-170); owner-scoped reads with 404-not-403 (`payments.go:295-300`).
Ledger semantics correct in direction: **debit rider wallet (1xxx) → credit operator revenue
(2001)** (payments.go:135-137).
**Defects.**
- **No wallet funding exists and no balance enforcement works in either mode**: real TigerBeetle
  accounts are created with no `debits_must_not_exceed_credits` flag (`ledger.go:101-116`), so
  riders can pay from an empty wallet (balance goes negative unchecked); the simulated dev
  ledger does enforce non-negative balances (`ledger.go:187-190`) but wallets start at 0 and no
  top-up endpoint exists → **every payment fails in dev mode**. Either way the fare flow is not
  operable end-to-end.
- No refunds: status enum includes `refunded` (migration 0001:135) but no endpoint or transfer
  can set it; no `refund_of`/`refunded_at` columns.
- No fare capping (daily/weekly caps) — every ride is a full-price independent payment.
- Mojaloop leg is real-HTTP-when-configured and honestly labeled simulated otherwise
  (payments.go:243-274) — good; but `fare.payment.initiated/settled/failed` have no consumer
  (e.g. loyalty accrual, receipts).
- `POST /v1/payments` and `GET /v1/payments/{id}` have no client caller — the PWA `PaymentsPage`
  is list-only (`apps/pwa/src/api/commerce.ts:18-25`), so riders cannot actually pay from any UI.

### 17. loyalty-marketplace — Score 3
**Rule accuracy.** Redemption is transactional: offer locked `FOR UPDATE`, account ensured,
conditional `UPDATE ... WHERE points >= cost` (double-spend-safe at the balance level,
`handlers/loyalty.go:126-169`). PWA `MarketplacePage` lists offers + redeems
(`apps/pwa/src/api/commerce.ts:27-43`).
**Defects.**
- **No accrual path exists at all**: nothing in the repo increments
  `commerce.loyalty_accounts.points` — no endpoint, no `fare.payment.settled` consumer, no job.
  Every balance is 0 forever; every redeem returns 409 "insufficient points". The feature is
  dead end-to-end.
- No redemption records table → redeeming the same offer twice is untracked (balance guard
  allows it if points existed), no audit, no fulfillment handle for the partner.
- `GET /v1/loyalty/balance` (loyalty.go:23) and `POST /v1/marketplace/offers` (loyalty.go:89)
  have no client caller (orphans).

### 18. energy-trading — Score 4
**Rule accuracy.** A trade does reach TigerBeetle and publishes `energy.trade.executed` with the
transfer id (`handlers/trading.go:60-99`).
**Defects.**
- **Ledger semantics conjure money**: the transfer is debit `ENERGY_TRADE=3001` (a platform
  account starting at 0) → credit `OPERATOR_REVENUE=2001` (trading.go:68-70). Revenue appears
  from a platform clearing account that is never funded by any buyer — there is no buyer/counterparty
  account at all.
- **No physical backing**: `quantity_kg` is never checked against `infra.stations.available_kg`
  (surplus H2) nor decremented; you can "sell" unlimited hydrogen.
- Ordering hazard: ledger transfer executes **before** the DB insert (trading.go:67-85); a DB
  failure leaves an orphan ledger transfer with no trade row, and `commerce.trades` has no
  `tb_transfer_id` column to reconcile (missing column).
- SPEC §3.8 settlement workflows (Temporal) unimplemented; statuses `proposed|settled|cancelled`
  unreachable (only `executed|failed` written). `energy.trade.executed` has no consumer.

### 19. gov-dashboard — Score 7
**Rule accuracy.** Real cross-schema rollups: 30d settled revenue/payments, carbon totals,
fleet active ratio, station inventory, open incidents
(`handlers/dashboard.go:23-70`); PWA `GovDashboardPage` wired
(`apps/pwa/src/api/commerce.ts:13-16`). Honest labeling of the ridership estimate
(dashboard.go:35-36).
**Defects.**
- `fleet_uptime_pct` = active/total vehicles (dashboard.go:45-53) — a static status ratio, not
  time-based availability from telemetry/incidents; a bus broken for 29 days but status
  'active' counts as 100% uptime.
- Open-incident count excludes `in_progress` (dashboard.go:63) — incidents being worked vanish
  from the KPI.

### 20. advertising — Score 3
**Rule accuracy.** Campaign CRUD with a status enum (`handlers/advertising.go`). PWA
`AdvertisingPage` lists campaigns (`apps/pwa/src/api/commerce.ts:61-64`).
**Defects.**
- **No ad inventory entity at all** (SPEC: "on-bus/digital ad inventory & campaigns"): no
  inventory/slots table, no endpoints, no campaign↔slot placement → inventory/campaign
  date-overlap rules are not merely missing, they are inexpressible.
- No validation: `ends_at < starts_at` accepted (advertising.go:97-100); any status → any status
  transition accepted (`ended → active` resurrects campaigns, advertising.go:117-136); negative
  budgets accepted at API level (no check, only DB default).
- Budget is never decremented or tracked against impressions/spend — no impressions table.
- `GET /v1/ads/campaigns/{id}`, `POST`, `PATCH` have no client caller (orphans).

---

## Orphan / Dead-Code Inventory

### A. Orphan backend endpoints (route exists, zero PWA/mobile callers)
| Endpoint | File:line | Note |
|---|---|---|
| GET /v1/vehicles/{id} | fleet-api handlers/vehicles.go:83 | client fn exists but unused |
| GET /v1/vehicles/{id}/telemetry | vehicles.go:112 | PWA uses /v1/telemetry/latest only |
| GET /v1/vehicles/{id}/twin (proxy) | fleet-api handlers/proxy.go:36 | PWA calls /api/twin directly |
| POST /v1/optimize/route (proxy) | proxy.go:43 | PWA calls /api/optimize directly |
| POST /v1/stations, PATCH /v1/stations/{id}/status | infra-api handlers/stations.go:85,128 | no operator UI |
| POST /v1/dispatch/jobs | infra-api handlers/dispatch.go:80 | no create UI |
| POST /v1/depot/work-orders, POST .../{id}/close | infra-api handlers/depot.go:118,138 | DepotPage read-only |
| POST /v1/safety/leak | infra-api handlers/incidents.go:171 | intended for sensors — legitimate orphan |
| GET /v1/carbon/credits/summary | citizen-api handlers/carbon.go:56 | unused |
| GET /v1/opendata/search | citizen-api handlers/opendata.go:60 | unused |
| GET /v1/mobile/config | citizen-api handlers/passenger.go:250 | mobile never calls it |
| POST /v1/payments, GET /v1/payments/{id} | commerce-api handlers/payments.go:59,283 | PWA list-only |
| GET /v1/loyalty/balance | commerce-api handlers/loyalty.go:23 | MarketplacePage doesn't show balance |
| POST /v1/marketplace/offers | loyalty.go:89 | no operator UI |
| GET /v1/ads/campaigns/{id}, POST, PATCH | commerce-api handlers/advertising.go:67,91,117 | list-only UI |
| POST /v1/ml/{demand/forecast, leak/score, fleet/propagate, carbon/forecast} | ml-platform app/main.py:109-138 | PWA uses only /models, /drift, /maintenance/score |

### B. Dead client calls (client function exists but no screen uses it)
| Function | File:line | Backend route exists? |
|---|---|---|
| `getVehicle` | apps/pwa/src/api/fleet.ts:21 | yes |
| `listTwins` | apps/pwa/src/api/fleet.ts:62 | yes |
| mobile `driver_sub` filter param | apps/mobile/src/api/client.ts:173-176 | route exists but **ignores the param** (dispatch.go:41) |
| mobile/PWA `reportIncident` `description` field | client.ts:181-190, infra.ts:41-52 | route exists but **drops the field** (incidents.go:72-78) |
| PWA `listMaintenancePredictions` `min_risk` param | fleet.ts:36 | route exists but **ignores the param** (operations.go:27) |
| PWA `createDrtRequest` extra fields (`pickup_label`, `dropoff_label`, `passengers`) | citizen.ts:52-58 | route exists but **drops the fields** (drt.go:39-42) |

No client was found calling a non-existent route (all paths resolve through APISIX to real routes).

### C. Events produced-never-consumed
| Topic | Producer | Consumer |
|---|---|---|
| `twin.updated` | digital-twin twin.rs:128-141 | none |
| `maintenance.predicted` | predictive-maintenance events.py:73-87 | none (should create work orders) |
| `drt.requested` | citizen-api drt.go:91 | none (should drive assignment) |
| `fare.payment.initiated/settled/failed` | commerce-api payments.go:120,179,191 | none (settled should drive loyalty accrual) |
| `energy.trade.executed` | commerce-api trading.go:88 | none |
| `carbon.credit.issued` | carbon-analytics core.py:120 | none (should post CARBON_FUND ledger leg) |
| `station.status.changed` | infra-api stations.go:158 | none |
| `safety.leak.detected` | infra-api incidents.go:206 | none (workflow driven by direct Temporal signal) |
| `dispatch.job.assigned` | infra-api dispatch.go:110 | none |
| `toggle.changed` | toggle-service toggles.go:219 | none (services poll HTTP instead — functional, but the topic is decorative) |
| `fuel.reading` | **never produced** (schema+fixture only) | none — dead catalog entry |

Consumed-never-produced: none. (`telemetry.raw` ← telemetry-simulator; `telemetry.enriched` ← telemetry-ingest; both consumed.)

### D. Tables never read / Redis keys never read
- `fleet.twin_snapshots`: writes always fail (missing `ts` column) and no API reads it → dead table.
- Redis `bus:meta:<bus_id>`: read by telemetry-ingest (`store.rs:13,24-26`), **never written** by anything → dead enrichment.
- Redis `toggles:<module>` (toggle-service) and `twin:<bus_id>`/`twin:buses` (digital-twin): properly read+written.

---

## Missing-Schema Inventory (code vs infra/sql/migrations)

| # | Missing object | Required by (evidence) | Needed DDL |
|---|---|---|---|
| S1 | `fleet.twin_snapshots.ts timestamptz` | digital-twin snapshot insert `twin.rs:228` vs `0001_core.sql:72-78` | `ALTER TABLE fleet.twin_snapshots ADD COLUMN IF NOT EXISTS ts timestamptz NOT NULL DEFAULT now();` (or change insert to `updated_at`) |
| S2 | `UNIQUE(period)` on `citizen.carbon_credits` | carbon-analytics delete+insert `core.py:101-113`; concurrent compute double-issues | `CREATE UNIQUE INDEX carbon_credits_period_uq ON citizen.carbon_credits(period);` + switch to `INSERT ... ON CONFLICT (period) DO UPDATE` |
| S3 | `citizen.drt_requests`: `pickup_label text`, `dropoff_label text`, `passengers int` | PWA sends these fields (`citizen.ts:52-58`), backend drops them (`drt.go:39-42`) | ALTER TABLE ADD COLUMNs + accept in `createDRTRequest` |
| S4 | `citizen.drt_requests`: `vehicle_id uuid`, `driver_sub text`, `assigned_at timestamptz` | no assignment is expressible (drt.go) | ALTER TABLE + assign endpoint |
| S5 | `infra.dispatch_jobs.ends_at timestamptz` | `shift_end` published as null (dispatch.go:100-108); overlap checks inexpressible | ALTER TABLE + use in create/conflict check |
| S6 | Drivers/vehicles reference for dispatch | `driver_sub` free text, `vehicle_id` unvalidated (dispatch.go:87-91) | `infra.drivers` table + FK (or validate against Keycloak/fleet.vehicles) |
| S7 | Station queue tables | SPEC "queue mgmt" absent (stations.go) | `infra.station_queue(id, station_id FK, bus_id FK, joined_at, status)` |
| S8 | Ad inventory + placement tables | SPEC "ad inventory" absent (advertising.go) | `commerce.ad_inventory(id, kind, bus_id null, label)`, `commerce.ad_placements(campaign_id FK, inventory_id FK, starts_at, ends_at)` + overlap exclusion constraint |
| S9 | Loyalty ledger/redemptions | no accrual, no audit (loyalty.go) | `commerce.loyalty_transactions(id, user_sub, delta int, reason text, ref text, created_at)`; `commerce.loyalty_redemptions(id, user_sub, offer_id FK, points int, created_at, UNIQUE(user_sub, offer_id, created...))` |
| S10 | `commerce.trades.tb_transfer_id text` | transfer id computed but not persisted (trading.go:68-81) | ALTER TABLE ADD COLUMN |
| S11 | `commerce.fare_payments`: `refund_of uuid null`, `refunded_at timestamptz null` | 'refunded' status unreachable (payments.go) | ALTER TABLE + refund endpoint |
| S12 | `infra.work_orders`: `bus_id uuid null`, `prediction_id uuid null`, `assignee text null`, `started_at timestamptz null` | predictive-maintenance→work-order loop impossible (depot.go) | ALTER TABLE + status set extension |
| S13 | Route/GTFS tables (`fleet.route_stops` or `gtfs_stops/routes/trips/stop_times`) | passenger.go:31-49 hardcodes data; route-optimizer generates random stops (`data.py:71-85`); lakehouse geo_enrich references absent `fleet.depot_zones`/`fleet.route_corridors` (`geo_enrich.py:5-7`) | new tables + loaders; replace hardcoded/generated data |

---

## Data-Flow Diagram Notes

```
[telemetry-simulator] --telemetry.raw--> [telemetry-ingest(Rust)] --+--> fleet.telemetry (Timescale, dedup OK)
        (+ Redis bus:meta:<id>  ✗ NEVER WRITTEN → enrichment null)  +--> telemetry.enriched
telemetry.enriched --+--> [digital-twin(Rust)] --> Redis twin:<id> (TTL) --> twin.updated (✗ no consumer)
                     |        \--snapshot--> fleet.twin_snapshots (✗ P0 broken: no ts column)
                     +--> [predictive-maintenance] --> fleet.maintenance_predictions
                     |        \--> maintenance.predicted (✗ no consumer → no work order; OPEN LOOP)
[fleet-api] --reads--> fleet.vehicles/telemetry/maintenance_predictions --> PWA fleet pages (OK)
[route-optimizer] --reads--> fleet.vehicles+telemetry, infra.stations (OK; stops = random gen ✗)
sensor --> [infra-api]/v1/safety/leak --> infra.incidents + safety.leak.detected (✗ no consumer)
           + Temporal signal --> IncidentResponseWorkflow (escalation OK, closed via HTTP ack/resolve)
[citizen-api] DRT --> citizen.drt_requests + drt.requested (✗ no consumer → assignment never happens)
[carbon-analytics] --odometer deltas--> citizen.carbon_credits + carbon.credit.issued (✗ no consumer, ✗ no UNIQUE(period), ✗ no TB CARBON_FUND leg)
[commerce-api] payment --> commerce.fare_payments + TigerBeetle (rider 1xxx → operator 2001; ✗ no funding/balance rule)
           + fare.payment.* (✗ no consumer → loyalty accrual never happens → loyalty dead)
           trade --> commerce.trades + TigerBeetle (3001 → 2001: ✗ unfunded clearing, ✗ no surplus check)
[toggle-service] --> public.feature_toggles + Redis toggles:<m> (OK) + toggle.changed (✗ no consumer; HTTP polling used)
[mobile app] --JWT flows--> ✗ setAccessToken never called → all authed endpoints 401
```
