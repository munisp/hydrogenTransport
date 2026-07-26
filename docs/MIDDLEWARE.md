# Middleware Robustness & Integration Scorecard

Evidence-based audit of every middleware component in H2Fleet: what actually
uses it (code-verified), how robust the deployment is, and the concrete gaps
to production. Scores are 0–10, where 10 means production-grade (HA,
persistent, secured, observable, backed up, graceful dependent behavior).

**Method.** Sources: `SPEC.md §3.8`, `infra/docker-compose.yml`, `infra/apisix/`,
`infra/dapr/`, `infra/sql/`, `infra/backup/`, `infra/k8s/`, `docs/`, and grep of
`services/{go,rust,python}`, `packages/`, `apps/` for real client usage.

**Baseline context (applies to every component below):**

* All middleware runs **only** in `infra/docker-compose.yml`. `infra/k8s/base/`
  contains manifests for the 10 app services + PWA only — **no StatefulSets,
  operators, or Helm releases for any middleware**. There is no production
  deployment story for the stateful plane.
* Every middleware is a **single container instance**. Nothing is clustered,
  replicated, or failover-capable.
* The middleware data plane is **unencrypted and unauthenticated** throughout:
  Kafka PLAINTEXT, Postgres `sslmode=disable`, Redis no AUTH, TigerBeetle no
  TLS, OpenSearch `plugins.security.disabled: true`, Permify gRPC
  `insecure.NewCredentials()`, Temporal dev server. Compose depends on network
  isolation (`h2net` bridge) alone.
* `x-h2-env` (compose lines 41–49) is the shared wiring: `DATABASE_URL`,
  `KAFKA_BROKERS`, `REDIS_ADDR`, `TOGGLE_URL`, `KEYCLOAK_ISSUER`,
  `KEYCLOAK_AUDIENCE`.

---

## 1. Postgres / TimescaleDB — score 6/10

### Integration depth
* **Used by every platform service.** Go services use `pgxpool`
  (`services/go/*/internal/handlers/db.go`); Python services use `asyncpg`
  (`predictive-maintenance`, `route-optimizer`, `carbon-analytics`,
  `telemetry-simulator`); Rust services connect for fleet metadata and the
  telemetry hot path (`telemetry-ingest/src/store.rs`, `digital-twin` snapshots
  — `twin.rs`: "periodically snapshots to Postgres").
* **Temporal** has its own dedicated instance (`postgres-temporal`, compose
  lines 123–137) — correct blast-radius isolation.
* **Permify** persists to a `permify` database on the main instance
  (`permify-db-init`, `permify-migrate`).
* **TimescaleDB is real, not decorative**: `fleet.telemetry` is a hypertable
  (`migrations/0001_core.sql:57`), with a dedup `UNIQUE(bus_id, ts)`, a 90-day
  retention policy, and 7-day compression policy (`migrations/0004_telemetry_dedup.sql`).
* Schema governance is good: idempotent first-boot init + goose forward
  migrations; all app services `depends_on` migrator success.

### Robustness
* Healthcheck: `pg_isready` (adequate for compose).
* **Backups exist and run**: `infra/backup/backup.sh` does scheduled
  `pg_dump -Fc` of both databases to MinIO `h2-backups` with 14-day retention;
  `docs/DR.md` has a documented restore drill with RTO target < 1 h.
* Failure behavior of dependents: services fail to start without PG
  (`depends_on: service_healthy`); runtime outage behavior varies — toggle
  reads degrade via Redis cache, most handlers return 500. No retry/circuit
  breaker layer.

### Production gaps (ranked)
1. **Single instance, no replication/failover** — no streaming replica, no
   Patroni/managed-PG story; total write outage + potential data loss on node
   failure.
2. **pg_dump is not PITR** — no WAL archiving/base backups; RPO is up to
   `BACKUP_INTERVAL_SECONDS` (24 h default).
3. `sslmode=disable` in transit; single superuser-ish `h2` role shared by all
   services and Permify.
