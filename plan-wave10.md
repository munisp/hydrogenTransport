# Wave 10 — deeper onboarding-lifecycle audit (offboarding & admin plane)

Scope: the third audit round on stakeholder onboarding, this time into the
**lifecycle around** onboarding — what happens after an account exists:
driver suspension/offboarding, post-approval identity mutation, and the
admin plane that operates onboarding itself. Three gaps confirmed and fixed,
all with regression tests. No schema changes (no migration).

## Findings → fixes

| # | Severity | Finding | Fix |
|---|----------|---------|-----|
| W10-1 | high | **Admin-plane lockout**: any platform-admin could (a) disable their OWN account by mistake, or (b) disable / revoke `platform-admin` from the LAST enabled platform-admin — bricking the entire admin plane (no one left to approve onboarding, manage users, flip toggles; recovery = direct Keycloak console access). | `POST /v1/users/{id}/disable` → `409` on self-disable and on the last enabled platform-admin; `PUT /v1/users/{id}/roles` → `409` when removing `platform-admin` from the last enabled holder. The check counts enabled holders via the Keycloak client and fails closed (502) when the count is unavailable. |
| W10-2 | high (individuals) | **Suspended drivers kept working**: `infra.drivers.status` had a `suspended` value but nothing wrote it, and `POST /v1/dispatch/jobs/{id}/accept` never checked it — a suspended (or never-registered) driver could keep accepting jobs, since JWTs/realm roles outlive any operational action. | New `POST /v1/drivers/{sub}/status` (operator): `active\|off-duty\|suspended`. Accept now pre-checks the drivers row: unregistered → `403` (points at self-registration), non-active → `403` naming the status. |
| W10-3 | medium (individuals) | **Self-register could overwrite the verified identity**: Wave-9's `POST /v1/drivers/register` upserted name + licence on conflict, so a driver could silently replace the licence number a platform-admin verified at intake approval. | Self-service registration is now INSERT-ONLY (`ON CONFLICT DO NOTHING` → `200 already_registered` with a pointer to the operator channel); `POST /v1/drivers` (operator) remains the correction path. |

## Verified non-gaps (checked this round, no change needed)

- **Concurrent approvals** of one request: both provision idempotently
  (`existed=true` + `EnsureRealmRole`), `Decide` converges to one terminal
  state — safe since Wave 9.
- **Operator `POST /v1/drivers` with an arbitrary sub**: no privilege gain —
  operators can already assign jobs to any registered driver; the FK keeps
  phantom subs out.
- **Re-application after rejection**: allowed (new pending row); dedup only
  suppresses concurrent duplicates. Intended.
- **Users mutations audit coverage**: all five user-management mutations
  already write hash-chained audit rows via middleware.
- **UpdateRoles partial failure**: adds/removes are independent Keycloak
  calls; a mid-list failure returns 502 naming the failed role and the
  caller can retry (idempotent per role).

## Accepted residual (documented)

- **Disabled users' live JWTs** remain valid until expiry (standard
  stateless-JWT trade-off; short token lifetime is the mitigation). A
  token-revocation/backlist mechanism would be a platform-wide decision,
  not an onboarding gap.

## Verification

- Go gate: all 8 modules build/vet/test green (Go 1.26.4).
- New tests: users lockout guards (self-disable 409, last-admin disable 409
  then allowed with two admins, last-admin demote 409 then allowed,
  non-admin role removal unaffected); infra-api driver status (suspend →
  accept 403, unregistered → 403, status endpoint 200/404/400); Wave-9
  register test updated for insert-only semantics.
