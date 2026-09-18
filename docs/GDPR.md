# H2Fleet GDPR & Data-Subject Rights

Scope: rider (citizen) personal data processed by the platform. Employee
(driver/operator) data is out of scope here — it is covered by the HR system
of record; the platform stores only their Keycloak `sub` and role.

## 1. Pseudonymous-by-design model

The platform never stores rider names, e-mail addresses, phone numbers, or
payment credentials. Every rider reference is the **Keycloak subject claim
(`sub`, a UUID)** — a pseudonymous identifier. The mapping `sub → natural
person` exists only inside Keycloak (the identity provider), which is the
single system of record for identity attributes.

Consequence: a platform database dump alone does not identify any natural
person; re-identification requires Keycloak access, which is separately
audited and admin-gated.

## 2. Data map (where rider-referencing rows live)

| store | table | rider key | content |
|---|---|---|---|
| Postgres | `citizen.drt_requests` | `rider_sub` | on-demand ride requests (origin/destination, labels) |
| Postgres | `commerce.fare_payments` | `rider_sub` | fare charges, amounts, status (no card data — Mojaloop holds the payment rails) |
| Postgres | `commerce.loyalty_accounts` / `loyalty_ledger` / `loyalty_redemptions` | `rider_sub` | points balance, earn/redeem/clawback entries |
| Postgres | `commerce.rider_entitlements` | `rider_sub` | granted passes/discounts |
| Postgres | `platform.audit_log` | `actor_sub` | hash-chained WORM audit trail (see §5) |
| Keycloak | realm `h2fleet` | user record | name/e-mail/credentials — **IdP-owned**, administered there |

Telemetry (`fleet.telemetry`) is keyed by `bus_id`, not by rider — vehicle
telemetry is not personal data in this deployment (no passenger association).

## 3. Right of access (Art. 15) — data export

`GET /api/citizen/v1/me/data-export` (citizen-api, rider JWT — a rider can
only ever export their own data; there is no admin export endpoint by
design) returns one JSON document aggregating every rider-keyed row:

```json
{
  "rider_sub": "…",
  "exported_at": "…",
  "sections": {
    "drt_requests": [...], "fare_payments": [...],
    "loyalty_account": {...}, "loyalty_ledger": [...],
    "loyalty_redemptions": [...], "entitlements": [...]
  }
}
```

## 4. Right to erasure (Art. 17) — tombstone erasure

`POST /api/citizen/v1/me/erasure` (rider JWT) erases the rider in one
transaction across all seven rider-keyed tables. Erasure is **pseudonymising
by tombstone**, not row deletion:

```
rider_sub  →  "erased:" + hex(sha256(GDPR_ERASURE_SALT | sub))[:16]
```

- **`GDPR_ERASURE_SALT` is mandatory** — the endpoint fails closed (503) if
  unset, so no deployment can silently perform unsalted (reversible-by-
  dictionary) erasure. Rotate = re-erase is impossible; the salt must be
  retained for audit-marker correlation and destroyed only when the audit
  retention in §5 expires.
- **Why tombstone, not DELETE:** financial rows (`fare_payments`,
  `loyalty_ledger`) have statutory retention obligations (tax/commercial
  law, typically 6–10 years) that override erasure for the *financial
  substance*; the tombstone destroys the link to the person while preserving
  the legally required record. This is the GDPR-standard
  "anonymisation-in-place" pattern — after erasure the rows are no longer
  personal data.
- On completion the service publishes `gdpr.erasure.completed` carrying
  **only the tombstone marker** (never the original `sub`) so downstream
  consumers can scrub derived state without ever seeing the identity.

## 5. Audit-trail exception (Art. 17(3))

`platform.audit_log` is an append-only, hash-chained WORM trail kept for
security accountability and regulator evidence (see `docs/SECURITY_AUDIT.md`
and the evidence-pack endpoint). Erasure does **not** rewrite it — rewriting
a hash chain would destroy its evidentiary value. Justification: Art. 17(3)(b)
(legal obligation) and Art. 6(1)(f)/(c). The trail records `actor_sub`
pseudonyms only; with the Keycloak mapping admin-gated and the erasure salt
eventually destroyed, residual identifiability is minimal and time-boxed by
the audit retention schedule (`docs/DATA_RETENTION.md`).

## 6. Operational checklist for a DSAR

1. Verify the request in Keycloak (the IdP authenticates the person).
2. Access → call the export endpoint with the rider's own token (or have the
   rider self-serve via the app — the endpoint is the self-serve path).
3. Erasure → rider calls the erasure endpoint (self-serve), or an operator
   triggers it through the app on verified request.
4. IdP attributes (name/e-mail) are erased in Keycloak separately by the
   identity admin.
5. Record the DSAR in the ops log; the `gdpr.erasure.completed` event +
   audit-trail entry are the completion evidence.
