// public schema — platform-level tables (migration 0001_core.sql).
import { boolean, pgTable, text, timestamp } from "drizzle-orm/pg-core";

export const featureToggles = pgTable("feature_toggles", {
  module: text("module").primaryKey(),
  domain: text("domain").notNull(),
  enabled: boolean("enabled").notNull().default(true),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
});