4. No connection pooler (pgbouncer) — 10 services × pgxpool against one PG.
5. Backups land on single-node MinIO in the same compose stack (same-fate
   infrastructure).
6. Restore drill is documented but there is no evidence it is automated/timed.

---

## 2. TigerBeetle — score 4/10

### Integration depth
* **Only `commerce-api` actually uses it** — `internal/ledger/ledger.go` (real
  `tigerbeetle-go` client, double-entry transfers for fare payments and energy
  trading per SPEC §3.4 account map). Strong correctness details: deterministic
  transfer IDs from idempotency keys (SHA-256 namespaced) so retries dedupe
  (`TransferExists`); persisted rider→wallet mapping; payments persist status
  **before** publishing events (outbox-lite ordering); a failed TB transfer
  marks the payment failed and returns 502 — **fail-closed, no fabricated
  ledger entries** (`payments.go:135–183`).
* Honest simulated fallback: in-memory ledger only behind the explicit dev
  opt-in `H2_SIMULATED_LEDGER=true` when `TIGERBEETLE_ADDR` is unset (startup
  fails closed otherwise), clearly logged as a warning.
* **Dead wiring**: `TIGERBEETLE_ADDR` is set in compose for `telemetry-ingest`
  (line 949) and `carbon-analytics` (line 1038), but grep shows **zero
  TigerBeetle references in either codebase**. The env vars imply an
  integration that does not exist.

### Robustness
* Single replica started with `--development` (compose line 523); TigerBeetle
  itself recommends a 6-replica cluster for production. No quorum = the ledger
  is down whenever the container is down, and payments 502.
* No TLS (`--development` disables it); no real healthcheck — the compose
  healthcheck is literally `exit 0` (line 528), so "healthy" means "container
  running", not "replica serving".
* Backup = crash-consistent single-file copy to MinIO (backup.sh); restore
  requires stopping TB (docs/DR.md §4). Correct as far as it goes, but there is
  no quorum to recover from corruption of that one file + its snapshot.

### Production gaps (ranked)
1. Single replica vs recommended 6-node cluster; no consensus, no failover.
2. `--development` mode (no TLS) — ledger traffic unauthenticated in-net.
3. Healthcheck is a stub; dependents can route to a non-serving replica.
4. No reconciliation job verifying Postgres `fare_payments` vs TB account
   balances (cron-settlement-hourly Dapr binding exists but there is no
   evidence of an implemented reconciler against TB).
5. Dead `TIGERBEETLE_ADDR` wiring in two services should be implemented or
   removed (SPEC §3.4 CARBON_FUND movements currently have no writer).

---

## 3. Redis — score 5/10

### Integration depth
* **toggle-service**: read-through cache `toggles:<module>` TTL 30 s
  (`handlers/toggles.go`); cache errors log-and-continue to Postgres — correct
  fail-open for a cache. Healthz reports Redis status.
* **digital-twin**: twin hot state lives in Redis (`api.rs`, `twin.rs`);
  `/healthz` reports `degraded` when Redis pings fail; read endpoints return
  **502** when Redis is down (fail-closed for the twin API — acceptable because
  Postgres snapshots are the fallback store but are not served by those
  endpoints).
* **telemetry-simulator**: writes `bus:meta:*` enrichment hashes that
  telemetry-ingest joins (sync dependency on the hot path).
* **Dapr statestore** component (`infra/dapr/components/statestore-redis.yaml`)
  for citizen/commerce services.

### Robustness
* **AOF persistence is enabled** (`redis-server --appendonly yes`, compose line
  361) with a named volume — better than a pure cache; restart survives.
* Healthcheck `redis-cli ping`. No Sentinel, no replica, no AUTH, no TLS, no
  `maxmemory`/eviction policy configured.
* Failure blast radius: Redis down ⇒ toggle writes still work (PG), twin API
  502s, simulator stops (waits for Redis), enrichment hashes vanish ⇒
  telemetry-ingest joins degrade. AOF recovery is single-node; corruption of
  the AOF = data loss of twin hot state (mitigated by PG snapshots).

