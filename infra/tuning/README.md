# infra/tuning — middleware tuning reference configs

Reference configurations for the production posture of each middleware
component. These are *sources of truth for parameters*; deployment wiring:

| File | Component | Applied via |
|---|---|---|
| `postgresql.conf` | Postgres 16 (TimescaleDB) | mount + `config_file=`, `ALTER SYSTEM`, or CloudNativePG `postgresql.parameters` |
| `kafka-server.prod.properties` | Kafka KRaft brokers | bitnami `KAFKA_CFG_*` env (see infra/prod overlay) or Strimzi `spec.kafka.config` |
| `redis.conf` | Redis master | `redis-server /path/redis.conf`; prod overlay uses `infra/prod/redis/master.conf` |
| `opensearch.yml` | OpenSearch nodes | ConfigMap / mount; env-key form in compose overlay |
| `opensearch-jvm.options` | OpenSearch JVM | append to config/jvm.options |
| `apisix prod` | APISIX gateway | `infra/prod/apisix/config.yaml.prod` (worker_processes, keepalive live there) |
| `nginx-pwa.conf` | PWA static nginx | apps/pwa container (brotli via pre-compressed assets) |
| `tigerbeetle-batching.md` | TigerBeetle | code-level guidance for services/go/commerce-api/internal/ledger |

Sizing assumptions: 8 vCPU / 32 GB RAM / SSD-class storage per middleware
node unless a file says otherwise. Re-derive `shared_buffers` (25% RAM),
`effective_cache_size` (70% RAM) and heap (`-Xmx` = 50% RAM, ≤30GB) when node
sizes change.

Cross-reference: capacity ceilings and the horizontal scale-out path per tier
are in `docs/MIDDLEWARE_HARDENING.md` §Throughput engineering.
