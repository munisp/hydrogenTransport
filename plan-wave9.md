# Wave 9 — stakeholder-onboarding audit beyond the original scope (merchants & individuals)

Scope: a fresh audit of the onboarding workflow for **merchants** (advertiser,
data-partner, corporate billing) and **individuals** (citizen, driver), going
beyond the 7 gaps closed in Wave 8. Four findings were fixed; three adjacent
authorization/robustness gaps found during the same pass were fixed with
them; three items are documented as accepted residuals (product decisions).

## Findings → fixes

| # | Severity | Finding | Fix |
|---|----------|---------|-----|
| W9-1 | **critical** | `CreateUser` adopted an existing Keycloak account on 409 and `provision()` then unconditionally called `SetTemporaryPassword` on it. The **public citizen self-serve** (and any approved intake) let an unauthenticated party force a password reset on ANY registered account — operators and platform-admins included. | `CreateUser` now returns `existed bool`; the temporary password is set ONLY for brand-new users. Existing accounts get the persona role (idempotent) + the actions email to the address on file (ownership-proof channel). Citizen self-serve responds indistinguishably (no account-existence oracle); the approve path flags `existing_account: true` to the admin (possible impersonation signal). |
| W9-2 | high | Email identity was case-sensitive: `Victim@Example.com` vs `victim@example.com` bypassed the intake dedup replay, the per-email velocity cap AND the 0011 pending-dedup unique index. | Email lowercased at validate; `FindPending`/`CountRecent` match `lower(email)`; migration **0012** backfills `email = lower(email)` and rebuilds both indexes as expression indexes on `lower(email)`. |
| W9-3 | high (individuals) | An approved driver had no `infra.drivers` row — dispatch jobs FK-reference it, so the new driver could never be assigned or accept a job, and NO registration endpoint existed (only charter placeholders wrote the table). Onboarding produced an account that could not work. | `POST /v1/drivers/register` (driver self-service; sub from JWT only, idempotent upsert, 201/200) + `POST /v1/drivers` (operator-managed, explicit sub). |
| W9-7 | high (adjacent) | `POST /v1/dispatch/jobs/{id}/accept` matched on id+status only — ANY driver could accept ANY assigned job. | UPDATE scoped by `driver_sub = <JWT sub>`; 404 either way (no existence leak); 401 without identity. |
| W9-4 | medium | `POST /v1/onboarding/reconcile` listed at most 500 completed requests and silently stopped. | Paginates (limit+offset, stable `created_at DESC, id` order) until exhausted. |
| W9-5 | medium | Admin `POST /v1/users` silently adopted an existing account on email conflict and attached the requested roles to it. | Returns `409` naming the existing id; role changes go through `PUT /v1/users/{id}/roles`. |
| W9-6 | medium | Missing hard limits: email length unbounded (column TEXT), meta unbounded jsonb, reject reason unbounded. | email ≤ 254 chars; meta ≤ 8 KiB; reject reason ≤ 500 chars (all 400). |

## Accepted residuals (documented, not code gaps)

- **R1 — no email on rejection**: the platform has no mail provider for
  non-Keycloak mail; approved applicants ARE notified (Keycloak actions
  email), rejected applicants use the W8-6 public status endpoint.
- **R2 — advertiser/data-partner map to the read-only `citizen` role**:
  merchant self-service campaign management is the deferred DECISION item
  A2-08 (docs/GAP_AUDIT.md), unchanged.
- **R3 — corporate billing accounts remain operator-mediated** (Wave-7
  A2-06): a deliberate control — credit-bearing accounts are opened by staff,
  not self-serve.
- **R4 — existed-path actions email**: citizen self-serve on a registered
  address sends Keycloak's VERIFY_EMAIL+UPDATE_PASSWORD email to that
  address. Nuisance only (no credential change), bounded by the 5/day
  per-email velocity cap; suppressing it would create an account-existence
  oracle via response differences.

## Verification

- Go gate: all 8 modules build/vet/test green (Go 1.26.4).
- New tests (admin-api onboarding): existing-account citizen self-serve never
  resets credentials (W9-1), approve flags existing_account + no reset
  (W9-1), case-insensitive dedup + velocity (W9-2), hard limits (W9-6),
  reject-reason cap (W9-6). Wave-8 suites (dedup, velocity, orphan retry,
  status endpoint, reconcile, TTL, captcha, F3 gate) still green.
- New tests (infra-api): driver self-register 201/200 + guards, operator
  create, accept ownership (assignee 200 / other driver 404 / anonymous 401).
- Migration 0012 ships goose up/down; admin-api `EnsureSchema` updated to the
  same expression indexes (mixed-version rollouts stay consistent).