### Production gaps (ranked)
1. Single node; no Sentinel/Cluster — twin hot-state and Dapr statestore have
   no failover.
2. No AUTH/TLS on a store holding session-ish and twin state.
3. No `maxmemory-policy`; AOF on a busy cache + statestore will grow and can
   stall on fsync under load (no `appendfsync` tuning documented).
4. Redis AOF/volume is **not included in the backup job** (backup.sh covers PG
   + TB only).
5. No cache/versioning story for toggle propagation beyond 30 s TTL.

---

## 4. Mojaloop — score 3/10

### Integration depth (honest: this is a simulator, not a switch)
* Compose runs `mojaloop/simulator:v14.2.0` (lines 490–505) — the dev
  simulator, **not** the Helm-deployed switch (the compose comment says so
  explicitly).
* `commerce-api` integration is real as far as the simulator goes:
  `mojaloopTransfer()` (`payments.go:243+`) POSTs a transfer to
  `MOJALOOP_ENDPOINT`, stores the echoed/client-generated transfer id, and on
  any transport error or non-2xx marks the payment with the classified
  `mojaloop_*` status and returns 502 — **it never fabricates a transfer
  id**. Without an endpoint the leg fails closed (`mojaloop_unavailable`)
  unless the explicit dev opt-in `H2_SIMULATED_MOJALOOP=true` returns a
  clearly-labelled `ml-simulated-*` id. The Mojaloop leg is opt-in per
  request (`use_mojaloop`).
* `mojaloop_transfer_id` is persisted in `commerce.fare_payments`.

### Robustness / gaps (ranked)
1. **No real switch**: no quoting/party-lookup flow exercised end-to-end, no
   oracle, no settlement — the FSPIOP happy path is a single POST against a
   stub. Integration depth with actual Mojaloop semantics is shallow.
2. **No FSPIOP security**: no JWS signing, no mTLS, no DFSP onboarding —
   all required against a real switch.
3. Simulator has no persistence (`CACHE_ENABLED: false`); restart loses all
   transfer state, while Postgres keeps `mojaloop_transfer_id`s pointing at
   transfers that no longer exist anywhere.
4. No reconciliation between `fare_payments.mojaloop_transfer_id` and switch
   state (the hourly settlement cron binding exists but there is no evidence
   of an implemented Mojaloop reconciler).
5. Payment UX gap: `mojaloop_failed` payments have no retry/compensation path
   visible in code.

---

## 5. Kafka — score 4/10

### Integration depth
* The true event backbone. Producers/consumers verified in code:
  * Rust hot path (`rdkafka`): `telemetry-ingest` consumes `telemetry.raw` →
    publishes `telemetry.enriched`; `digital-twin` consumes
    `telemetry.enriched` → publishes `twin.updated`.
  * Go publishers (`franz-go`): toggle-service, infra-api, commerce-api
    (`internal/events/publisher.go`).
  * Python (`aiokafka`): predictive-maintenance consumer+producer
    (`app/events.py`), carbon-analytics (`app/core.py`), telemetry-simulator.
  * Dapr `pubsub.kafka` component (`h2pubsub`) fronts citizen-api/commerce-api.
* **A DLQ exists**: telemetry-ingest dead-letters poison batches to
  `telemetry.raw.dlq` after a bounded insert retry budget, then commits offsets
  so the pipeline never stalls (`pipeline.rs:180–210`). This is the strongest
  stream-processing robustness feature in the repo — but it exists **only** on
  the telemetry path; no other consumer has a DLQ.

### Robustness
* **Single broker, ZooKeeper mode** (bitnami/kafka:3.7 with
  `KAFKA_CFG_ZOOKEEPER_CONNECT`; **not** KRaft), plus a single ZK with
  `ALLOW_ANONYMOUS_LOGIN: yes`.
