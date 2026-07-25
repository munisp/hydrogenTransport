# Middleware hardening & throughput engineering

This document is the engineering companion to SPEC §3.8. It covers (1) the HA
production posture delivered under `infra/prod/`, and (2) an honest
throughput analysis: what "millions of TPS" actually decomposes into for this
platform, which components scale horizontally, which don't, and the concrete
scale-out path per tier. No marketing numbers — every figure is either a
vendor/design number (cited as such) or an order-of-magnitude engineering
estimate with the assumptions stated.

## 1. HA production overlays (infra/prod/)

`docker-compose.prod.yml` is an overlay on the untouched dev
`infra/docker-compose.yml` (Compose v2.24+, `!reset` semantics):

```
cd infra
docker compose -f docker-compose.yml -f prod/docker-compose.prod.yml up -d
```

| Component | Dev | Prod overlay |
|---|---|---|
| Kafka | single broker + ZooKeeper | 3-node KRaft, rf=3, `min.insync.replicas=2`, unclean election off; ZK parked |
| Postgres | single TimescaleDB | primary + **streaming replica** (slot `h2_replica_slot`, WAL config via `prod/postgres/004_replication.sh`); single WRITER honestly noted — CloudNativePG on k8s |
| Redis | single, AOF on | master (AOF everysec) + replica + **3 Sentinels** (quorum 2) |
| OpenSearch | single-node, security off | 3-node cluster (`discovery.seed_hosts`, `initial_master_nodes`); security-off flagged **dev-only**, TLS checklist in `infra/prod/K8S_NOTES.md` |
| Keycloak | `start-dev`, dev-file | `start` on Postgres, **2 nodes**, `KC_HOSTNAME`, `KC_PROXY_HEADERS=forwarded`, jdbc-ping cluster cache, LB sticky cookie (`KC_NODE`) |
| APISIX | standalone yaml | **2 nodes on etcd** (`prod/apisix/config.yaml.prod`), routes synced from git by `apisix-etcd-sync`, HAProxy `gateway-lb` owns :9080 |
| Temporal | all-in-one auto-setup | + second **frontend** node (`SERVICES=frontend`) |
| Permify | single | + second replica on shared Postgres |
| TigerBeetle | single `--development` | `prod/tigerbeetle-cluster.sh format/start` — 6 replicas (0-5), `--replica-count=6` |
| OpenAppSec | learn/detect | prod profile: `LEARNING_MODE=false`, `mode: prevent`, `prod/openappsec/local_policy.yaml` |
| MinIO | single drive | distributed 4-drive documented (compose comment + MinIO Operator on k8s) |

Kubernetes paths (Strimzi, CloudNativePG, Redis operator, Keycloak operator,
Temporal Helm, TB StatefulSet on local NVMe, MinIO Operator):
`infra/prod/K8S_NOTES.md`.

## 2. Fluvio edge tier (services/rust/fluvio-edge)

Kafka remains the platform backbone (SPEC §3.3 topic contracts are unchanged).
Fluvio runs **on each bus gateway** as the edge ingestion tier: tiny runtime,
on-bus buffering, SmartModule pre-aggregation. The `fluvio-edge` agent
bridges the tiers: consumes `bus-telemetry` from the local SPU, batches
(`BATCH_MAX=500`/`BATCH_LINGER_MS=500`), produces to `telemetry.raw` with
lz4/acks=all, and falls back to a **CRC-framed store-and-forward spool**
(`SPOOL_DIR`, survives restarts, FIFO drain) when the uplink is down.
At-least-once end-to-end; ingest dedupes on CloudEvents `id`.
Dev harness: `infra/fluvio-edge/docker-compose.edge.yml` (profile `edge`).

## 3. Throughput engineering — "millions of TPS", honestly

First, decompose the demand. "Millions of TPS" is meaningless without saying
*which* TPS:

| Flow | Realistic city demand | Platform target (headroom) |
|---|---|---|
| Telemetry messages (bus gateways) | 50 buses × 1–10 Hz = 50–500 msg/s; a 10,000-vehicle future ≈ 100k msg/s | 1M msg/s ingest |
| Financial transfers (fares, trades) | ~2 TPS avg, ~20 TPS rush burst; even 100x city growth ≈ 2k TPS | 100k–1M ledger TPS |
| API requests (PWA/citizen) | 100s RPS citywide | 50k RPS |
| Search/indexing (open data) | 10s RPS + bulk telemetry indexing | 10k docs/s |

So: **the only flow that can legitimately approach "millions" is telemetry
ingest, and the only flow that needs >10k is the ledger.** Everything else is
solved with boring horizontal scaling.

### Per-component realistic ceilings and scale-out

