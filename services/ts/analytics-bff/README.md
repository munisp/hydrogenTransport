# analytics-bff (:8096)

Read-only analytics backend-for-frontend — the **innovation path for future
TypeScript services** in H2Fleet: Hono + Drizzle ORM (`@h2fleet/db`, see
`packages/db/README.md`) + jose JWT + zod + prom-client.

## Endpoints

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| GET | `/v1/analytics/fleet-summary` | JWT + role `operator`/`platform-admin`/`analyst` | Vehicles by status, mean latest H2 level per bus, station availability |
| GET | `/v1/analytics/revenue-daily?days=30` | same | Settled fare revenue + executed trade value per day (minor units) |
| GET | `/v1/analytics/carbon-daily?days=30` | same | Avoided CO₂ + issued credits per day |
| GET | `/healthz` | public | Liveness/readiness (`select 1`) |
| GET | `/metrics` | cluster | Prometheus (`http_requests_total{service,route,status}` + defaults) |

Outputs are **zod-validated** before sending — schema drift fails loudly
instead of leaking malformed analytics to dashboards.

## Auth

jose RS256 JWKS against `KEYCLOAK_ISSUER` (`/protocol/openid-connect/certs`),
mirroring `packages/go-auth`: accepts `KEYCLOAK_ISSUER` +
`KEYCLOAK_ISSUER_ALT` (default browser issuer), validates `aud` only when
`KEYCLOAK_AUDIENCE` is set, fail-closed 503 when the issuer is unset.

## Env

`PORT` (8096), `DATABASE_URL` (required — use a **SELECT-only** role in real
deployments; this service is read-only by contract), `KEYCLOAK_ISSUER`,
`KEYCLOAK_ISSUER_ALT`, `KEYCLOAK_AUDIENCE`.

## Dev / build / test

```sh
npm ci
npm run typecheck   # strict tsc
npm test            # vitest, mocked pg + real RS256 JWTs via local JWKS
npm run build       # esbuild bundle -> dist/server.cjs (pg external)
npm start           # node dist/server.cjs
```

Docker build context is the repo root (`@h2fleet/db` is a `file:` dependency).