* `DEFAULT_REPLICATION_FACTOR: 1`, `AUTO_CREATE_TOPICS_ENABLE: true`,
  PLAINTEXT listeners only, no SASL/TLS, no quotas.
* rf=1 + auto-create = any topic (including the DLQ) has one copy; broker loss
  = data loss. No min.insync.replicas, no backup of topic data.
* Healthcheck is real (`kafka-topics.sh --list`).

### Production gaps (ranked)
1. Single broker + replication factor 1 + single ZooKeeper (two SPOFs in
   series).
2. PLAINTEXT, no authentication/authorization — any container on `h2net` can
   produce/consume anything, including `fare.payment.*`.
3. DLQ only on telemetry-ingest; payment/incident/twin consumers have no
   poison-message strategy.
4. Auto-created topics: no explicit partitions/retention per SPEC §3.3 topics.
5. No topic backup/MirrorMaker; Kafka volume excluded from backup job.
6. KRaft migration needed anyway (ZK is deprecated upstream).

---

## 6. APISIX — score 5/10

### Integration depth
* The supported public entrypoint (SPEC §3.6): full route map for all 9 routed
  services with prefix-strip `proxy-rewrite`, plus a dedicated stricter
  rate-limited route for the leak-sensor webhook (`infra/apisix/apisix.yaml`).
* Defense-in-depth auth done right: `openid-connect` bearer-only with
  `unauth_action: pass` at the edge, backends enforce their own JWT middleware;
  `key-auth` for the machine-to-machine open-data route; carbon-analytics
  deliberately unmapped (internal only).
* Prometheus plugin enabled and scraped; CORS restricted to PWA origin;
  OpenAppSec global rule wired (detect mode).

### Robustness
* **Standalone mode** (`config_provider: yaml`, `infra/apisix/config.yaml`) —
  routes load from a mounted file; **no etcd**, so no persistent/dynamic
  control plane and no multi-instance config distribution. Config change =
  file edit + reload of the single container.
* Admin API locked down correctly: loopback-only bind, `allow_admin
  127.0.0.1/32`, env-injected admin key, `:9180` not published.
* Single instance; gateway down = entire API surface down. Healthcheck on
  `/apisix/status`. TLS block is fully prepared but commented out (dev runs
  plain HTTP).

### Production gaps (ranked)
1. Single instance, no HA — total ingress SPOF; no second node or LB story.
2. No etcd/dynamic config — no runtime route updates, no canary/weight changes
   without restart; standalone file config does not scale past one node.
3. TLS disabled by default (documented opt-in).
4. Rate limiting exists only on the leak route; general API routes are
   unthrottled.
5. Secrets (`KEYCLOAK_SERVICES_CLIENT_SECRET`) inline in apisix.yaml via env
   substitution — fine locally, needs a secrets manager story in prod.

---

## 7. Keycloak — score 4/10

### Integration depth
* Deep and correct integration: shared `packages/go-auth` RS256 JWT middleware
  in all Go services (issuer allow-list incl. browser-facing alt issuer,
  optional audience check, JWKS cached with **stale-key fallback on transient
  JWKS outage** — `jwt.go:280–281`, a well-chosen degradation), Python
  `h2fleet_auth` for FastAPI services, APISIX OIDC plugin, PWA
  `apps/pwa/src/auth/keycloak.ts`.
* Realm import is template-rendered with secret substitution
  (`keycloak-realm-init` + `substitute-realm.sh`) — no plaintext secrets in the
  realm template.

### Robustness
* **`start-dev` with `KC_DB: dev-file` (H2 file DB)** — compose line 258–263.
  Users/roles/sessions live in an embedded H2 file on a volume; the comment
  itself says "swap for postgres in prod". No clustering, no external DB, no
  HA.
* `KC_HTTP_ENABLED: true`, hostname `http://localhost:8180`, default admin
  credentials `admin/admin` (overridable via env but defaulted).
* Failure behavior: Keycloak down ⇒ token *issuance* stops, but services keep
  validating existing tokens via cached JWKS — good. New logins and key
  rotation fail.

