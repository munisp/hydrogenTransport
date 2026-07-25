import { defineConfig } from "drizzle-kit";

// Drizzle Kit config for @h2fleet/db. The SQL DDL remains authored in
// infra/sql/migrations (goose) — this config exists so `drizzle-kit check`
// can statically validate the TypeScript schema, and so future TS services
// can opt into drizzle-kit introspect/diff against a live database.
export default defineConfig({
  dialect: "postgresql",
  schema: "./src/schema/index.ts",
  out: "./drizzle",
  dbCredentials: {
    // Only used by drizzle-kit introspect/generate against a live DB.
    url: process.env.DATABASE_URL ?? "postgres://h2:h2pass@localhost:5432/h2fleet",
  },
  // TimescaleDB/PostGIS extensions and hypertables live in goose migrations;
  // Drizzle models the plain relational surface only.
  extensionsFilters: ["postgis"],
  strict: true,
  verbose: true,
});
