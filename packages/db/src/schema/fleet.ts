// fleet schema (migration 0001_core.sql + 0004_telemetry_dedup.sql).
// fleet.telemetry is a TimescaleDB hypertable with UNIQUE(bus_id, ts),
// 90-day retention and 7-day compression — modeled here as a plain table;
// the hypertable/policy DDL stays in goose migrations.
import { boolean, index, integer, jsonb, numeric, pgSchema, primaryKey, text, timestamp, unique, uniqueIndex, uuid } from "drizzle-orm/pg-core";

import { geometryLineString, geometryPoint, geometryPolygon } from "./columns";

export const fleet = pgSchema("fleet");

export const vehicles = fleet.table("vehicles", {
  id: uuid("id").primaryKey().defaultRandom(),
  fleetNo: text("fleet_no").notNull().unique(),
  vin: text("vin"),
  model: text("model"),
  h2CapacityKg: numeric("h2_capacity_kg"),
  status: text("status").notNull().default("active"), // active|maintenance|depot|retired
  geom: geometryPoint("geom"),
});

export const telemetry = fleet.table(
  "telemetry",
  {
    busId: uuid("bus_id").notNull().references(() => vehicles.id),
    ts: timestamp("ts", { withTimezone: true }).notNull(),
    speedKph: numeric("speed_kph"),
    h2LevelPct: numeric("h2_level_pct"),
    fuelCellKw: numeric("fuel_cell_kw"),
    batterySocPct: numeric("battery_soc_pct"),
    odometerKm: numeric("odometer_km"),
    geom: geometryPoint("geom"),
  },
  (t) => ({
    busTsIdx: index("telemetry_bus_ts_idx").on(t.busId, t.ts),
    busTsUq: uniqueIndex("telemetry_bus_ts_uq").on(t.busId, t.ts),
  }),
);

export const maintenancePredictions = fleet.table(
  "maintenance_predictions",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    busId: uuid("bus_id").notNull().references(() => vehicles.id),
    component: text("component").notNull(), // fuel-cell|battery|h2-system|powertrain
    riskScore: numeric("risk_score").notNull(), // 0..1
    predictedFailureAt: timestamp("predicted_failure_at", { withTimezone: true }),
    modelVersion: text("model_version"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    busIdx: index("maintenance_pred_bus_idx").on(t.busId, t.createdAt),
  }),
);

export const twinSnapshots = fleet.table(
  "twin_snapshots",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    busId: uuid("bus_id").notNull().references(() => vehicles.id),
    state: jsonb("state").notNull(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    busIdx: index("twin_snapshots_bus_idx").on(t.busId, t.updatedAt),
  }),
);

// --- GTFS-like route network + geofences (migration 0005_missing_schemas.sql).
// Replace the previously hardcoded stops/routes (citizen-api) and random
// stop generation (route-optimizer); depot_zones / route_corridors are the
// reference tables for the lakehouse geo_enrich job.
export const stops = fleet.table("stops", {
  id: uuid("id").primaryKey().defaultRandom(),
  code: text("code").notNull().unique(), // e.g. 'S001'
  name: text("name").notNull(),
  geom: geometryPoint("geom").notNull(),
});

export const routes = fleet.table("routes", {
  id: uuid("id").primaryKey().defaultRandom(),
  code: text("code").notNull().unique(), // e.g. 'R10'
  shortName: text("short_name").notNull().default(""),
  longName: text("long_name").notNull().default(""),
  headwayMin: integer("headway_min"),
  active: boolean("active").notNull().default(true),
});

export const routeStops = fleet.table(
  "route_stops",
  {
    routeId: uuid("route_id").notNull().references(() => routes.id),
    stopId: uuid("stop_id").notNull().references(() => stops.id),
    seq: integer("seq").notNull(),
  },
  (t) => ({
    pk: primaryKey({ columns: [t.routeId, t.seq] }),
    routeStopUq: unique("route_stops_route_id_stop_id_key").on(t.routeId, t.stopId),
  }),
);

export const depotZones = fleet.table("depot_zones", {
  id: text("id").primaryKey(), // e.g. 'DEPOT-CENTRAL'
  name: text("name").notNull(),
  geom: geometryPolygon("geom").notNull(),
});

export const routeCorridors = fleet.table("route_corridors", {
  id: text("id").primaryKey(), // e.g. 'R12'
  name: text("name").notNull(),
  geom: geometryLineString("geom").notNull(),
});
