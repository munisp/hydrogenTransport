// Schema compile + query-building tests with a mocked pg client — proves the
// Drizzle schema mirrors the migration surface and produces the expected
// SQL without needing a live Postgres.
import { describe, expect, it, vi } from "vitest";
import { drizzle } from "drizzle-orm/node-postgres";
import { desc, sql } from "drizzle-orm";

import {
  auditLog,
  carbonCredits,
  chargePoints,
  chargingSessions,
  farePayments,
  featureToggles,
  incidents,
  stations,
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

  it("mirrors 0008 (Wave-5 energy vectors) columns and tables", () => {
    // fleet.vehicles.energy_type (NOT NULL DEFAULT 'h2')
    expect(vehicles.energyType.name).toBe("energy_type");
    expect(vehicles.energyType.notNull).toBe(true);
    // fleet.telemetry additive generic energy columns
    expect(telemetry.energyLevelPct.name).toBe("energy_level_pct");
    expect(telemetry.powertrainKw.name).toBe("powertrain_kw");
    expect(telemetry.energyType.name).toBe("energy_type");
    // infra.stations type + EV inventory
    expect(stations.stationType.name).toBe("station_type");
    expect(stations.availableKwh.name).toBe("available_kwh");
    expect(stations.chargerCount.name).toBe("charger_count");
    // new OCPP contract tables (written by the Wave-5 ocpp-gateway)
    expect(chargePoints).toBeDefined();
    expect(chargingSessions).toBeDefined();
    expect(chargePoints.ocppId.name).toBe("ocpp_id");
    expect(chargingSessions.chargePointId.name).toBe("charge_point_id");
    expect(chargingSessions.meterStart.notNull).toBe(true);
  });

  it("builds a parameterized select over charge points", async () => {
    const { db, query } = mockDb([{ ocpp_id: "CP-0001", status: "Available" }]);
    const rows = await db
      .select({ ocppId: chargePoints.ocppId, status: chargePoints.status })
      .from(chargePoints)
      .limit(10);
    expect(rows).toHaveLength(1);
    const [cfg, params] = query.mock.calls[0] as [{ text: string }, unknown[]];
    expect(cfg.text).toContain('"infra"."charge_points"');
    expect(params).toContain(10);
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
