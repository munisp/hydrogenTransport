# @h2fleet/db — Drizzle ORM schema (decision record)

## Decision

**Drizzle ORM is the recommended data-access layer for TypeScript edge
services in H2Fleet** (first consumer: `services/ts/analytics-bff`).
Go services keep `pgx`, Rust keeps `sqlx`, Python keeps `psycopg` — native
drivers stay; this is *not* a rewrite of any existing service.

## Why Drizzle (and why now)

* **One schema of truth, two type-safe lenses.** The goose migrations in
  `infra/sql/migrations` remain the canonical DDL. This package mirrors
  *every* table (`public.feature_toggles`; `fleet.*`, `infra.*`,
  `citizen.*`, `commerce.*`, `platform.*`) as typed Drizzle tables, so TS
  services get compile-time checked column access instead of hand-written
  SQL strings that drift silently.
* **SQL-first, not magic.** Drizzle composes real SQL (incl. `sql<>`
  template aggregations, `date_trunc`, window fns) — no query-builder
  dialect to learn, no hidden N+1, and the generated SQL is reviewable in
  tests (see `test/schema.test.ts`, which runs against a mocked pg client).
* **Lightweight for edge/TS services.** Zero runtime deps beyond `pg`;
  schema is plain TypeScript; bundler-friendly (esbuild/tsx/vite).
* **Migration story stays unified.** DDL is still authored in goose
  migrations. `drizzle.config.ts` exists so `drizzle-kit check` statically
  validates the TS schema, and `drizzle-kit introspect` can diff the TS
  schema against a live database during reviews. We deliberately do **not**
  generate migrations from Drizzle — that would fork the source of truth.

## Layout

```
src/schema/public.ts     public.feature_toggles
src/schema/fleet.ts      fleet.vehicles / telemetry (hypertable) / maintenance_predictions / twin_snapshots
src/schema/infra.ts      infra.stations / incidents / compliance_reports / work_orders / dispatch_jobs / depot_bays
src/schema/citizen.ts    citizen.drt_requests / carbon_credits
src/schema/commerce.ts   commerce.fare_payments (+idempotency) / trades / loyalty_accounts / marketplace_offers / ad_campaigns / rider_accounts
src/schema/platform.ts   platform.onboarding_requests / audit_log (hash-chained audit trail)
src/schema/columns.ts    PostGIS geometry(Point,4326) custom type
```

TimescaleDB hypertables, retention/compression policies, triggers and
PostGIS indexes intentionally live **only** in the goose migrations; the
Drizzle models describe the plain relational surface.

## Usage

```ts
import { drizzle } from "drizzle-orm/node-postgres";
import pg from "pg";
import { vehicles } from "@h2fleet/db";

const db = drizzle(new pg.Pool({ connectionString: process.env.DATABASE_URL }));
const fleet = await db.select().from(vehicles).where(eq(vehicles.status, "active"));
```

## Checks

* `npm run typecheck` — strict `tsc --noEmit`
* `npm test` — vitest, mocked pg (no live database)
* `npm run drizzle:check` — drizzle-kit static schema check