### Production gaps (ranked)
1. Dev mode + embedded H2 DB; must move to `start` with optimized build +
   external Postgres.
2. HTTP only; no TLS at the IdP.
3. Single instance; no clustered Infinispan — IdP outage eventually locks out
   all new auth.
4. Dev passwords default in env interpolation (`operator123`, `driver123`…).
5. Keycloak data (H2 volume) not covered by the backup job; realm exists only
   as a template + runtime mutations.

---

## 8. OpenAppSec (WAF) — score 2/10

### Integration depth
* Attached to APISIX via the `openappsec` plugin in the **global rule**
  (`apisix.yaml:269–271`) — so when active it covers every route. That is the
  right integration point.

### Robustness / reality check
* **Opt-in profile** (`profiles: ["waf"]`) — not part of the default or even
  the `all` profile. **Detect mode only** (`mode: detect`; flipping to
  `prevent` is a manual edit). Agent runs standalone with `AGENT_TOKEN: ""` and
  `LEARNING_MODE: "true"` — no cloud management, no policy versioning, no
  enforcement.
* The config's own comment warns: if the plugin is enabled but the agent is
  not deployed, "an unreachable agent can add latency or fail requests" — i.e.
  the failure mode of a half-enabled WAF is ambiguous and can become a
  self-inflicted outage. No healthcheck on the agent container.

### Production gaps (ranked)
1. Effectively off: detect-only, profile-gated, learning mode, unmanaged agent.
2. Fail-open/fail-closed behavior on agent loss undocumented/untested.
3. No policy-as-code, no tuning, no alerting on detections (nothing ships
   detections to Prometheus/Alertmanager).
4. No healthcheck/dependency gating between apisix and the agent.

---

## 9. Permify — score 5/10

### Integration depth
* Real ReBAC on admin routes: hand-written minimal gRPC client
  (`packages/go-auth/permify.go`) calling `PermissionService/Check` for tenant
  `t1`; `PERMIFY_GRPC` wired into toggle/fleet/infra/citizen/commerce services.
* **Failure semantics are explicitly documented and fail-closed when
  configured**: DENIED ⇒ 403, transport/check error ⇒ 502, "the route never
  silently allows". When `PERMIFY_GRPC` is unset the middleware degrades to
  Keycloak realm-role-only with a one-time warning (hybrid fallback — fine for
  dev, dangerous if it happens silently in prod; 5 s check timeout).

