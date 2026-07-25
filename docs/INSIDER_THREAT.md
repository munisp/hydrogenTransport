# H2Fleet — Insider-Threat Program

Threat model, controls and operating procedures for malicious or careless
**insiders**: people and workloads that already hold legitimate credentials
(admins, operators, service accounts, contractors). External-attacker
controls (WAF, OIDC edge auth, rate limits) are covered in
`docs/GATEWAY.md`; this document covers the adversary who is *already
inside* the trust boundary.

## 1. Threat model

| # | Persona | Description | Example abuse |
|---|---------|-------------|---------------|
| T1 | **Rogue platform-admin** | Holds `platform-admin` realm role legitimately; turns malicious or is coerced. | Disables safety modules (`leak-detection`), creates backdoor users, resets passwords, flips toggles off-hours. |
| T2 | **Curious operator** | Holds `operator` role (day-to-day NOC); snoops beyond need-to-know. | Reads rider payment history, citizen journeys, advertiser budgets without a ticket. |
| T3 | **Compromised service account** | A workload credential (Keycloak `services` client, `X-Audit-Token`, `LEAK_INGEST_TOKEN`, DB role) is exfiltrated. | Attacker replays it from an unexpected network; mass-creates payments or trades; floods the audit trail to hide a real action. |
| T4 | **Contractor / third party** | Time-boxed engagement, broad "temporary" access that never expires. | Access persists after contract end; unreviewed privilege creep. |

Assets at risk: user accounts & roles (Keycloak), feature toggles (safety
modules!), fare/energy ledger integrity (TigerBeetle), citizen PII,
compliance reporting trustworthiness, and the audit trail itself.

## 2. Controls

### 2.1 Least privilege via Permify relationships

* Toggle mutations require **both** the Keycloak `platform-admin` role *and*
  a Permify ReBAC check (`module:manage` on the specific module) — see
  `packages/go-auth/permify.go` and toggle-service's `PUT /v1/toggles/{module}`.
  Granting module-by-module relationships (not a blanket "manage everything")
  limits T1 blast radius.
* admin-api gates user management to `platform-admin`; operational KPI/health
  reads are `operator`-or-admin (need-to-know split, T2).
* audit-log reads (`GET /v1/audit`, `/v1/audit/verify`) are `platform-admin`
  only; ingest accepts only the shared service token or a service JWT (T3:
  stolen citizen/operator tokens cannot write or read audit data).

### 2.2 Hash-chained, append-only audit trail

`services/go/audit-log` (:8086) stores every sensitive mutation in
`platform.audit_log` with `hash = SHA-256(prev_hash ‖ fields)`:

* Emission points (via `auditclient` middleware, best-effort, never blocks
  business traffic):
  * **admin-api** — user create/role-update/disable/enable/reset-password,
    onboarding approve/reject, toggle proxy `PUT /v1/admin/toggles/{module}`;
  * **toggle-service** — `PUT /v1/toggles/{module}` (captures `{enabled}` body);
  * **commerce-api** — payment create, energy trade create, ad campaign create.
* `GET /v1/audit/verify` recomputes the whole chain: any retroactive edit,
  deletion or reordering returns `409` with `first_bad_id` (T1/T3 evidence).
* Entries are mirrored best-effort to OpenSearch `h2fleet-audit` for SOC
  search; Postgres remains authoritative.
* **Residual trust assumption:** a DB superuser can still rewrite the table.
  Mitigations: grant the app role `INSERT,SELECT` only (DBA runbook), anchor
  the daily head hash to the quarterly-review record (§4), and alert on
  `verify` failures.

### 2.3 Break-glass procedure (dual control for platform-admin grants)

Emergency elevation when no active platform-admin is available or an
incident requires admin action:

1. **Request**: incident commander files a P1 ticket stating scope, duration
   (max 4h) and reason. PagerDuty acknowledges.
2. **Dual approval**: TWO approvers from {head of operations, security
   lead, platform owner} approve in writing (ticket comment). Self-approval
   is never allowed.
