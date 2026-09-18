# H2Fleet API Reference

All traffic enters through APISIX at `http://localhost:9080`. The gateway strips
`/api/<domain>` (proxy-rewrite), so a call to `/api/fleet/v1/vehicles` arrives
at fleet-api as `/v1/vehicles` (SPEC §3.6).

* Auth: `Authorization: Bearer <keycloak jwt>` for mutating routes; public GETs
  on toggles/citizen pass through unauthenticated (see `infra/apisix/apisix.yaml`).
* **Payment and DRT reads also require auth**: unauthenticated calls to
  `/v1/payments*` and `/v1/drt/requests*` return `401 unauthorized`.
* **Role-gated mutations**: routes marked `JWT (operator)` / `JWT (driver)`
  return `403 forbidden` without the realm role.
* **Downstream failures**: payment routes return `502 bad_gateway` when the
  TigerBeetle ledger or Mojaloop switch/simulator fails, and publish
  `fare.payment.failed`. The simulated ledger rejects negative balances, so
  dev payments without a reachable TigerBeetle fail with 502.
* **Python mutating endpoints require JWTs too**: `POST /api/ml/v1/predict`,
  `POST /api/optimize/v1/optimize/route`, `POST /v1/carbon/compute`
  (carbon-analytics, internal-only).
* Machine consumers: `apikey: h2fleet-partner-demo-key` on `/api/open-data/*` (demo;
  gateway rewrites to citizen-api `/v1/opendata/*`).
* Every service exposes `GET /healthz` (unauthenticated).
* Disabled modules return `404 {"error":"module disabled","module":"<id>"}`.
* Event topics beyond SPEC §3.3: `fare.payment.failed` (payment failure
  companion event) and `telemetry.raw.dlq` (dead-letter for malformed raw
  telemetry — envelope shape in `services/rust/telemetry-ingest/README.md`).
  Schemas + fixtures live in `packages/events/`.

## toggle-service — `/api/toggles` → :8080

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/toggles` | public | `{"toggles": {"<module>": bool, ...}}` |
| GET | `/v1/toggles/{module}` | public | `{"module","enabled","domain"}` |
| PUT | `/v1/toggles/{module}` | `platform-admin` | body `{"enabled": bool}` → updates PG, Redis, publishes `toggle.changed` |

## fleet-api — `/api/fleet` → :8081

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/vehicles` | public | List buses (fleet_no, status, last geom) |
| GET | `/v1/vehicles/{id}` | public | Vehicle detail |
| GET | `/v1/vehicles/{id}/telemetry?from&to` | public | Timescale hypertable window query |
| GET | `/v1/telemetry/latest` | public | Latest telemetry sample per bus (`DISTINCT ON (bus_id) ... ORDER BY bus_id, ts DESC`), JSON array |
| GET | `/v1/vehicles/{id}/twin` | public | Proxy to digital-twin for one bus |
| GET | `/v1/maintenance/predictions?bus_id=` | public | Rows from `fleet.maintenance_predictions` |
| GET | `/v1/fuel/levels` | public | Latest H2 level per vehicle + estimated range |
| POST | `/v1/optimize/route` | JWT (`operator`) | Proxy to route-optimizer |