| Tier | Single-node ceiling (order of magnitude) | Scales horizontally? | Scale-out path |
|---|---|---|---|
| **Go services** (fleet/infra/citizen/commerce/admin APIs) | 10–50k RPS/instance (stateless, JSON, small PG queries) | **Yes, linearly** | N replicas behind APISIX; HPA on CPU/latency. Bottleneck becomes PG connections → PgBouncer/CNPG Pooler |
| **Rust services** (telemetry-ingest, digital-twin, fluvio-edge) | ingest: 50–200k msg/s/instance (batch writes dominate) | **Yes** | Kafka consumer-group rebalancing across 12+ partitions; add partitions to add consumers (12 → 24 → 48) |
| **Kafka** | ~100k–1M msg/s per broker (lz4, batched; vendor-consistent figures) | **Yes, by partitions** | 3 brokers → 5/7; `telemetry.raw` 12 → 48+ partitions. rf=3/min ISR=2 unchanged. Watch NIC (10GbE ≈ 1M small msgs/s with lz4) |
| **Fluvio edge** | 10–50k msg/s per gateway SPU | **Yes, by fleet** | per-bus horizontal by construction; platform-side limit is Kafka, not Fluvio |
| **TigerBeetle** | **~1M transfers/s per 6-replica cluster** (design number, 8k transfers/batch) — **BUT ~5–20k/s with our current 1-transfer-per-call code** | No (single consensus group by design) | **Fix batching first** (`infra/tuning/tigerbeetle-batching.md`: 512+ transfers/batch, constant `TARGET_BATCH`; ~line 125 of `internal/ledger/ledger.go` posts 1/call today). Then: shard by ledger (fares vs energy vs carbon) if 1M/cluster is ever genuinely exceeded — it won't be for a fleet |
| **Postgres (OLTP + telemetry hypertable)** | 10–30k simple writes/s; ~50–100k reads/s with indexes; **single writer, always** | **Reads yes (replicas); writes NO** | (1) streaming replica(s) offload reads (prod overlay), (2) partition `fleet.telemetry` by time (TimescaleDB does) and by bus for very large fleets, (3) route domain schemas to separate PG clusters (fleet vs commerce), (4) **Citus** or Timescale multi-node for genuine multi-writer telemetry scale, (5) batch inserts (COPY/multi-row) in ingest paths |
| **Redis** | ~100k ops/s/instance | Cache reads yes (replicas); the dataset is small | Sentinel failover (overlay); split cache DB (`allkeys-lru`) from twin DB (`noeviction`) — `infra/tuning/redis.conf`; Redis Cluster only past ~50GB or ~500k ops/s |
| **OpenSearch** | 10–50k docs/s indexing per node | **Yes** | 3 → N data nodes, shard sizing per `infra/tuning/opensearch.yml`, ISM rollover |
| **Keycloak** | 500–2k logins/s/node (token signing CPU) | **Yes** | 2 nodes (overlay) → N; sticky sessions; realm tuning |
| **APISIX** | 20–50k RPS/node | **Yes** | 2 nodes behind LB (overlay) → N; etcd control plane already clustered-capable |
| **Temporal** | ~1–5k workflow starts/s; history service is the scaling unit | Partially (shards) | 2 frontends (overlay); scale matching/history; raise shard count BEFORE growth (can't reshard) |
| **Permify** | ~10–50k checks/s/node (cache-warm) | **Yes** | 2 replicas (overlay); PG is its store → same PG limits |
| **Mojaloop switch** | central-ledger on **MySQL 8**: thousands of committed transfers/s with tuning (`docs/MOJALOOP.md`) | Switch scales; MySQL is single-writer | Keep Mojaloop off the hot path: **TB = hot ledger, Mojaloop = settlement rails** (docs/MOJALOOP.md §architecture) |
| **MinIO/lakehouse/Spark** | GB/s per node | **Yes** | 4-drive distributed (overlay note) → pools; Spark workers N |
| **OpenAppSec** | adds ~1–5ms/req inline | **Yes** (per-gateway sidecar) | scales with APISIX nodes |

### The three real bottlenecks (and their fixes)

1. **Postgres single-writer.** This is the platform's hard ceiling for
   *transactional* writes (~10–30k/s tuned per `infra/tuning/postgresql.conf`).
   Path: batch writes in ingest → read replicas → per-domain DB split →
   Citus/Timescale multi-node. **Do not pretend Postgres scales writes
   horizontally.**
2. **TigerBeetle batching.** The 1M TPS design number is unreachable with
   1-transfer-per-request code (5–20k/s). Fix is a constant + a micro-batching
   accumulator (`infra/tuning/tigerbeetle-batching.md`). Until that's done,
   quoting "1M TPS" for payments is a lie.
3. **Uplink, not compute, at the edge.** 10k buses × 10 Hz × ~300B ≈ 30 MB/s
   sustained into the platform — fine for Kafka, expensive over cellular.
   Fluvio SmartModule downsample at the edge (services/rust/fluvio-edge
   README) is the cost lever, not more brokers.

### Validation posture

- Prod overlay: YAML-parsed; `!reset` requires Compose v2.24+.
- Tuning configs: reference-grade; load-test against YOUR hardware before
  adopting heap/buffer sizes verbatim.
- Numbers above are engineering estimates for capacity planning, not SLO
  commitments — SLOs live in `docs/SLO.md`.
