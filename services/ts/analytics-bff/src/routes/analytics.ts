// Read-only analytics endpoints backed by Drizzle queries (sql<>
// aggregations) over the @h2fleet/db schema, with zod-validated outputs.
// Read-only by contract: this service's DATABASE_URL should use a role with
// SELECT-only grants (deployment note in README).
import { Hono } from "hono";
import { sql, type SQL } from "drizzle-orm";
import type { NodePgDatabase } from "drizzle-orm/node-postgres";
import { z } from "zod";

import { carbonCredits, farePayments, stations, telemetry, trades, vehicles } from "@h2fleet/db";
import type { AuthEnv } from "../auth";

// ------------------------------------------------------------- zod output --
const fleetSummarySchema = z.object({
  buses: z.object({
    total: z.number().int().nonnegative(),
    by_status: z.record(z.string(), z.number().int().nonnegative()),
  }),
  avg_h2_level_pct: z.number().nullable(),
  stations: z.object({
    total: z.number().int().nonnegative(),
    available_kg: z.number().nonnegative(),
  }),
  generated_at: z.string(),
});

const revenueDailySchema = z.object({
  days: z.number().int().min(1).max(365),
  rows: z.array(
    z.object({
      day: z.string(),
      payments: z.number().int().nonnegative(),
      settled_minor: z.number().nonnegative(),
      trades: z.number().int().nonnegative(),
      trades_minor: z.number().nonnegative(),
    }),
  ),
});

const carbonDailySchema = z.object({
  days: z.number().int().min(1).max(365),
  rows: z.array(
    z.object({
      day: z.string(),
      kg_co2_avoided: z.number().nonnegative(),
      credits: z.number().nonnegative(),
    }),
  ),
});

const daysParam = z.coerce.number().int().min(1).max(365).catch(30);

// Any drizzle database handle (pg in prod, mock in tests).
export type Db = Pick<NodePgDatabase, "execute">;

async function rows<T = Record<string, unknown>>(db: Db, q: SQL): Promise<T[]> {
  const res = await db.execute(q);
  return res.rows as T[];
}

export function analyticsRoutes(db: Db) {
  const r = new Hono<AuthEnv>();

  // GET /v1/analytics/fleet-summary — fleet posture: vehicle counts by
  // status, mean H2 level of the latest telemetry reading per bus, and
  // station availability.
  r.get("/fleet-summary", async (c) => {
    const byStatus = await rows<{ status: string; count: number }>(
      db,
      sql`select ${vehicles.status} as status, count(*)::int as count
          from ${vehicles} group by ${vehicles.status}`,
    );
    const h2 = await rows<{ avg_h2: number | null }>(
      db,
      sql`select avg(latest.h2_level_pct)::float as avg_h2 from (
            select distinct on (${telemetry.busId}) ${telemetry.busId} as bus_id,
                   ${telemetry.h2LevelPct} as h2_level_pct
            from ${telemetry} order by ${telemetry.busId}, ${telemetry.ts} desc
          ) latest`,
    );
    const st = await rows<{ total: number; available_kg: number }>(
      db,
      sql`select count(*)::int as total,
                 coalesce(sum(${stations.availableKg}), 0)::float as available_kg
          from ${stations}`,
    );

    const by_status: Record<string, number> = {};
    let total = 0;
    for (const row of byStatus) {
      by_status[row.status] = Number(row.count);
      total += Number(row.count);
    }
    const payload = fleetSummarySchema.parse({
      buses: { total, by_status },
      avg_h2_level_pct: h2[0]?.avg_h2 ?? null,
      stations: {
        total: Number(st[0]?.total ?? 0),
        available_kg: Number(st[0]?.available_kg ?? 0),
      },
      generated_at: new Date().toISOString(),
    });
    return c.json(payload);
  });

  // GET /v1/analytics/revenue-daily?days=30 — settled fare revenue and
  // executed energy-trade value per day (minor units).
  r.get("/revenue-daily", async (c) => {
    const days = daysParam.parse(c.req.query("days") ?? "30");
    const fares = await rows<{ day: string; payments: number; settled_minor: number }>(
      db,
      sql`select date_trunc('day', ${farePayments.createdAt})::date::text as day,
                 count(*)::int as payments,
                 coalesce(sum(${farePayments.amountMinor}) filter (where ${farePayments.status} = 'settled'), 0)::float as settled_minor
          from ${farePayments}
          where ${farePayments.createdAt} >= now() - make_interval(days => ${days})
          group by 1 order by 1`,
    );
    const tradeRows = await rows<{ day: string; trades: number; trades_minor: number }>(
      db,
      sql`select date_trunc('day', ${trades.createdAt})::date::text as day,
                 count(*)::int as trades,
                 coalesce(sum(${trades.priceMinor}) filter (where ${trades.status} in ('executed','settled')), 0)::float as trades_minor
          from ${trades}
          where ${trades.createdAt} >= now() - make_interval(days => ${days})
          group by 1 order by 1`,
    );
    const byDay = new Map<string, { trades: number; trades_minor: number }>();
    for (const t of tradeRows) {
      byDay.set(t.day, { trades: Number(t.trades), trades_minor: Number(t.trades_minor) });
    }
    const payload = revenueDailySchema.parse({
      days,
      rows: fares.map((f) => ({
        day: f.day,
        payments: Number(f.payments),
        settled_minor: Number(f.settled_minor),
        trades: byDay.get(f.day)?.trades ?? 0,
        trades_minor: byDay.get(f.day)?.trades_minor ?? 0,
      })),
    });
    return c.json(payload);
  });

  // GET /v1/analytics/carbon-daily?days=30 — avoided CO2 and issued credits
  // per day (source of the gov dashboard trend).
  r.get("/carbon-daily", async (c) => {
    const days = daysParam.parse(c.req.query("days") ?? "30");
    const data = await rows<{ day: string; kg: number; credits: number }>(
      db,
      sql`select date_trunc('day', ${carbonCredits.issuedAt})::date::text as day,
                 coalesce(sum(${carbonCredits.kgCo2Avoided}), 0)::float as kg,
                 coalesce(sum(${carbonCredits.credits}), 0)::float as credits
          from ${carbonCredits}
          where ${carbonCredits.issuedAt} >= now() - make_interval(days => ${days})
          group by 1 order by 1`,
    );
    const payload = carbonDailySchema.parse({
      days,
      rows: data.map((d) => ({
        day: d.day,
        kg_co2_avoided: Number(d.kg),
        credits: Number(d.credits),
      })),
    });
    return c.json(payload);
  });

  return r;
}
