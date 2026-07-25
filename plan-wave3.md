# Plan — Wave 3: Hardening, Deep Audits, Scenarios, 100/100 Push

## Wave A (4 parallel agents)
- **A1 Business-logic auditor** (reviewer): all 20 features — business rule accuracy/completeness, data-flow tracing end-to-end, orphan/dead code, missing schema vs code expectations → docs/BUSINESS_LOGIC_AUDIT.md + ranked gaps.
- **A2 Security auditor** (reviewer): external+insider exploitation paths, OWASP Top 10, dependency CVEs (govulncheck/cargo audit/pip-audit/npm audit/osv), OSS component CVE table (kafka, apisix, keycloak, postgres, redis, opensearch, temporal, tigerbeetle, mojaloop, permify, openappsec, fluvio, minio, mlflow, neo4j, ray) → docs/SECURITY_AUDIT.md + docs/OSS_VULNERABILITIES.md + ranked fixes.
- **A3 Middleware hardening** (coder): HA/prod overlays (Kafka KRaft 3-node prod overlay or Strimzi k8s, Postgres CNPG HA, Redis Sentinel+AOF, OpenSearch 3-node, Keycloak prod start+cluster, APISIX etcd+2rep prod, TigerBeetle 6-replica guide+scripts, OpenAppSec prevent profile, Permify HA), REAL Fluvio edge agent (Rust, bus gateway → SC → Kafka bridge), Mojaloop real quoting flow (SDK-style parties/quotes/transfers against simulator + prod-switch docs + MySQL-vs-Postgres answer + MySQL tuning), perf tuning configs (postgresql.conf, kafka prod properties, redis.conf, opensearch jvm/cluster, apisix worker, TB batching), throughput engineering notes (millions TPS honest analysis).
- **A4 Platform assurance** (coder): insider-threat program (audit-log Go service, hash-chained immutable audit entries → Postgres+OpenSearch, instrument admin-api/toggle/commerce admin actions, break-glass, docs/INSIDER_THREAT.md), Drizzle ORM innovation (packages/db Drizzle schema + TS analytics BFF service w/ Hono — no Go rewrites), cache busting (index.html no-store at nginx+APISIX, SW version-change purge, meta tags), 10 stakeholder scenario scripts + runner (tests/e2e/scenarios/).

## Wave B (after A1/A2 land)
- Fix agents: implement all A1 business-logic gaps + A2 security fixes.
- Scenario validation: run all 10 scenarios against mocks; fix discoveries.

## Wave C
- End-to-end compile gate (Go/Rust/Python/TS, zero errors), full test suite, push to GitHub, final production score + report (Q2/Q4 answers, remaining env-bound residuals).
