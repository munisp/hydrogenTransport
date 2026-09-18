# Wave 8 — Stakeholder-onboarding hardening

Closes the gaps found in the Wave-7 review of the onboarding workflow
(admin-api `internal/onboarding`). Scope: hardening only — no new personas,
no self-service portal (A2-08 remains a DECISION item).

| # | Gap (review finding) | Fix |
|---|----------------------|-----|
| W8-1 | No intake dedup — same email can file N pending requests | App-level pending-dedup (200 replay, `deduplicated:true`) + DB backstop: partial UNIQUE `(persona,email) WHERE status='pending'` (0011); 23505 → re-read → 200 |
| W8-2 | No per-email velocity (only per-IP at the gateway) | Max 5 onboarding requests per email per 24 h across all personas → 429 (complements the ~6 r/min/IP APISIX limit) |
| W8-3 | Credential-free intake (`meta` free-form) | Structured requirements: `org` mandatory for every approval-gated persona; `driver` additionally requires `meta.license_no` (validated shape) |
| W8-4 | Provisioning is 4 non-transactional Keycloak calls; mid-sequence failure strands a role-less user | `EnsureRealmRole` (read-then-assign, idempotent) in the approve path + `POST /v1/onboarding/reconcile` (platform-admin, audited) re-applying persona roles to all completed requests |
| W8-5 | Citizen self-serve failure orphans a `pending` row | Failure records `meta.provision_error`; a citizen retry with the same email adopts the orphan row and re-attempts provisioning (200 on success) |
| W8-6 | Rejected applicants hear nothing | Public capability-URL status check `GET /v1/onboarding/status/{id}` — returns status/persona/timestamps only, never PII (unguessable uuid is the capability) |
| W8-7 | Pending requests live forever | `expired` status (0011 CHECK); TTL guard on decide (default 30 d, `ONBOARDING_PENDING_TTL_DAYS`) + startup/daily sweep |
| W8-8 | No captcha on public intake (documented residual) | Optional verification: `ONBOARDING_CAPTCHA_VERIFY_URL` + `ONBOARDING_CAPTCHA_SECRET` (siteverify-compatible); fail-closed when configured, off by default (dev) |
| W8-9 | `station-staff` mapped to full `operator` role (least-privilege breach) | Real `station-staff` realm role (realm JSON); persona maps to it; infra-api station mutation routes accept `operator` OR `station-staff` |

Out of scope (unchanged decisions): advertiser self-service portal (A2-08),
multi-tenancy (A3-04), document-upload verification (external identity-proofing
boundary — the structured fields of W8-3 are the hook point).

## Verification

- admin-api build/vet/test green incl. new suites (dedup, velocity, retry,
  status-check, reconcile, expiry, captcha, role mapping).
- Migration 0011 up/re-up/down on embedded Postgres (goose).
- infra-api build/vet/test green (station route role widening).
- Push: sequential ≤10-file commits to main, byte-parity verified.
