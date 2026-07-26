# Mojaloop at H2Fleet — rails, data stores, and the honest answer to "does it use MySQL?"

## Does Mojaloop use MySQL?

**Yes.** Mojaloop's **central-ledger** service (the switch's authoritative
record of transfers, positions and settlement) runs on **MySQL 8**
(`mysql:8.x` in the official Helm charts). The quoting service, account-lookup
service (ALS) and the central-settlement service also persist to that MySQL
cluster (separate schemas). Objection-handling detail: other switch
components use different stores — the ALS additionally uses **Redis** for the
party cache, and the bulk-api-adapter / some simulators use **MongoDB** — but
the ledger of record is MySQL 8.

**Can it use PostgreSQL instead?** **No — not upstream.** The central-ledger
codebase (mojaloop/central-ledger) is written against MySQL (mysql2 driver,
MySQL-flavored SQL, migrations, and stored-procedure-style position updates).
There is no supported PostgreSQL dialect toggle, no maintained fork, and the
Helm charts hard-wire MySQL. Running Mojaloop "on Postgres" would mean
maintaining a private fork of the switch — not viable for a city deployment.
If you want Mojaloop rails, you accept a MySQL 8 cluster in the platform.

Deployment consequence for H2Fleet: a production Mojaloop switch is deployed
from the official Helm charts (`mojaloop/mojaloop`) and brings its own MySQL 8
(percona/official chart), separate from our TimescaleDB/Postgres platform
data. Our compose dev stack uses the lightweight `mojaloop/simulator` instead
(no MySQL needed for dev).

## Making MySQL 8 keep up (tuning for TPS)

Central-ledger's hot path is: `transfer` row inserts, position-account
updates (per-DFSP, per-currency), and fulfil updates. MySQL 8 on NVMe with
the knobs below sustains **thousands of committed transfers/sec** — far above
city-fleet fare volume (~50k boardings/day peak ≈ <2 TPS average, ~20 TPS
rush-hour bursts; even 100x headroom is trivial).

Key `my.cnf` parameters (InnoDB):

| Parameter | Recommended | Why |
|---|---|---|
| `innodb_buffer_pool_size` | 50–70% of DB host RAM | All hot ledger rows + indexes must stay resident; disk reads on the position-update path kill TPS |
| `innodb_redo_log_capacity` | 4–8G | MySQL 8.0.30+ redo sizing; write-heavy ledger needs redo headroom to avoid checkpoint stalls |
| `innodb_flush_log_at_trx_commit` | `1` (prod) / `2` (cost-optimized) | `1` = full durability per commit; `2` doubles-to-triples TPS at ~1s loss window. Use group commit + replicas instead of relaxing this if durability matters |
| `sync_binlog` | `1` | Pair with the above; binlog is needed for replicas/PITR |
| `innodb_flush_method` | `O_DIRECT` | Skip double buffering |
| `binlog_group_commit_sync_delay` | ~1000 (µs) | Lets group commit batch multiple txns per fsync — the legitimate way to get TPS back with `=1` durability |
| `binlog_transaction_dependency_tracking` | `WRITESET` | Faster parallel replication appliers |
| `innodb_io_capacity` / `_max` | 4000 / 8000 (NVMe) | Match device; defaults (200) throttle purge/flush |
| `innodb_doublewrite` | `ON` on SSD unless filesystem guarantees atomic writes | corruption protection |
| `max_connections` | 500 + pooler | central-ledger opens many; cap and pool |

Plus the standard Mojaloop ops notes: MySQL **replicas** for reads/failover
(the Helm chart supports a replicated topology), `performance_schema` on for
the slow-query loop, and partition/rotate the largest history tables per
Mojaloop ops guides.

## Architecture recommendation: TigerBeetle hot ledger + Mojaloop settlement rails

Do **not** put Mojaloop's MySQL on the per-transaction hot path of a busy
fleet platform. Our recommendation (and what SPEC §3.4/§3.8 already implies):

- **TigerBeetle = operational hot ledger.** Every fare, wallet movement,
  energy trade and carbon credit posts double-entry transfers to TB at
  ~1M TPS capability with deterministic idempotency
  (`DeterministicTransferID`, see `internal/ledger`). TB is our system of
  record for *operational* balances (rider wallets 1xxx, operator revenue
  2xxx, energy trade 3xxx, carbon fund 4xxx).