## infra-api — `/api/infra` → :8082

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/stations` | public | Stations + `available_kg` inventory + status |
| GET | `/v1/stations/{id}` | public | Station detail |
| POST | `/v1/stations` | JWT (`operator`, `station-staff`) | Create station |
| PATCH | `/v1/stations/{id}/status` | JWT (`operator`, `station-staff`) | Set online/offline/maintenance |
| POST | `/v1/stations/{id}/queue/{entry}/complete` | JWT (`operator`, `station-staff`) | Complete a station queue entry (Wave-8: `station-staff` realm role accepted) |
| GET | `/v1/incidents` | public | List incidents |
| POST | `/v1/incidents` | JWT | Open incident (leak workflow starts in Temporal) |
| POST | `/v1/incidents/{id}/ack` | JWT (`operator`) | Acknowledge incident |
| POST | `/v1/incidents/{id}/resolve` | JWT (`operator`) | Resolve incident |
| POST | `/v1/safety/leak` | sensor token / JWT | Leak sensor webhook |
| GET | `/v1/dispatch/jobs` | public | List dispatch jobs |
| POST | `/v1/dispatch/jobs` | JWT (`operator`) | Assign job → `dispatch.job.assigned` + Temporal signal |
| POST | `/v1/drivers/register` | JWT (`driver`) | Wave-9 W9-3: driver self-registration — upserts `infra.drivers` keyed by the JWT `sub` (never the body), `{"name","license_no"}` → 201 created / 200 updated. Required before any job can be assigned to the driver |
| POST | `/v1/drivers` | JWT (`operator`) | Wave-9 W9-3: operator-managed driver registration, `{"sub","name","license_no"}` → 201/200 |
| POST | `/v1/dispatch/jobs/{id}/accept` | JWT (`driver`) | Driver accepts OWN assigned job (Wave-9 W9-7: scoped by JWT `sub` — another driver's job is 404, indistinguishable from unknown) |
| POST | `/v1/charters` | JWT (`operator`) | Wave-7 A2-09 charter/school block booking, body `{"reference","customer_name","starts_at","ends_at","vehicle_ids":[...]}`: one tx per vehicle — overlap check → placeholder driver → dispatch job `route='charter:<reference>'`; 409 on overlap, 422 unknown vehicle; same reference replays the booking (200) → `charter.booking.confirmed` |
| GET | `/v1/charters` | JWT (`operator`) | List charter bookings (`?status=`) with vehicle counts |
| GET | `/v1/charters/{id}` | JWT (`operator`) | Booking detail + each reserved vehicle and its backing dispatch job |
| POST | `/v1/charters/{id}/cancel` | JWT (`operator`) | Cancels the booking AND all active backing dispatch jobs in one tx → `charter.booking.cancelled`; 409 unless `confirmed` |
| GET | `/v1/compliance/reports` | public | Generated compliance reports |
| GET | `/v1/compliance/reports/{id}` | public | One report |
| POST | `/v1/compliance/reports/generate` | JWT (`platform-admin`) | Trigger report generation |
| GET | `/v1/depot/bays` | public | Depot bays (fueling/charging/parking/workshop) + occupancy |
| GET/POST | `/v1/depot/work-orders` | public / JWT | Depot work orders (from predictions/leaks) |
| POST | `/v1/depot/work-orders/{id}/close` | JWT | Close a work order |

## citizen-api — `/api/citizen` → :8083

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/passenger/stops` | public | GTFS stops |
| GET | `/v1/passenger/routes` | public | GTFS routes |
| GET | `/v1/passenger/arrivals?stop_id=` | public | Headway-based arrival predictions |
| GET | `/v1/passenger/journey?from=&to=` | public | Direct-route journey options between two stop IDs |
| GET | `/v1/passenger/alerts` | public | Active service alerts |
| GET | `/v1/mobile/config` | public | Bootstrap config for the Expo apps |
| POST | `/v1/drt/requests` | JWT (`citizen`) | Book DRT shuttle → `drt.requested` |
| GET | `/v1/drt/requests` | JWT | Own requests (row owner = `sub`); 401 unauthenticated |
| GET | `/v1/drt/requests/{id}` | JWT | One request (owner or `operator`); 401 unauthenticated |
| POST | `/v1/drt/requests/{id}/cancel` | JWT | Cancel a `requested`/`assigned` request (404 unknown, 409 not cancellable) |
| GET | `/v1/carbon/credits` | public | Issued credits + kg CO2 avoided |
| GET | `/v1/carbon/credits/summary` | public | Totals across periods |
| GET | `/v1/opendata/datasets` | public/key-auth | Open dataset catalog (via `/api/open-data/*` for key-auth consumers) |
| GET | `/v1/opendata/search?q=` | public/key-auth | OpenSearch-backed dataset search |

