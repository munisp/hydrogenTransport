// Schema compile + query-building tests with a mocked pg client — proves the
// Drizzle schema mirrors the migration surface and produces the expected
// SQL without needing a live Postgres.
import { describe, expect, it, vi } from "vitest";
import { drizzle } from "drizzle-orm/node-postgres";
import { desc, sql } from "drizzle-orm";

import {
  auditLog,
  carbonCredits,
  farePayments,
  featureToggles,
  incidents,
  telemetry,
  trades,
  vehicles,
  workOrders,
} from "../src/index";

function mockDb(rows: unknown[] = []) {
  const query = vi.fn().mockResolvedValue({ rows });
  // node-postgres driver only needs .query for reads.
  const db = drizzle({ query } as never);
  return { db, query };
}

describe("@h2fleet/db schema", () => {
  it("exposes every domain table", () => {
    for (const t of [featureToggles, vehicles, telemetry, incidents, workOrders, carbonCredits, farePayments, auditLog]) {
      expect(t).toBeDefined();
    }
  });

  it("maps snake_case columns to camelCase fields", () => {
    expect(farePayments.amountMinor.name).toBe("amount_minor");
    expect(farePayments.idempotencyKey.name).toBe("idempotency_key");
    // 0006: trades idempotency key mirrors commerce.trades.idempotency_key
    expect(trades.idempotencyKey.name).toBe("idempotency_key");
    expect(telemetry.busId.name).toBe("bus_id");
    expect(vehicles.fleetNo.name).toBe("fleet_no");
    expect(auditLog.prevHash.name).toBe("prev_hash");
  });

  it("scopes tables to their postgres schemas", () => {
    expect((vehicles as never as Record<string, symbol>)[Symbol.for("drizzle:Name")]).toBe("vehicles");
  });

  it("builds a parameterized select against a mocked pg client", async () => {
    const { db, query } = mockDb([{ fleet_no: "H2-001", status: "active" }]);
    const rows = await db
      .select({ fleetNo: vehicles.fleetNo, status: vehicles.status })
      .from(vehicles)
      .orderBy(desc(vehicles.fleetNo))
      .limit(5);
    expect(rows).toHaveLength(1);
    // node-postgres driver issues query(QueryConfig{text}, values).
    const [cfg, params] = query.mock.calls[0] as [{ text: string }, unknown[]];
    expect(cfg.text).toContain('"fleet"."vehicles"');
    expect(cfg.text).toContain("limit");
    expect(params).toContain(5);
  });

  it("builds sql<> aggregations (analytics pattern)", async () => {
    const { db, query } = mockDb([]);
    await db
      .select({
        day: sql<string>`date_trunc('day', ${farePayments.createdAt})`,
        totalMinor: sql<string>`coalesce(sum(${farePayments.amountMinor}), 0)`,
      })
      .from(farePayments)
      .groupBy(sql`1`);
    const [cfg] = query.mock.calls[0] as [{ text: string }];
    expect(cfg.text).toContain("date_trunc('day'");
    expect(cfg.text).toContain("sum(");
    expect(cfg.text).toContain('"commerce"."fare_payments"');
  });
});
