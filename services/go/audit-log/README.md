# audit-log (:8086)

Append-only, **hash-chained audit trail** for H2Fleet's insider-threat program
(see `docs/INSIDER_THREAT.md`). Gateway prefix `/api/audit/*` (proxy-rewrite
strips the prefix).

## API

| Method | Path                | Auth                                   | Purpose |
|--------|---------------------|----------------------------------------|---------|
| POST   | `/v1/audit`         | `X-Audit-Token` shared secret **or** JWT | Append one entry |
| GET    | `/v1/audit`         | JWT realm role `platform-admin`        | Search: `?actor=&entity=&from=&limit=` (RFC3339 `from`, limit ≤ 1000, default 100) |
| GET    | `/v1/audit/verify`  | JWT realm role `platform-admin`        | Hash-chain integrity check (`200 ok:true`, `409` + `first_bad_id` when broken) |
| GET    | `/healthz`          | public                                 | Liveness/readiness (pings Postgres) |
| GET    | `/metrics`          | cluster                                | Prometheus |

Entry shape (POST body): `actor_sub*` `action*` `entity*` `entity_id`
`actor_roles[]` `before` `after` (JSON) `ip` `ua` `ts` (optional; server
assigns). Response echoes the stored entry including `id`, `prev_hash`,
`hash`.

## Tamper evidence

`platform.audit_log` rows are chained: `hash = SHA-256(prev_hash ‖
length-prefixed fields)`. Appends hold a Postgres advisory xact lock so
concurrent writers cannot fork the chain. `before`/`after` are normalized
through `jsonb` **before** hashing so `GET /v1/audit/verify` recomputes over
the exact stored text. Any UPDATE/DELETE/reorder breaks verification.

**DBA hardening (recommended, not automated):** grant the app role
`INSERT,SELECT` only on `platform.audit_log` and take quarterly logical
backups of the table (or anchor the head hash externally) so a rogue admin
with superuser cannot rewrite history silently.

## OpenSearch mirror

Every accepted entry is also indexed (best-effort, async) into OpenSearch
index `h2fleet-audit` for SOC search. Postgres is authoritative; failures are
logged and counted (`audit_mirror_failures_total`) and never fail the write.

## Anomaly detector

In-service sliding-window rate check: more than `AUDIT_ANOMALY_THRESHOLD`
(default 20) sensitive actions per actor per `AUDIT_ANOMALY_WINDOW` (default
1m) → warning log + `audit_anomaly_alerts_total` + best-effort Alertmanager
alert (`H2FleetAuditAnomaly`, 5-minute per-actor cooldown).

## Env

`PORT` (8086), `DATABASE_URL` (required), `KEYCLOAK_ISSUER`,
`AUDIT_INGEST_TOKEN` (shared service token; empty = JWT-only ingest),
`OPENSEARCH_URL` (default `http://opensearch:9200`; empty disables mirror),
`OPENSEARCH_INDEX` (`h2fleet-audit`), `ALERTMANAGER_URL`,
`AUDIT_ANOMALY_THRESHOLD`, `AUDIT_ANOMALY_WINDOW`.

## Emitters

Other services emit via `pkg/auditclient` (chi middleware +
`auditclient.FromEnv`), configured with `AUDIT_LOG_URL` /
`AUDIT_INGEST_TOKEN`. Wired into: admin-api (user mgmt, onboarding
decisions, toggle proxy), toggle-service (PUT toggles), commerce-api
(payment/trade/campaign create).

## Tests

`go test ./...` — hash-chain properties + tamper cases, anomaly window/
cooldown, handler validation/filters/verify/auth (in-memory store),
auditclient middleware behavior (httptest).
