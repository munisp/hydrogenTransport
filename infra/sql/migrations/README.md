# H2Fleet database migrations (goose)

Canonical, versioned schema source for the platform Postgres
(`timescale/timescaledb-ha`, ships PostGIS + TimescaleDB). The legacy
`infra/sql/001_init.sql` / `002_seed.sql` files are kept as compat shims for
`docker-entrypoint-initdb.d` on fresh boots — all new DDL goes here.

## Migrations

| file | contents |
|---|---|
| `0001_core.sql` | Extensions (postgis, timescaledb, pgcrypto), per-domain schemas, all SPEC §3.4 core tables, `fleet.twin_snapshots`, `public.feature_toggles` (DEFAULT `enabled = true`, reconciled with seed behaviour), hypertable + `updated_at` trigger. |
| `0002_seed.sql` | Seed data: 20 feature toggles ON, 50 buses, 3 stations, sample incidents/telemetry/carbon/fare/trades. |
| `0003_supplemental.sql` | Service-owned supplemental DDL previously applied at runtime via `EnsureSchema`: commerce (`loyalty_accounts`, `marketplace_offers`, `ad_campaigns`, `rider_accounts`, `fare_payments.idempotency_key`/`tb_transfer_id` + unique index) and infra (`compliance_reports`, `work_orders`, `dispatch_jobs` + `accepted_at`, `depot_bays` + seed bays). |
| `0004_telemetry_dedup.sql` | `UNIQUE(bus_id, ts)` on `fleet.telemetry` (includes the partition column — Timescale-safe; pairs with `ON CONFLICT DO NOTHING` in telemetry-ingest), 90-day retention policy, compression after 7 days (segment by `bus_id`). |

Every file has a `-- +goose Down` section for rollback.

## Running the migrator

Install goose (`go install github.com/pressly/goose/v3/cmd/goose@latest`, or
use the container snippet below). Then:

```bash
export DSN="postgres://postgres:postgres@localhost:5432/h2fleet?sslmode=disable"

goose -dir infra/sql/migrations postgres "$DSN" up        # apply all pending
goose -dir infra/sql/migrations postgres "$DSN" status    # show applied/pending
```

One-shot container variant (no local goose install):

```bash
docker run --rm --network host \
  -v "$PWD/infra/sql/migrations:/migrations" \
  ghcr.io/kukymbr/goose-docker:latest \
  -dir /migrations postgres "$DSN" up
```

### Rollback procedure

```bash
# roll back the single most recent migration (e.g. undo 0004):
goose -dir infra/sql/migrations postgres "$DSN" down

# roll back repeatedly or to a specific version:
goose -dir infra/sql/migrations postgres "$DSN" down-to 0002

# full teardown (all the way down, drops schemas/extensions):
goose -dir infra/sql/migrations postgres "$DSN" reset
```

Always take a backup (`pg_dump`) before rolling back production data;
`down` on `0001`/`0002` drops tables and seed rows.

### Notes for operators

- Migrations run inside a transaction (goose default); `create_hypertable`,
  retention/compression policies and non-concurrent indexes are all
  transaction-safe.
- `0003` coexists with the legacy `EnsureSchema` runtime DDL in the Go
  services: both are fully idempotent (`IF NOT EXISTS`), so mixed-version
  rollouts are safe.
- On an existing database that was created by `001_init.sql`/`002_seed.sql`
  before goose was introduced, baseline it with
  `goose -dir infra/sql/migrations postgres "$DSN" up-to 0002` (all statements
  are idempotent) and then run `up` normally; or stamp with
  `goose ... up-to` after verifying objects exist.
