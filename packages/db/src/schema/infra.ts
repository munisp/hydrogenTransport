// infra schema (migrations 0001_core.sql + 0003_supplemental.sql + 0005_missing_schemas.sql).
import { sql } from "drizzle-orm";
import { index, jsonb, numeric, pgSchema, text, timestamp, unique, uniqueIndex, uuid } from "drizzle-orm/pg-core";

import { geometryPoint } from "./columns";
import { maintenancePredictions, vehicles } from "./fleet";

export const infra = pgSchema("infra");

export const stations = infra.table("stations", {
  id: uuid("id").primaryKey().defaultRandom(),
  name: text("name").notNull(),
  capacityKg: numeric("capacity_kg"),
  availableKg: numeric("available_kg"),
  status: text("status").notNull().default("online"), // online|offline|maintenance
  geom: geometryPoint("geom"),
});

export const incidents = infra.table(
  "incidents",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    type: text("type").notNull(), // leak|collision|fuel-cell-fault|station-fault|other
    severity: text("severity").notNull().default("low"), // low|medium|high|critical
    busId: uuid("bus_id").references(() => vehicles.id),
    stationId: uuid("station_id").references(() => stations.id),
    status: text("status").notNull().default("open"), // open|acknowledged|resolved
    openedAt: timestamp("opened_at", { withTimezone: true }).notNull().defaultNow(),
    meta: jsonb("meta"),
    // 0005: deterministic human-readable number, sequence-backed DEFAULT
    // ('INC-000123'); assigned by the database, never by the app.
    incidentNo: text("incident_no").notNull(),
  },
  (t) => ({
    incidentNoUq: uniqueIndex("incidents_incident_no_uq").on(t.incidentNo),
  }),
);

export const complianceReports = infra.table("compliance_reports", {
  id: uuid("id").primaryKey().defaultRandom(),
  generatedAt: timestamp("generated_at", { withTimezone: true }).notNull().defaultNow(),
  report: jsonb("report").notNull(),
});

export const workOrders = infra.table("work_orders", {
  id: uuid("id").primaryKey().defaultRandom(),
  title: text("title").notNull(),
  description: text("description").notNull().default(""),
  assetRef: text("asset_ref").notNull().default(""),
  status: text("status").notNull().default("open"),
  openedAt: timestamp("opened_at", { withTimezone: true }).notNull().defaultNow(),
  closedAt: timestamp("closed_at", { withTimezone: true }),
  // 0005: predictive-maintenance → work-order linkage + assignment lifecycle
  busId: uuid("bus_id").references(() => vehicles.id),
  predictionId: uuid("prediction_id").references(() => maintenancePredictions.id),
  assignee: text("assignee"),
  startedAt: timestamp("started_at", { withTimezone: true }),
});

export const dispatchJobs = infra.table(
  "dispatch_jobs",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    driverSub: text("driver_sub").notNull(),
    vehicleId: uuid("vehicle_id"),
    route: text("route").notNull().default(""),
    startsAt: timestamp("starts_at", { withTimezone: true }),
    endsAt: timestamp("ends_at", { withTimezone: true }), // 0005: shift end (shift_end event field)
    status: text("status").notNull().default("assigned"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    acceptedAt: timestamp("accepted_at", { withTimezone: true }),
  },
  (t) => ({
    driverIdx: index("dispatch_jobs_driver_idx").on(t.driverSub),
    // 0005: double-booking guard — one active job per driver / per vehicle.
    activeDriverUq: uniqueIndex("dispatch_jobs_active_driver_uq")
      .on(t.driverSub)
      .where(sql`${t.status} IN ('assigned','accepted','in_progress')`),
    activeVehicleUq: uniqueIndex("dispatch_jobs_active_vehicle_uq")
      .on(t.vehicleId)
      .where(sql`${t.vehicleId} IS NOT NULL AND ${t.status} IN ('assigned','accepted','in_progress')`),
  }),
);

// 0005: dispatch drivers reference (Keycloak subject keyed).
export const drivers = infra.table("drivers", {
  sub: text("sub").primaryKey(), // Keycloak subject
  name: text("name").notNull().default(""),
  licenseNo: text("license_no").notNull().default(""),
  status: text("status").notNull().default("active"), // active|off-duty|suspended
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});

// 0005: station queue management (SPEC §1 "queue mgmt"). One active
// (waiting|serving) entry per bus per station, enforced by a partial
// unique index.
export const stationQueue = infra.table(
  "station_queue",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    stationId: uuid("station_id").notNull().references(() => stations.id),
    busId: uuid("bus_id").notNull().references(() => vehicles.id),
    joinedAt: timestamp("joined_at", { withTimezone: true }).notNull().defaultNow(),
    status: text("status").notNull().default("waiting"), // waiting|serving|completed|left
  },
  (t) => ({
    activeUq: uniqueIndex("station_queue_active_uq")
      .on(t.stationId, t.busId)
      .where(sql`${t.status} IN ('waiting','serving')`),
    stationIdx: index("station_queue_station_idx").on(t.stationId, t.status, t.joinedAt),
  }),
);

export const depotBays = infra.table(
  "depot_bays",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    depot: text("depot").notNull(),
    label: text("label").notNull(),
    kind: text("kind").notNull().default("parking"),
    occupiedBy: uuid("occupied_by"),
    status: text("status").notNull().default("free"),
  },
  (t) => ({
    depotLabelUq: unique("depot_bays_depot_label_uq").on(t.depot, t.label),
  }),
);
