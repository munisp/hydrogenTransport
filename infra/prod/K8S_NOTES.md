# H2Fleet middleware on Kubernetes — HA deployment notes

The compose overlay (`docker-compose.prod.yml`) is the *shape reference* and
works for single-docker-host staging. Production belongs on Kubernetes; this
file maps every middleware component to its production-grade k8s mechanism.
Existing app manifests live in `infra/k8s/base` (this file does not modify
them); middleware operators are added per the sketches below.

## Kafka — Strimzi (kustomize)

Use the Strimzi operator; do not hand-roll StatefulSets.

```yaml
# infra/k8s/overlays/prod/kafka.yaml (sketch)
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaNodePool
metadata: { name: broker, labels: { strimzi.io/cluster: h2fleet } }
spec:
  replicas: 3
  roles: [broker, controller]        # dedicated controller pool at >100k partitions
  storage:
    type: jbod
    volumes: [{ id: 0, type: persistent-claim, size: 100Gi, class: local-ssd, deleteClaim: false }]
---
apiVersion: kafka.strimzi.io/v1beta2
kind: Kafka
metadata: { name: h2fleet, annotations: { strimzi.io/node-pools: enabled, strimzi.io/kraft: enabled } }
spec:
  kafka:
    version: 3.7.0
    config:
      default.replication.factor: 3
      min.insync.replicas: 2
      offsets.topic.replication.factor: 3
      transaction.state.log.replication.factor: 3
      transaction.state.log.min.isr: 2
      unclean.leader.election.enable: false
      compression.type: lz4
      auto.create.topics.enable: false
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false          # enable tls: true + strimzi certs once service mesh is absent
  entityOperator: { topicOperator: {}, userOperator: {} }
```

Topics as `KafkaTopic` CRs: `telemetry.raw` 12 partitions rf=3,
`telemetry.enriched` 12/3, `twin.updated` 12/3, payment topics 6/3 (see
infra/tuning/kafka-server.prod.properties for sizing rationale).
Add a `PodDisruptionBudget: maxUnavailable: 1` per node pool and rack-aware
spread (`rack` topology key) across AZs.

## Postgres — CloudNativePG

Compose keeps a single writer + streaming replica because docker cannot fail
over safely. On k8s use **CloudNativePG**:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: { name: h2fleet-pg }
spec:
  instances: 3                       # 1 primary + 2 hot standbys, sync replica optional
  imageName: timescale/timescaledb-ha:pg16.4-ts2.17.2-all
  postgresql:
    parameters: {}                   # apply infra/tuning/postgresql.conf here
  bootstrap:
    initdb:
      database: h2fleet
      owner: h2
      postInitSQLRefs:
        configMaps: [{ name: h2fleet-sql-init, key: 001_init.sql }]
  storage: { size: 50Gi, storageClass: local-ssd }
  monitoring: { enablePodMonitor: true }
```

CNPG gives automatic failover (~10-30s), WAL archiving to MinIO
(`barmanObjectStore`) for PITR, and connection pooling via its Pooler CR
(PgBouncer) — put the Pooler in front; all Go services then keep small pools.

## Redis — Sentinel (or Redis Operator)

k8s options, in order of operational maturity: (1) **OT-CONTAINER-KIT/redis-
operator** `RedisReplication` (1 master + 2 replicas) + `RedisSentinel` (3);
(2) Bitnami `redis` Helm chart with `sentinel.enabled=true`. Same posture as
compose: AOF everysec on the master, `allkeys-lru` for cache DBs,
`noeviction` for the twin hot-state DB (infra/tuning/redis.conf). Services
should use Sentinel-aware clients for automatic failover.

## OpenSearch — prod TLS checklist

Compose overlay runs `plugins.security.disabled=true` — **dev-only posture**.
Before real production:

1. Deploy via the OpenSearch operator/Helm with the security plugin ENABLED.
2. TLS on transport + REST: cert-manager-issued certs (or the operator's
   `generate-certs`), `plugins.security.ssl.http.enabled=true`.
3. Internal users via `securityconfig` secret; delete the demo `admin/admin`.
4. Map Keycloak OIDC → OpenSearch Dashboards SSO (`openid` authc domain).
5. Snapshot repo to MinIO (`s3.client.default.endpoint`) + ISM policies
   (hot 7d → warm 30d → snapshot/delete).
6. Resources: 3 dedicated masters at >5 data nodes; heap stays ≤50% RAM,
   `OPENSEARCH_JAVA_OPTS=-Xms2g -Xmx2g` per infra/tuning/opensearch.
7. `DISABLE_INSTALL_DEMO_CONFIG=true` everywhere (already set).

## Keycloak

Use the Keycloak operator: `Keycloak` CR with `instances: 2`,
`db` on the CNPG cluster (`jdbc:postgresql://h2fleet-pg-rw:5432/keycloak`),
`hostname.hostname=https://auth.h2fleet.example.com`,
`proxy.headers=forwarded`, cache `ispn` with `dns.DNS_PING` (default on k8s).
Sticky sessions: ingress annotation `nginx.ingress.kubernetes.io/affinity:
cookie`. Import the realm via `start --import-realm` once, then manage with
the realm CR.

## APISIX + etcd

Helm `apisix/apisix`: `gateway.replicaCount: 2`, `etcd.replicaCount: 3` (real
etcd cluster with TLS + auth — the compose `ALLOW_NONE_AUTHENTICATION` is
in-cluster-only), `apisix.admin` behind a ClusterIP Service. Routes: keep
`infra/apisix/apisix.yaml` as the git source of truth and sync with
`infra/prod/apisix/sync_routes.py` as an ArgoCD/Flux post-sync Job, or migrate
to `ApisixRoute` CRDs (`ingress-controller` variant). Config values from
`infra/prod/apisix/config.yaml.prod` go into the chart's `config` value.

## Temporal

Official Helm chart `temporalio/temporal`: scale `frontend.replicaCount: 2`
(second frontend = the compose overlay's `temporal-frontend-2`),
`matching/history/worker: 2` each, persistence on the CNPG cluster
(`visibility` + `default` stores), and put a headless Service in front of the
frontends — Temporal clients do client-side LB across A records. Frontends
are stateless; history shards are the stateful part (shard count in
dynamicconfig — raise before, not after, scale-up).

## Permify

2+ replicas of the distroless `serve` image behind one ClusterIP Service,
shared Postgres (CNPG `permify` db). Stateless — horizontal scaling is free;
watch Postgres connections (set `PERMIFY_DATABASE_MAX_OPEN_CONNECTIONS`).

## TigerBeetle

No operator. StatefulSet, 6 replicas (see `infra/prod/tigerbeetle-cluster.sh`
for the format/start logic to encode in an initContainer Job), one PVC per
replica on **local NVMe** (TB is latency-bound on fsync; network storage
kills the ~1M TPS ceiling). Anti-affinity across nodes+AZs is mandatory.
Clients get all 6 addresses via env (headless Service DNS list).

## MinIO

MinIO Operator, one `Tenant`, `pools: [{ servers: 4, volumesPerServer: 1 }]`
(= the distributed 4-drive minimum, EC:2) on local-ssd PVCs. The Iceberg REST
catalog and lakehouse buckets move unchanged (`s3://h2-lakehouse/`).

## OpenAppSec

Helm `openappsec/open-appsec`: `appsec.mode: prevent` after the learn phase,
agent token via sealed-secret, policy as in
`infra/prod/openappsec/local_policy.yaml` (translated to the CR format).

## Capacity baseline

Per-component ceilings and the scale-out path per tier:
`docs/MIDDLEWARE_HARDENING.md` §Throughput engineering.
