# H2Fleet Data Protection Impact Assessment (DPIA) — summary

GDPR Art. 35 assessment for the H2Fleet municipal transit platform.
Controller: the municipal transit authority. Processor: the platform
operator. This is the living summary; review cadence in §6.

## 1. Systematic description of processing

| | |
|---|---|
| **What** | Fare collection and capping, loyalty programme, on-demand transit (DRT) requests, entitlement passes/discounts, fleet telemetry, safety incident management, audit trail. |
| **Whose data** | Riders (pseudonymous — Keycloak `sub` only), drivers/staff (`sub` + role), vehicles (not personal data). |
| **Where** | Postgres/TimescaleDB (system of record), TigerBeetle (ledger), Mojaloop (payment rails), Kafka/Fluvio (events), Redis (ephemeral), OpenSearch (audit mirror), Keycloak (identity). |
| **How long** | Per docs/DATA_RETENTION.md. |
| **Special categories** | None intentionally. DRT `requires_wheelchair` flag is accessibility-related and treated as quasi-sensitive (minimised: boolean only, tombstoned on erasure, never exposed to other riders). |

## 2. Necessity & proportionality

- **Pseudonymous-by-design**: no names/e-mails/phones in platform databases;
  identity attributes stay in Keycloak (docs/GDPR.md §1). This is the
  primary proportionality measure.
- **No payment credentials**: card/account data never transits the platform;
  Mojaloop holds the rails, the platform stores amounts and transfer ids.
- **Fare capping** requires same-day spend per rider — justified (contract);
  the daily sum is computed in-place, not exported to a profile store.
- **Telemetry is vehicle-keyed**, not rider-keyed; no passenger tracking.

## 3. Risk assessment

| risk | L | S | mitigation | residual |
|---|---|---|---|---|
| Re-identification of riders from platform DB | M | H | Pseudonymous model; Keycloak mapping admin-gated + audited; no PII columns anywhere (schema-level guarantee) | Low |
| Insider reads rider history | M | M | Role model (Permify + JWT roles), hash-chained WORM audit trail with anomaly alerts, auditor role separation (Wave-6 A4-03), docs/INSIDER_THREAT.md | Low |
| Erasure incomplete (derived state, backups) | M | M | Single-tx tombstone across all rider tables; `gdpr.erasure.completed` event for downstream scrub; backup-restore erasure replay procedure (DATA_RETENTION.md §2) | Low |
| Payment data breach | L | H | No credentials stored; Mojaloop + TigerBeetle fail-closed; idempotency keys; dual-token ingest rollover for audit (Wave-6 A3-08) | Low |
| Unlawful audit-trail retention | L | M | 7-year horizon + salt destruction (retention schedule); Art. 17(3)(b) basis documented (GDPR.md §5) | Low |
| Accessibility flag misuse | L | M | Boolean only, owner-scoped reads, tombstoned on erasure, no admin list endpoint exposing it per-rider | Low |

(L = likelihood, S = severity: L/M/H.)

## 4. Measures mapped to articles

| article | measure |
|---|---|
| Art. 15 access | Self-serve export endpoint (GDPR.md §3) |
| Art. 17 erasure | Self-serve tombstone erasure (GDPR.md §4), audit exception §5 |
| Art. 25 by-design/default | Pseudonymous model, no-PII schema, fail-closed salt, TTL'd caches |
| Art. 30 records | This DPIA + data map (GDPR.md §2) |
| Art. 32 security | mTLS via APISIX policies, JWT roles, WORM audit, secrets externalised (docs/SECRETS.md), encrypted backups |
| Art. 33 breach notification | Incident-response runbook §credential-revocation (docs/INCIDENT_RESPONSE.md); evidence packs give regulators the tamper-evident slice |

## 5. Consultation

DPO sign-off required before production go-live and after any change to §1
(new processing purpose or new rider-keyed table — see the three-place rule
in DATA_RETENTION.md §4).

## 6. Review

Annual, and on: new module with rider data, new external integration, any
personal-data breach. Owner: DPO + platform lead.