## commerce-api — `/api/commerce` → :8084

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/payments` | JWT (`citizen`) + `Idempotency-Key` header (400 without) | Initiate fare, body `{"amount_minor": 250, "currency": "EUR"}`: Mojaloop transfer + TB ledger entry → `fare.payment.initiated`; same key replays the original payment; 502 + `fare.payment.failed` on ledger/switch failure |
| GET | `/v1/payments` | JWT | List payments (own for `citizen`, all for `operator`); 401 unauthenticated |
| GET | `/v1/payments/{id}` | JWT | Payment detail; 401 unauthenticated |
| GET | `/v1/loyalty/balance` | JWT | Caller loyalty point balance |
| POST | `/v1/loyalty/redeem` | JWT (`citizen`) | Redeem an offer, body `{"offer_id": "..."}` → `{redeemed_offer_id, points_spent, remaining_points}` |
| GET | `/v1/marketplace/offers` | public | Loyalty marketplace offers |
| POST | `/v1/marketplace/offers` | JWT | Publish an offer |
| GET/POST | `/v1/energy/trades` | public / JWT (`operator`) + `Idempotency-Key` header (400 without) | Energy/H2 trade, body `{"kind": "h2-sale\|h2-purchase\|energy-export", "quantity_kg": 25, "price_minor": 84000}`: proposed → station-surplus backing check (409 `insufficient_surplus`) → TigerBeetle settlement (402 `insufficient_funds` when energy clearing is unfunded) → `executed` + `energy.trade.executed`; same key replays the trade |
| GET | `/v1/gov/kpis` | public | Gov dashboard KPIs (each rollup independently nullable; failed sources are named in `degraded` + `partial: true`): `revenue_30d_minor`, `settled_payments_30d`, `ridership_estimate_30d`, `kg_co2_avoided_total`, `carbon_credits_total`, `vehicles_total`, `vehicles_active`, `fleet_active_ratio_pct`, `fleet_uptime_pct` (null until a time-based source exists), `stations_available_kg`, `open_incidents` |
| GET/POST | `/v1/ads/campaigns` | public / JWT | Ad inventory & campaigns |
| GET/PATCH | `/v1/ads/campaigns/{id}` | public / JWT | Campaign detail / update |
| POST | `/v1/billing/accounts` | JWT (`operator`) | Wave-7 A2-06: create a corporate payer account (`{"name","kind": "corporate\|school\|agency\|municipal","contact_email"}`); allocates the next TigerBeetle clearing account (5xxx) → `billing.account.created` |
| GET | `/v1/billing/accounts` | JWT (`operator`) | List payer accounts + uninvoiced accrual totals |
| GET | `/v1/billing/accounts/{id}` | JWT (`operator`) | Account detail |
| POST | `/v1/billing/accounts/{id}/invoices` | JWT (`operator`) | Sweep uninvoiced charges in `{"period_start","period_end"}` into an invoice (idempotent per account+period: replay 200); 422 no charges or account not active → `billing.invoice.issued` |
| GET | `/v1/billing/invoices` | JWT (`operator`) | List invoices (`?billing_account_id=`) |
| GET | `/v1/billing/invoices/{id}` | JWT (`operator`) | Invoice + its charges |
| POST | `/v1/billing/invoices/{id}/pay` | JWT (`operator`) | Settle an issued invoice: deterministic transfer 5xxx clearing → 2001 operator revenue (code 500); 402 `insufficient_funds` when unfunded; replay 200 → `billing.invoice.paid` |

## predictive-maintenance — `/api/ml` → :8090

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/predict` | JWT | body `{"bus_id": "<uuid>"}` → `{bus_id, model_version, feature_window_hours, predictions: [{component, risk_score, predicted_failure_at}]}` |

## route-optimizer — `/api/optimize` → :8091

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/optimize/route` | JWT | body `{"bus_ids": [...]?, "date": "YYYY-MM-DD"}` → `{date, data_source, solver_status, unassigned_stops, plans: [BusPlan]}` |

## carbon-analytics — internal-only (NOT routed via APISIX)

`carbon-analytics` is an internal batch/service component: it is deliberately
absent from the gateway prefix map (SPEC §3.6) and reachable only in-network
on :8094. `POST /v1/carbon/compute` requires a service JWT.

## digital-twin — `/api/twin` → :8092

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/v1/twin` | public | All twins (Redis hot state), `{"twins": [...], "count": n}` |
| GET | `/v1/twin/{bus_id}` | public | One twin: latest state + `updated_at` |

