// citizen schema (migration 0001_core.sql + 0005_missing_schemas.sql).
import { integer, numeric, pgSchema, text, timestamp, uuid } from "drizzle-orm/pg-core";

import { geometryPoint } from "./columns";
import { vehicles } from "./fleet";

export const citizen = pgSchema("citizen");

export const drtRequests = citizen.table("drt_requests", {
  id: uuid("id").primaryKey().defaultRandom(),
  userSub: text("user_sub").notNull(), // Keycloak subject
  pickup: geometryPoint("pickup").notNull(),
  dropoff: geometryPoint("dropoff").notNull(),
  status: text("status").notNull().default("requested"), // requested|assigned|enroute|completed|cancelled
  requestedAt: timestamp("requested_at", { withTimezone: true }).notNull().defaultNow(),
  // 0005: passenger-supplied labels (previously silently dropped) + assignment
  pickupLabel: text("pickup_label").notNull().default(""),
  dropoffLabel: text("dropoff_label").notNull().default(""),
  passengers: integer("passengers").notNull().default(1),
  vehicleId: uuid("vehicle_id").references(() => vehicles.id),
  driverSub: text("driver_sub"),
  assignedAt: timestamp("assigned_at", { withTimezone: true }),
});

export const carbonCredits = citizen.table("carbon_credits", {
  id: uuid("id").primaryKey().defaultRandom(),
  period: text("period").notNull(), // e.g. '2024-05' or '2024-W22'
  kgCo2Avoided: numeric("kg_co2_avoided").notNull(),
  credits: numeric("credits").notNull(),
  issuedAt: timestamp("issued_at", { withTimezone: true }).notNull().defaultNow(),
});