- **Mojaloop = settlement rails / interoperability.** When value crosses an
  organizational boundary — topping up a rider wallet from an external bank
  or mobile-money DFSP, or settling net operator revenue to external parties
  — the payment runs over Mojaloop FSPIOP rails (parties → quotes →
  transfers, ILP condition/fulfilment). `commerce.fare_payments
  .mojaloop_transfer_id` links the two worlds.
- **Why this split:** TB gives us 3–4 orders of magnitude more headroom,
  simpler HA (6-replica consensus, no SQL), and double-entry correctness for
  high-frequency internal movements; Mojaloop gives us standard,
  audited inter-DFSP settlement (including its own central-ledger on MySQL 8)
  where interoperability matters — exactly the "hot path vs settlement path"
  split payment platforms use.

## The H2Fleet payer-side client

`services/go/commerce-api/internal/mojaloop/` implements the
sdk-scheme-adapter style flow:

1. `GET /parties/{type}/{id}` — payee discovery (operator party at the switch)
2. `POST /quotes` — quote; payee-supplied `ilpPacket`+`condition` forwarded
   verbatim when present, locally generated (deterministic fulfilment =
   `SHA256` preimage over `secret + transferId`) otherwise/simulator mode
3. `POST /transfers` — with the ILP packet + condition; the returned
   `fulfilment` is verified against the condition (`SHA256(fulfilment) ==
   condition`) — the cryptographic settlement proof of the Interledger flow

Properties:

- **Idempotent**: quoteId/transferId are deterministic UUIDs derived from the
  request's `Idempotency-Key`; a duplicate at the switch (HTTP 409 / FSPIOP
  3208) is a successful replay, never a second transfer.
- **Retry budget**: per-attempt timeout (8s) inside a total flow budget (20s);
  retries only on transport errors / 408 / 429 / 5xx with capped exponential
  backoff; 4xx business rejections fail immediately.
- **Error → status mapping** (`mojaloop.PaymentStatus`):
  `payee_not_found → mojaloop_payee_not_found`, `quote_rejected →
  mojaloop_quote_rejected`, `transfer_rejected → mojaloop_transfer_rejected`,
  `timeout → mojaloop_timeout`, `switch_unavailable → mojaloop_unavailable`.
  Persisted on `commerce.fare_payments.status`; events carry the reason.
- **Simulated fallback env-gated**: without `MOJALOOP_ENDPOINT` the Mojaloop
  leg fails closed (`mojaloop_unavailable`, classified via
  `mojaloop.PaymentStatus`). The legacy clearly-labelled `ml-simulated-*` ids
  (SPEC §4) are returned only behind the explicit dev opt-in
  `H2_SIMULATED_MOJALOOP=true`.

Env wiring (compose `MOJALOOP_ENDPOINT=http://mojaloop-simulator:8444`):

| Env | Default | Meaning |
|---|---|---|
| `MOJALOOP_ENDPOINT` | (unset) | scheme-adapter/simulator base URL; unset → Mojaloop leg fails closed (`mojaloop_unavailable`) |
| `H2_SIMULATED_MOJALOOP` | (unset) | `true` opts into clearly-labelled `ml-simulated-*` transfer ids when `MOJALOOP_ENDPOINT` is unset (DEV ONLY) |
| `MOJALOOP_DFSP_ID` | `h2fleet` | our DFSP id (`FSPIOP-Source`) |
| `MOJALOOP_PAYEE_PARTY_ID` | `h2fleet-operator` | operator party at the switch |
| `MOJALOOP_PAYEE_PARTY_TYPE` | `BUSINESS` | payee party id type |
| `MOJALOOP_ILP_SECRET` | (empty) | seeds deterministic fulfilment derivation — secret-manage in prod |
| `MOJALOOP_GENERATE_ILP` | `true` | force local ILP generation (simulator); set `false` behind a real sdk-scheme-adapter so the payee's quote condition is used |

Tests: `internal/mojaloop/client_test.go` runs the full flow against a mock
scheme-adapter (`httptest`): happy path with payee ILP, local-ILP fallback,
deterministic-id replay (409 → success), transient-5xx retry, budget-exhaust
give-up, payee-not-found mapping, validation errors, ILP round-trip.
