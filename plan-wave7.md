# Wave 7 — Build the DECISION Items: Corporate Settlement + Charter Bookings

User ask (2026-09-19): after Wave 6 closed all FIX-NOW gaps, build the most
operator-valuable DECISION items from docs/GAP_AUDIT.md:

- **A2-06 — Corporate/employer group settlement.** An employer (or school,
  agency) buys mobility for N riders and pays one periodic invoice instead
  of per-ride rider payments. Extension path named in Wave 6:
  `payer_account` on entitlements.
- **A2-09 — Charter/school block bookings.** Reserve a block of vehicles
  for a customer window so dispatch cannot assign them to routes; the
  Wave-6 convention (charter = dispatch job `route='charter:<ref>'`)
  becomes a real booking entity with a transactional multi-vehicle
  reservation workflow.

Explicitly out of scope (remain DECISION/CONTRACT by design): multi-tenancy
(A3-04), advertiser self-service (A2-08), PWA offline writes (A3-09),
odometer-rewind anomaly surfacing (A1-06).

## Design

### W7-1 Corporate settlement (commerce-api)
- `commerce.billing_accounts` — corporate payer entity, one TigerBeetle
  clearing account each (5xxx, sequential from 5001, code 500). Flags:
  debits-must-not-exceed-credits (an unfunded corporate account cannot
  overdraw — invoice payment fails 402 like rider wallets).
- `rider_entitlements.payer_account` → billing_accounts(id). A payer-backed
  entitlement still resolves the rider charge to 0 (or discounted), but the
  COVERED amount (requested − charged) accrues to the billing account in
  the SAME transaction as the payment (never an orphan accrual).
- `commerce.billing_charges` — one row per covered ride (UNIQUE payment_id).
- `commerce.invoices` — periodic settlement artifact, UNIQUE(account,
  period) idempotency; generation locks the account (advisory xact lock)
  and atomically sweeps uninvoiced charges in the period; payment posts a
  deterministic transfer (`invoice:<id>`) clearing → operator revenue,
  conditional UPDATE guards double-pay.
- Endpoints: POST/GET /v1/billing/accounts, GET /v1/billing/accounts/{id},
  POST /v1/billing/accounts/{id}/invoices (generate), GET
  /v1/billing/invoices(+/{id}), POST /v1/billing/invoices/{id}/pay.
  Operator-only; audit-middleware on mutations.

### W7-2 Charter bookings (infra-api)
- `infra.charter_bookings` (reference UNIQUE, customer, window, status
  confirmed|cancelled|completed) + `infra.charter_vehicles` (booking,
  vehicle, dispatch_job).
- POST /v1/charters (operator): one transaction — validate window +
  vehicles, per-vehicle overlap check identical to CreateDispatchJob, then
  per vehicle: placeholder driver row (`charter:<ref>:<vehicle_id>`,
  satisfies the NOT VALID driver FK and the one-active-job-per-driver
  partial unique index) + dispatch job `route='charter:<reference>'`.
  Partial unique vehicle index is the DB-level race guard.
- POST /v1/charters/{id}/cancel: booking → cancelled, all its active
  dispatch jobs → cancelled (frees the vehicles), charter.booking.cancelled
  event. GET /v1/charters(+/{id}).

## Milestones
- wa7-a: plan + migration 0010 (this file + 0010_wave7.sql)
- wa7-b: commerce billing implementation + pgxmock tests
- wa7-c: infra charters implementation + pgxmock tests
- wa7-d: docs (GAP_AUDIT reclass, API.md, scorecard, migrations README) +
  full compile/test gate
- wa7-e: push to main + byte-for-byte verification
