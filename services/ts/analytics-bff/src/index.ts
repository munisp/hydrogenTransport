// analytics-bff — read-only analytics backend-for-frontend (Hono + Drizzle).
// Port 8096 (gateway prefix /api/analytics/*). See README.md.
import { serve } from "@hono/node-server";
import pg from "pg";
import { drizzle } from "drizzle-orm/node-postgres";
import { sql } from "drizzle-orm";

import { buildApp } from "./app";
import { loadAuthConfig } from "./auth";

const port = Number(process.env.PORT ?? 8096);
const databaseUrl = process.env.DATABASE_URL;
if (!databaseUrl) {
  console.error("DATABASE_URL is required");
  process.exit(1);
}

const pool = new pg.Pool({ connectionString: databaseUrl, max: 10 });
const db = drizzle(pool);

const app = buildApp({
  db,
  authCfg: loadAuthConfig(),
  ping: async () => {
    await db.execute(sql`select 1`);
  },
});

const server = serve({ fetch: app.fetch, port }, (info) => {
  console.log(`analytics-bff listening on :${info.port}`);
});

for (const sig of ["SIGINT", "SIGTERM"] as const) {
  process.on(sig, () => {
    server.close(async () => {
      await pool.end();
      process.exit(0);
    });
    setTimeout(() => process.exit(1), 10_000).unref();
  });
}