## admin-api — `/api/admin` → :8085

Stakeholder onboarding (full contract incl. user management, KPIs and ops
feed in `services/go/admin-api/README.md`):

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/v1/onboarding/citizen` | public | Citizen self-serve → immediate Keycloak provisioning, `201`; a retry after a provisioning outage adopts the orphaned pending row → `200` |
| POST | `/v1/onboarding/{persona}` | public | Intake for `driver`/`operator`/`station-staff`/`advertiser`/`data-partner`/`gov-viewer` → `201 pending`; body requires `email` (≤254 chars, stored lowercase — case-insensitive identity), `display_name`, `org` (all gated personas) + `meta.license_no` (driver); re-filing the same `(persona, email)` while pending replays the original → `200 {"deduplicated": true}`; >5 requests/email/24 h → `429`; optional `captcha_token` enforced when captcha env is set |
| GET | `/v1/onboarding/status/{id}` | public | Applicant status check (capability URL): `{id, persona, status, created_at, decided_at}` only — zero PII; unknown ids 404 |
| GET | `/v1/onboarding?status=&persona=` | JWT (`platform-admin`, `operator`) | List the queue (`status=` accepts `pending\|approved\|rejected\|completed\|expired`) |
| GET | `/v1/onboarding/{id}` | JWT (`platform-admin`, `operator`) | Single request |
| POST | `/v1/onboarding/{id}/approve` | JWT (`platform-admin`) | Provisions the Keycloak user (idempotent `EnsureRealmRole`, actions email) → `completed`; temp password ONLY for brand-new users — a pre-existing account's credentials are never reset and the response flags `existing_account: true` (Wave-9 W9-1); `409` non-pending or TTL-expired; `502` Keycloak failure (stays pending, safe to retry) |
| POST | `/v1/onboarding/{id}/reject` | JWT (`platform-admin`) | `{"reason"}` → `rejected`; `409` non-pending or TTL-expired |
| POST | `/v1/onboarding/reconcile` | JWT (`platform-admin`) | Self-heal: re-asserts the persona realm role on every completed request → `{"checked","ensured","failed","failed_ids"}` |

Pending requests expire after `ONBOARDING_PENDING_TTL_DAYS` (default 30):
the decide path refuses with `409` and a boot + daily sweep flips stragglers
to `expired` (`decided_by='system:ttl-expiry'`).

## Error format

`{"error": "<code>", "message": "<human readable>", "module?": "<id>"}` —
codes: `unauthorized` (401), `forbidden` (403, role/Permify deny),
`module disabled` (404), `validation` (400), `not_found` (404),
`internal` (500), `bad_gateway` (502 — ledger/Mojaloop/OpenSearch downstream
failure).

## Versioning & deprecation policy

- **Path version is the contract.** Every route carries an explicit major
  version (`/v1/...`). Breaking changes (removed/renamed fields, changed
  semantics, new required parameters, stricter validation such as the
  Wave-6 incident-type enum) ship as `/v2/...` — never silently into `/v1`.
- **Additive is not breaking.** New optional request fields and new response
  fields may land in an existing version at any time; consumers MUST ignore
  unknown fields (all platform clients decode with `DisallowUnknownFields`
  off for this reason).
- **Deprecation lifecycle.** When a `/v(N)` route or field is superseded:
  1. responses gain `Deprecation: true` and `Sunset: <HTTP-date>` headers
     (RFC 8594/9745) for at least **90 days**;
  2. the change is announced on the `platform.api.changelog` Kafka topic
     and in this file;
  3. after the sunset date the route returns `410 Gone` with
     `{"error":"gone","message":"use /v2/..."}` for a further 90 days,
     then is removed.
- **Webhooks carry their own version.** The `X-H2Fleet-Event` header names
  the event type; payload shape changes create a new event type
  (`incident.opened.v2`), never an in-place change — subscribers opt in by
  updating their subscription's event list.
- **Error codes are stable.** The codes in §Error format are part of the
  contract; new codes may be added, existing ones are never repurposed.