### Robustness
* **Postgres-backed** (`PERMIFY_DATABASE_ENGINE: postgres` against the main
  instance) with a proper bootstrap chain: idempotent `permify-db-init` →
  `permify-migrate` (Permify's own schema migrations) → `serve` →
  `permify-setup` loads `infra/permify/schema.perm`. Well-engineered for
  compose.
* Weak points: gRPC uses `insecure.NewCredentials()`; schema load is
  **best-effort** ("use Permify UI/CLI to verify" — the curl writes a
  sed-mangled schema and swallows failure); single instance; no relationship
  tuples seeded beyond the schema, so checks may deny until data is written.

### Production gaps (ranked)
1. Schema bootstrap is best-effort and can silently leave authz unmodelled —
   combined with fail-closed checks this means **admin routes 502/403 for
   everyone** after a bad deploy; needs a verified, failing-loud init job.
2. No tuple seeding/management pipeline (who writes relationships?).
3. Single instance, no HA; every admin-route check is a sync gRPC call to one
   container.
4. Plaintext gRPC.
5. Role-only fallback path could mask a misconfigured prod deployment — add a
   startup hard-fail when `PERMIFY_GRPC` is expected but unreachable.

---

## 10. OpenSearch — score 4/10

### Integration depth
* Backs the open-data portal: `citizen-api` `opendata.go` proxies search to
  `OPENSEARCH_URL`/`OPENSEARCH_INDEX=h2fleet-open`; returns a clean **503
  "search backend not configured"** when unset (honest degradation).
* `opensearch-bootstrap` (one-shot, idempotent) creates the index with
  explicit mappings and upserts the dataset catalog — a real provisioning job,
  not a TODO.
* OpenSearch Dashboards provisioned for exploration.

### Robustness
* `discovery.type: single-node`, **security plugin disabled**, 512 MB heap —
  fine for dev, nothing more. Healthcheck on `_cluster/health`. Named volume.
* No index snapshots (repository to MinIO would be trivial but absent); no
  ISM policies; the "telemetry search" use case from SPEC §3.8 is not visible
  in code (only the open-data index exists).

### Production gaps (ranked)
1. Single node + security disabled (no authn/z, no TLS) on a search API.
2. No snapshot/restore — index rebuild depends on re-running bootstrap +
   whatever pipeline loads datasets (only the catalog is bootstrapped).
3. Telemetry search (SPEC §3.8) not implemented — no ingest pipeline from
   Kafka/Timescale into OpenSearch.
4. No ISM/retention; unbounded growth if telemetry indexing is added.
5. Heap fixed at 512 MB; Dashboards also security-disabled.

---

## 11. Fluvio — score 1/10

### Integration depth (honest: none)
* The compose comment is admirably blunt: the Fluvio→Kafka edge bridge "is a
  documented design, **not yet implemented**; locally the telemetry-simulator
  publishes straight to Kafka" (compose lines 99–104). Grep of all service
  code finds **zero** Fluvio client usage. The only runtime artifact is the
  container itself (profile `fluvio`/`all`) with a healthcheck.
* Score 1 rather than 0 because the design intent is documented in the right
  place and the container + healthcheck actually work — but no byte of
  telemetry ever touches Fluvio today.

### Production gaps (ranked)
1. Bridge unimplemented — the entire edge-streaming story is a comment.
2. `fluvio-run start --local` is a single-node dev cluster; no SC/SPU
   topology, no persistence guarantees for the edge use case.
3. No decision recorded on Fluvio vs plain Kafka at the edge — risk of
   carrying two streaming systems for one path.

---

## 12. Neo4j — score 0/10

* **Absent today.** No service, compose entry, client code, or docs reference;
  the only mention in the repo is the wave-2 plan ("compose for admin-api,
  ml-platform, mlflow, neo4j (profile), ray (profile)" — `plan-wave2.md:8`).
* Being added as an optional graph profile (presumably for route/network
  graphs). Assessment of what exists today: nothing to integrate, nothing to
  harden.
* When it lands, the baseline requirements are known from this audit: named
  volume + APOC constraints, auth enabled (no default neo4j/neo4j), bolt+TLS,
  a real healthcheck (`cypher-shell`), inclusion in the backup job
  (`neo4j-admin database dump` to MinIO), profile-gating like Fluvio, and at
  least one real consumer service before claiming integration points.

---

## 13. AI/ML stack — score 3/10

### What exists TODAY (honest: sklearn, not PyTorch)
* **predictive-maintenance** is the only ML-ish service. It loads
  `models/model.joblib` — a `RandomForestClassifier` artifact produced by
  `train.py` (`scikit-learn==1.6.1`, `joblib`, `numpy` in requirements.txt) —
  and falls back to a **deterministic rule model** when the artifact is absent
  (`app/model.py:114+`). Inference is wired into a real aiokafka consumer loop
  (`app/events.py`). There are unit tests for both model paths. This is a
  legitimate, working sklearn service — but there is **no PyTorch anywhere**
  (no torch import in the repo).
* **route-optimizer** is `ortools` (CP-SAT/VRP) — optimization, not ML.
* **No ML platform**: no MLflow, no model registry, no feature store, no GPU
  node, no batch/retrain scheduler (training is a manual `python train.py`),
  no drift monitoring. Wave-2 plan mentions ml-platform/mlflow/ray — not
  present.
* Model delivery gap: the joblib artifact is not built/baked by the Dockerfile
  pipeline visible here — the service ships with the rule fallback unless a
  model file is mounted/generated.

### Production gaps (ranked)
1. PyTorch rebuild not started; current model is a small RF on synthetic
   samples (`train.py --samples 5000`).
2. No model registry/versioning beyond a `version` string inside the joblib
   dict; no rollback story.
3. No retraining pipeline or schedule; training data source is synthetic.
4. No evaluation/monitoring in production (no drift, no accuracy feedback
   loop from actual maintenance outcomes).
5. Model artifact not part of the image/CI — deployment depends on a manual
   step.

---

## Summary score table

| # | Component | Score | One-line verdict |
|---|-----------|-------|------------------|
| 1 | Postgres/Timescale | **6** | Deepest integration, real hypertable+policies+backups; single instance, no PITR |
| 2 | Permify | **5** | Postgres-backed, fail-closed checks, hybrid fallback; best-effort schema init |
| 3 | Redis | **5** | AOF on, well-used cache/hot-state; single node, no auth/TLS, not backed up |
| 4 | APISIX | **5** | Correct standalone config & lockdown; single instance, no etcd, TLS off |
| 5 | Kafka | **4** | Real backbone with DLQ on telemetry path; 1 broker ZK-mode rf=1 PLAINTEXT |
| 6 | Keycloak | **4** | Solid JWT integration w/ JWKS stale fallback; start-dev + H2 dev-file |
| 7 | TigerBeetle | **4** | Exemplary idempotent ledger client; single --development replica, stub healthcheck |
| 8 | OpenSearch | **4** | Real bootstrap + search proxy; single node, security disabled, no snapshots |
| 9 | Mojaloop | **3** | Simulator-only leg, honest failure marking; no real switch/JWS/reconciliation |
| 10 | AI/ML stack | **3** | Working sklearn+rule fallback in a real consumer; no PyTorch/registry/retrain |
| 11 | OpenAppSec | **2** | Right plugin point, but profile-gated detect-only learning mode |
| 12 | Fluvio | **1** | Documented edge bridge explicitly not implemented; zero code usage |
| 13 | Neo4j | **0** | Absent; only a wave-2 plan line |

Average: **3.5/10** — a well-wired dev stack with honest fallbacks, not a
production middleware plane.

---

## Hardening roadmap — what 10/10 takes per component

**Postgres.** Managed HA (Patroni/CNCP or cloud RDS) with synchronous replica +
auto-failover; WAL archiving + base backups (PITR, RPO minutes); TLS +
per-service roles; pgbouncer; off-site backup copy; automated quarterly
restore drill with timing evidence.

**TigerBeetle.** 6-replica cluster across AZs, production mode with TLS; real
readiness probe (client handshake); keep the deterministic-idempotency client;
implement the Postgres↔TB settlement reconciler on the hourly cron; either
implement or delete the dead `TIGERBEETLE_ADDR` wiring in telemetry-ingest and
carbon-analytics (CARBON_FUND movements need a writer).

**Redis.** Sentinel (or managed) with replica + auto-failover; AUTH + TLS;
separate instances (or logical DBs with policies) for cache vs twin-hot-state
vs Dapr statestore; `maxmemory` + eviction policy; include AOF in backups.

**Mojaloop.** Deploy the real switch via the official Helm chart; implement
quotes/parties/transfers end-to-end with JWS signing + mTLS and DFSP
onboarding; persistent settlement with a reconciler against
`fare_payments`; retry/compensation flow for `mojaloop_failed` payments.

**Kafka.** 3+ brokers, KRaft mode, rf=3 + min.insync.replicas=2; SASL/TLS +
ACLs; explicit topic provisioning (partitions/retention per SPEC §3.3) instead
of auto-create; DLQ pattern extended to all consumers; MirrorMaker 2 or
topic backup; cruise-control-style rebalancing.

**APISIX.** 2+ data-plane nodes behind an LB; etcd-backed (or APISIX
Ingress/controller) control plane for dynamic config; enable the prepared TLS
block with cert-manager; global rate limiting; WAF in prevent mode (below).

**Keycloak.** `start` (optimized) on external Postgres; 2+ nodes clustered;
TLS everywhere; realm + user federation managed as code; backup of the KC
database; break-glass admin flow; remove all dev default passwords from
compose defaults.

**OpenAppSec.** Always-on (not profile-gated); prevent mode after a measured
learning period; managed policy as code; defined and tested fail-open vs
fail-closed behavior on agent loss; agent healthcheck + dependency gating;
detections exported to Prometheus/Alertmanager.

**Permify.** Failing-loud, verified schema+tuple bootstrap (init job asserts
the schema digest); relationship-tuple pipeline with ownership rules; 2+
instances behind a service LB (checks are latency-sensitive sync calls);
TLS/mTLS on gRPC; startup hard-fail in services when authz is required but
unreachable; cache decisions with snap tokens.

**OpenSearch.** 3-node cluster, security plugin on (TLS, internal users, fine-
grained roles); snapshot repository to MinIO/S3 with scheduled snapshots; ISM
retention; implement the SPEC §3.8 telemetry-search ingest path (Kafka
connector or Timescale→OS pipeline); sized heap + dashboards SSO via Keycloak.

**Fluvio.** Decide: implement the edge bridge (gateway→Fluvio topic→
telemetry-ingest consumer with offset tracking and DLQ parity with the Kafka
path) or formally drop it. If kept: clustered SC/SPU topology, persistence,
and an integration test proving bus-gateway bytes reach `telemetry.raw`.

**Neo4j.** Land as profile-gated service with: auth + bolt TLS, named volume,
real healthcheck, `neo4j-admin` dumps added to the backup loop, constraints
migrations via goose-style runner, and one real consumer (route/network graph
queries in route-optimizer or fleet-api) before scoring above 2.

**AI/ML.** Deliver the PyTorch rebuild behind the same inference interface
(keep the rule fallback — it is good practice); MLflow (or equivalent)
registry with stage promotion + rollback; scheduled retraining on real
telemetry/maintenance outcomes from Timescale; bake/version the artifact in CI;
add drift + outcome-monitoring metrics to Prometheus; document the
route-optimizer (ortools) boundary as optimization-not-ML.

---

## Cross-cutting findings (top 5)

1. **Zero HA anywhere in the stateful plane, and no k8s story for it.** Every
   middleware is one compose container; `infra/k8s/base/` deploys only app
   services. Kafka rf=1, TigerBeetle single replica, Postgres single, Redis no
   Sentinel — any one of them is a full-platform outage and potential data
   loss.
2. **The middleware data plane is entirely unencrypted and unauthenticated.**
   Kafka PLAINTEXT (with anonymous ZK), PG `sslmode=disable`, Redis no AUTH,
   TB `--development`, OpenSearch security disabled, Permify plaintext gRPC.
   Fine for dev, disqualifying for prod as-is.
3. **Failure semantics are unusually well thought out — but inconsistent.**
   Permify and payments fail closed with durable statuses; toggle cache and
   JWKS fail open/degrade gracefully; but digital-twin hard-502s on Redis,
   OpenAppSec's agent-loss behavior is undocumented, and Permify's own
   bootstrap is best-effort (can silently leave every admin route 403/502).
4. **Backup coverage is partial.** The backup job covers both Postgres DBs +
   TigerBeetle file, with a documented restore drill — but Kafka topics,
   Redis AOF, OpenSearch indexes, and Keycloak's H2 file have no backup or
   restore path at all.
5. **Several advertised integrations are thinner than they look.**
   Fluvio: explicitly unimplemented. Mojaloop: simulator-only opt-in leg.
   `TIGERBEETLE_ADDR` wired into two services whose code never touches
   TigerBeetle. OpenAppSec: off by default, detect-only. OpenSearch
   "telemetry search" from SPEC §3.8 has no ingest pipeline. Score claims in
   SPEC §3.8 should be read with this audit as the ground truth.
