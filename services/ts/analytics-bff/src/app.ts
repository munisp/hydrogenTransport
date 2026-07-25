// App assembly (separated from the server entry so tests can inject a
// mocked db and auth middlewares).
import { Hono } from "hono";

import { jwtAuth, requireAnyRole, type AuthConfig, type AuthEnv } from "./auth";
import { metricsHandler, metricsMiddleware } from "./metrics";
import { analyticsRoutes, type Db } from "./routes/analytics";

export interface AppDeps {
  db: Db;
  authCfg: AuthConfig;
  ping: () => Promise<void>; // db liveness probe for /healthz
}

export function buildApp(deps: AppDeps) {
  const app = new Hono<AuthEnv>();

  app.use("*", metricsMiddleware);

  app.get("/healthz", async (c) => {
    try {
      await deps.ping();
      return c.json({ status: "ok" });
    } catch {
      return c.json({ status: "unhealthy" }, 503);
    }
  });
  app.get("/metrics", metricsHandler);

  // Analytics surface: valid Keycloak JWT + operator/platform-admin/analyst
  // realm role (same gating intent as admin-api's operator+ surface).
  app.use(
    "/v1/analytics/*",
    jwtAuth(deps.authCfg),
    requireAnyRole("operator", "platform-admin", "analyst"),
  );
  app.route("/v1/analytics", analyticsRoutes(deps.db));

  return app;
}
