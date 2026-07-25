# TigerBeetle batching — the single biggest TPS lever in commerce-api

## The problem (measured in our code today)

`services/go/commerce-api/internal/ledger/ledger.go` — `tbLedger.Transfer`
(~line 125) posts **one transfer per `CreateTransfers` call**:

```go
results, err := l.client.CreateTransfers([]tb_types.Transfer{{ ... }})
```

Every HTTP payment therefore costs a full TigerBeetle round trip
(protocol encode → fsync quorum → response). A single TB replica does
~5k–20k such one-off requests/sec before fsync latency dominates — while the
same cluster sustains **~1M transfers/sec** when transfers arrive in large
batches (TB design numbers, 8k transfers per message).

## The constant to change

TigerBeetle's protocol maximum is **8189 events per batch message**
(`events_max` in the TB protocol; exposed in the Go client as the max slice
length for `CreateTransfers`). Target batch size for payment traffic:

```
TARGET_BATCH = 512   # transfers per CreateTransfers call (see math below)
```

512 transfers/batch at a ~2ms quorum write ≈ **~250k transfers/sec/sustained**
with one client pipeline — two orders of magnitude over the current
one-transfer-per-request path, at +≤10ms p99 settlement latency.

## How to get there without changing API semantics

1. **Batch what already batches naturally.** Energy-trade settlement and
   carbon-credit issuance run in Temporal batch jobs — collect the whole job's
   transfers and submit in chunks of 512.
2. **Add a micro-batching queue in front of `Transfer`** for the interactive
   fare path: a channel + 10ms window accumulator (same pattern as
   fluvio-edge's BATCH_LINGER_MS), deterministic transfer IDs unchanged —
   idempotency is per-transfer and unaffected by batching.
3. **Never cross-request batch without the deterministic ID.** The current
   `DeterministicTransferID(idemKey)` guarantee (retry-safe payments) is what
   makes batched retries safe too; keep it.

## Related knobs

- 6-replica cluster: `infra/prod/tigerbeetle-cluster.sh` (replicas 0-5).
- `grid`/`io` sizing: TB is fsync-latency bound — local NVMe, no network
  block storage (K8S_NOTES.md §TigerBeetle).
- Do NOT enable TB's `--development` flag in prod (it relaxes durability).
- Batch inserts of `commerce.fare_payments` rows the same way (PG
  `unnest` multi-row insert) when the Temporal settlement job posts many
  payments at once — PG, not TB, becomes the next bottleneck
  (docs/MIDDLEWARE_HARDENING.md §throughput).
