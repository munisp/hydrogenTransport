// Prometheus metrics for analytics-bff — same contract as the Go services:
// http_requests_total{service,route,status} + process default metrics,
// scraped at GET /metrics.
import { collectDefaultMetrics, Counter, Registry } from "prom-client";
import type { Context, Next } from "hono";

export const registry = new Registry();
collectDefaultMetrics({ register: registry });

const requestsTotal = new Counter({
  name: "http_requests_total",
  help: "Total HTTP requests handled, partitioned by service, route pattern and status code.",
  labelNames: ["service", "route", "status"],
  registers: [registry],
});

const SERVICE = "analytics-bff";

export async function metricsMiddleware(c: Context, next: Next) {
  await next();
  const route = c.req.routePath || "unmatched";
  requestsTotal.inc({ service: SERVICE, route, status: String(c.res.status) });
}

export async function metricsHandler(c: Context) {
  const body = await registry.metrics();
  return new Response(body, {
    headers: { "Content-Type": registry.contentType },
  });
}