3. **Grant**: Keycloak realm role `break-glass-admin` (mapped to the same
   permissions as `platform-admin`, but flagged) is assigned to the requestor
   by a second person (four-eyes on the console action itself).
4. **Audit**: every action taken while elevated lands in
   `platform.audit_log` (the emission points above) with the
   `break-glass-admin` role visible in `actor_roles`.
5. **Revoke + review**: role removed within 4h; the ticket links the
   `GET /v1/audit?actor=<sub>` extract; security lead signs off at the next
   quarterly review.

### 2.4 Session & timeout policies

* Keycloak realm `h2fleet`: SSO session idle 30 min / max 10 h for human
  users; service tokens 5 min access-token TTL with client-credentials
  refresh (see `infra/keycloak/realm-h2fleet.json`).
* admin-api user management can force re-auth (`POST
  /v1/users/{id}/reset-password`, disable) — both audited.
* PWA uses Keycloak JS with silent SSO refresh; refresh tokens rotate and
  are revoked on disable.

### 2.5 Rate limits

* Gateway: `limit-req` on the leak-ingest route (10 r/s + burst 20) —
  precedent for stricter limits on any future machine route
  (`docs/GATEWAY.md`).
* APISIX global rules + OpenAppSec WAF (detect→prevent) cover volumetric
  abuse of `/api/*`.
* In-service: the audit anomaly detector (§2.6) is the insider-specific
  compensating control where network rate limits are too coarse.

### 2.6 Anomaly detection hook

audit-log runs an in-service sliding-window detector
(`internal/anomaly`): more than `AUDIT_ANOMALY_THRESHOLD` (default 20)
sensitive actions per actor per `AUDIT_ANOMALY_WINDOW` (default 1m) →

1. `WARN` log line `AUDIT ANOMALY` (Loki/OpenSearch alert rule);
2. Prometheus `audit_anomaly_alerts_total{actor}` increment;
3. best-effort Alertmanager alert `H2FleetAuditAnomaly` (5-minute per-actor
   cooldown to avoid page storms).

Tuning: raise the threshold for batch jobs (ETL service accounts), lower it
for human `platform-admin` actors. This catches T1 mass-changes and T3
credential replay bursts even when every individual call is authorized.

## 3. Mapping: threats → controls

| Threat | Primary controls |
|--------|------------------|
| T1 rogue admin | Permify per-module grants, hash-chain audit + verify, anomaly detector, break-glass dual control, quarterly review |
| T2 curious operator | role split (operator vs platform-admin), audit reads restricted, quarterly access recertification |
| T3 compromised service account | short token TTLs, token-or-JWT ingest auth, anomaly rate detection, OpenSearch mirror for forensics |
| T4 contractor | time-boxed grants (Keycloak temp roles), quarterly recertification, disable flow audited |

## 4. Quarterly review process

1. **Access recertification**: export Keycloak role assignments; each
   `platform-admin`/`operator` grant re-justified by its owner; contractors
   without an active contract are disabled (audited action).
2. **Audit-chain verification**: run `GET /api/audit/v1/audit/verify`;
   record `checked` count and the head hash in the review minutes (external
   anchor against table rewrite).
3. **Anomaly review**: pull `H2FleetAuditAnomaly` alerts + top actors by
   `audit_entries_total` delta; investigate outliers.
4. **Break-glass log**: every grant in the quarter must have ticket + dual
   approval + revocation evidence.
5. **Permify relationship diff**: review `module:manage` grants added in
   the quarter.
6. Sign-off by security lead; findings tracked to remediation tickets.

## 5. Deployment notes

* Compose/env needs for audit-log and emitters (`AUDIT_LOG_URL`,
  `AUDIT_INGEST_TOKEN`) are listed in the platform-assurance report /
  `services/go/audit-log/README.md`.
* APISIX: `/api/audit/*` route required for admin/CLI access; emission is
  in-network service-to-service and does not traverse the gateway.
