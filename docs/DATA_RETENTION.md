# H2Fleet Data Retention Schedule

Two columns matter: **enforcement** distinguishes what the platform enforces
in code/config today from what is an operator policy executed on a schedule.
Nothing in this document is aspirational — "policy" rows include the exact
command/SQL to run.

## 1. Operational data

| data | store | retention | enforcement |
|---|---|---|---|
| Raw telemetry (`fleet.telemetry`) | TimescaleDB hypertable | **90 days** | **Enforced**: `timescaledb` retention policy (migration `0004`), compression after 7 days. Ingest also rejects records older than 90 days (`StaleTs`) so backfills cannot resurrect dropped data. |
| Kafka event topics (`safety.*`, `telemetry.*`, `fare.*`, …) | Kafka | 7 days | **Enforced**: topic-level `retention.ms` at provisioning (see `infra/kafka/`). |
| Redis caches / idempotency keys | Redis | ≤ 24 h per key TTL | **Enforced**: every `SET` carries an explicit TTL (no persistent keys). |
| Webhook deliveries (`infra.webhook_deliveries`) | Postgres | 90 days | **Policy** (quarterly): `DELETE FROM infra.webhook_deliveries WHERE attempted_at < now() - interval '90 days';` — subscriptions themselves persist until revoked. |
| Dispatch jobs, work orders, incidents | Postgres | life of platform | Operational record of the authority; incidents feed regulator evidence packs (docs/GAP_AUDIT.md A4-04) and are not time-deleted. |

## 2. Compliance & financial data

| data | store | retention | enforcement |
|---|---|---|---|
| Audit trail (`platform.audit_log`) | Postgres (WORM, hash-chained) | **7 years**, then destroy salt + rows | **Enforced**: append-only (no UPDATE/DELETE path exists in the service). Time-boxing is **policy** (annual): after the 7-year horizon, delete rows older than the horizon *and* destroy `GDPR_ERASURE_SALT` — this renders both the trail and all erasure tombstones permanently unlinkable (docs/GDPR.md §4–5). |
| OpenSearch audit mirror | OpenSearch | 7 years, matched to trail | **Policy**: ISM policy deleting monthly indices older than 84 months (`infra/opensearch/`). The Postgres trail is the source of truth; the mirror is rebuildable. |
| Fare payments, loyalty ledger, trades | Postgres + TigerBeetle | **10 years** (statutory, tax/commercial law) | **Enforced** by absence of any delete path; rider link is pseudonymised on erasure (tombstone), financial substance retained per GDPR.md §4. |
| Backups (pg_dump + TigerBeetle file) | MinIO bucket (`infra/backup/backup.sh`) | 14 days rolling | **Enforced**: `BACKUP_RETENTION_DAYS` (default 14) prune in `backup.sh`. Verified restorable by the quarterly drill + `verify_restore.sh` (docs/DR.md). Note: backups contain pre-erasure pseudonyms — a restored backup must have erasures **re-applied** (replay `gdpr.erasure.completed` markers from the audit trail). |

## 3. Identity data

| data | store | retention | enforcement |
|---|---|---|---|
| Rider accounts (name/e-mail/credentials) | Keycloak | until account deletion | IdP-owned; deletion in Keycloak is the identity-erasure step of a DSAR (docs/GDPR.md §6 step 4). The platform-side tombstone makes platform rows unlinkable independently. |

## 4. Data minimisation notes

- Riders are pseudonymous (`sub`) everywhere in the platform — see the data
  map in docs/GDPR.md §2.
- Telemetry carries no rider key; DRT requests carry pickup/dropoff
  coordinates and are tombstoned with the rider on erasure.
- New tables with rider keys MUST be added to: the erasure transaction in
  citizen-api `me.go`, the export sections, and this schedule — all three,
  in the same PR. The GDPR docs checklist is part of review.
