// infra schema (migrations 0001_core.sql + 0003_supplemental.sql + 0005_missing_schemas.sql +
// 0007_wave4_business_rules.sql + 0008_energy_vectors.sql).
import { sql } from "drizzle-orm";
import { index, integer, jsonb, numeric, pgSchema, text, timestamp, unique, uniqueIndex, uuid } from "drizzle-orm/pg-core";

import { geometryPoint } from "./columns";
import { maintenancePredictions, vehicles } from "./fleet";

export const infra = pgSchema("infra");

export const stations = infra.table("stations", {
  id: uuid("id").primaryKey().defaultRandom(),
  name: text("name").notNull(),
  capacityKg: numeric("capacity_kg"),
  availableKg: numeric("available_kg"),
  status: text("status").notNull().default("online"), // online|offline|maintenance
  // 0008 (Wave-5): station energy domain — existing stations stay 'h2'.
  // CHECK (station_type IN ('h2','ev_charger','diesel','cng','mixed')) lives
  // in the goose migration.
  stationType: text("station_type").notNull().default("h2"), // h2|ev_charger|diesel|cng|mixed
  availableKwh: numeric("available_kwh"), // EV inventory (ev_charger stations)
  chargerCount: integer("charger_count"), // number of charge points (ev_charger/mixed)
  geom: geometryPoint("geom"),
});

// 0008 (Wave-5): OCPP charge-point inventory, written by
// services/python/ocpp-gateway (W4).
export const chargePoints = infra.table(
  "charge_points",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    stationId: uuid("station_id").notNull().references(() => stations.id),
    ocppId: text("ocpp_id").notNull().unique(),
    vendor: text("vendor"),
    model: text("model"),
    status: text("status").notNull().default("Unavailable"), // OCPP 1.6J charge-point status
    lastHeartbeat: timestamp("last_heartbeat", { withTimezone: true }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    stationIdx: index("charge_points_station_idx").on(t.stationId),
  }),
);

// 0008 (Wave-5): OCPP charging sessions (StartTransaction/StopTransaction).
export const chargingSessions = infra.table(
  "charging_sessions",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    chargePointId: uuid("charge_point_id").notNull().references(() => chargePoints.id),
    busId: text("bus_id"), // free-text from OCPP idTag/vehicle mapping, may be NULL
    connectorId: integer("connector_id").notNull(),
    idTag: text("id_tag"),
    meterStart: numeric("meter_start").notNull(),
    meterStop: numeric("meter_stop"),
    kwh: numeric("kwh"),
    startedAt: timestamp("started_at", { withTimezone: true }).notNull(),
    stoppedAt: timestamp("stopped_at", { withTimezone: true }),
    status: text("status").notNull().default("active"), // active|completed|failed
  },
  (t) => ({
    cpIdx: index("charging_sessions_cp_idx").on(t.chargePointId, t.startedAt),
  }),
);

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
    // 0007 (W3c): resolution timestamp for compliance MTTR reporting.
    resolvedAt: timestamp("resolved_at", { withTimezone: true }),
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

export const workOrders = infra.table(
  "work_orders",
  {
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
  },
  (t) => ({
    // 0007 (W3): at most one OPEN work order per maintenance prediction, so
    // the maintenance.predicted consumer can retry/replay without duplicates.
    openPredictionUq: uniqueIndex("work_orders_open_prediction_uq")
      .on(t.predictionId)
      .where(sql`${t.predictionId} IS NOT NULL AND ${t.status} <> 'closed'`),
  }),
);

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
    // 0007 (W3b): completion timestamp for service-time (wait estimate) stats.
    completedAt: timestamp("completed_at", { withTimezone: true }),
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
