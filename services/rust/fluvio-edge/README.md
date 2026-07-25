# fluvio-edge — H2Fleet edge telemetry agent

Runs **on each bus gateway** next to the on-bus Fluvio cluster. It consumes the
local Fluvio topic `bus-telemetry`, batches records, and forwards them to the
platform Kafka topic `telemetry.raw` (SPEC §3.3). When the uplink is down,
records go to a durable on-disk **store-and-forward spool** and are replayed
(FIFO) once connectivity returns — the spool survives agent restarts and
gateway reboots.

## Architecture (honest)

- **Kafka stays the platform event backbone.** All platform consumers
  (telemetry-ingest, digital-twin, …) read Kafka topics per SPEC §3.3.
- **Fluvio is the edge ingestion tier**: a lightweight streaming runtime that
  fits on gateway-class hardware (no JVM, small footprint) and gives
  on-bus buffering + SmartModule pre-processing where connectivity is
  intermittent. This agent is the bridge between the two tiers.
- Delivery semantics: **at-least-once**. A produce that fails after a partial
  broker write can duplicate a record; telemetry-ingest dedupes on the
  CloudEvents `id` field.

## Configuration (env)

| Var | Default | Meaning |
|---|---|---|
| `FLUVIO_ENDPOINT` | `localhost:9003` | Fluvio SC endpoint on the gateway |
| `FLUVIO_TOPIC` | `bus-telemetry` | Edge topic produced by the on-bus publisher |
| `FLUVIO_PARTITION` | `0` | Edge partition |
| `KAFKA_BROKERS` | `kafka:9092` | Platform bootstrap servers |
| `KAFKA_TOPIC` | `telemetry.raw` | Platform topic (SPEC §3.3) |
| `SPOOL_DIR` | `/var/lib/fluvio-edge` | Spool + offset state (persist!) |
| `BATCH_MAX` | `500` | Records per Kafka batch |
| `BATCH_LINGER_MS` | `500` | Max wait before flushing a partial batch |
| `SPOOL_MAX_BYTES` | `67108864` (64 MiB) | Soft spool cap; consumer backpressures beyond it |
| `PORT` | `8093` | `/healthz` port |

`/healthz` always answers 200 while the agent is alive and reports the real
state: `fluvio_connected`, `kafka_ok`, `spool_depth`, `forwarded_total`,
`spooled_total`. Degraded uplink is a *normal* edge operating mode; alert on
`spool_depth` growth, not on the agent being "unhealthy".

## Spool format

`spool.data`: frames of `[u32 crc32][u32 len][payload]`, payload =
`[u8 has_key][u32 klen][key][u32 vlen][value]`. Append-only with fsync per
batch; compacted (atomic rewrite+rename) once the acked prefix dominates.
A crash-truncated or CRC-mismatched tail is dropped at open.
`offset.state` holds the next Fluvio offset to consume (atomic rename).

## Fluvio SmartModule note

Per-bus traffic is ~1 record/sec of a few hundred bytes — tiny. At fleet scale
(50 buses today, thousands later) the edge tier should *pre-aggregate before
the uplink*: a SmartModule on the `bus-telemetry` topic can downsample
high-frequency channels (e.g. 10 Hz fuel-cell current → 1 Hz mean/max) or
drop heartbeats when nothing changed, cutting uplink bytes 5–10×.
Sketch (SmartModule `filter-map`, compiled to WASM and attached at consume
time — this agent can attach it via `ConsumerConfigExt::smartmodule`):

```rust
// smartmodule: keep every record whose h2_level_pct moved >= 0.5,
// pass the rest only every 30th tick (heartbeat downsample).
#[smartmodule(filter_map)]
pub fn downsample(record: &SmartModuleRecord) -> Result<Option<Record>> { /* … */ }
```

Not bundled here: SmartModules are deployed per-edge-cluster
(`fluvio smartmodule deploy`) and versioned with the gateway firmware, not
with this forwarder.

## Local dev

See `infra/fluvio-edge/docker-compose.edge.yml` (profile `edge`): spins a
Fluvio SC+SPU plus this agent plus a sample on-bus publisher, against the main
stack's Kafka (`h2-kafka:9092` on `h2net`).

## Test

```sh
cargo test          # spool + offset roundtrip unit tests
cargo check         # type check
```

## Dependency resolution

fluvio 0.21.8's crates.io semver ranges no longer resolve to a consistent set
(later patch releases of sibling crates moved on underneath it). The agent
therefore pins the fluvio stack with exact `=` requirements in `Cargo.toml`,
and `Cargo.lock` is committed:

| Crate | Pin | Why |
|---|---|---|
| `fluvio` | `=0.21.8` | The client we build against |
| `fluvio-sc-schema` | `=0.23.0` | Line fluvio 0.21.8 was published against |
| `fluvio-socket` | `=0.14.8` | 0.14.9+ pulls fluvio-protocol 0.11 (fluvio 0.21.8 needs 0.10) |
| `fluvio-spu-schema` | `=0.14.7` | Matches fluvio-socket 0.14.8 / fluvio-smartmodule 0.7.3 |
| `fluvio-smartmodule` | `=0.7.3` | Required by fluvio-spu-schema 0.14.7 |
| `fluvio-stream-model` | `=0.11.2` | Last release on `async-rwlock` (0.11.3+ switched to `async-lock`, which the fluvio lib's `sync` module doesn't expect) |
| `async-std` | `=1.12.0` | Last release on `async-lock` 2.x; 1.13+ uses `async-lock` 3.x, whose `MutexGuard` no longer matches the `async-lock` 2.x `Mutex` fluvio's producer accumulator pairs with async-std's `Condvar` |

The upstream git tag's `Cargo.lock` (the ideal provenance for this pin set) is
unreachable from our build network, so the set was derived from crates.io
dependency metadata and verified by `cargo check --locked` + `cargo test
--locked`. Do not bump any single pin in isolation — the set is only
self-consistent as a whole. The client 0.21.x line pairs with the Fluvio
platform 0.11.x images used in `infra/fluvio-edge/docker-compose.edge.yml`.
