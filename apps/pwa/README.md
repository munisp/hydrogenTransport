# H2Fleet PWA (`apps/pwa`)

Unified React 18 + TypeScript + Vite PWA for the hydrogen bus platform (SPEC §3.7).
One shell, 20 toggleable modules grouped into 4 domains: fleet, infra, citizen, commerce.

## Architecture

- **Module registry** (`src/modules/registry.ts`) — maps all 20 module ids from SPEC §3.1
  to lazy-loaded React pages, grouped by domain. Every module is its own code-split bundle.
- **Toggle-driven shell** (`src/toggles/TogglesContext.tsx`) — at boot the app fetches
  `GET /api/toggles/v1/toggles` through `@h2fleet/toggle-client` (5s cache, fail-closed)
  and **polls every 30s** (`VITE_TOGGLE_POLL_MS`). Disabled modules vanish from the nav,
  and direct deep-links hit `RequireModule`, which renders a "module disabled" state.
- **Auth** (`src/auth/`) — `keycloak-js` with PKCE + `silent-check-sso.html`. When the IdP
  is unreachable in `vite dev`, the app falls back to a mock `platform-admin` identity
  (clearly badged in the header); in production builds auth failure is fatal.
- **API layer** (`src/api/`) — typed clients per APISIX prefix (SPEC §3.6):
  `/api/fleet`, `/api/infra`, `/api/citizen`, `/api/commerce`, `/api/toggles`,
  `/api/ml`, `/api/optimize`, `/api/twin`. React Query handles caching, polling and
  mutations; responses tolerate both `{ data: ... }` envelopes and bare payloads.
- **PWA** — `vite-plugin-pwa`: manifest "H2Fleet", service worker with offline app shell
  (`navigateFallback`), API traffic is `NetworkOnly`, map tiles are cached.

## Pages

| Module id | Page | Data |
|---|---|---|
| `gov-dashboard` | KPI cards (uptime, CO2, credits, ridership, revenue, stations, incidents) + carbon credits chart | `GET /api/commerce/v1/gov/kpis` |
| `telematics` | MapLibre live map, 50 bus markers, click → twin panel | `/api/fleet/v1/vehicles`, `/v1/telemetry/latest`, `/api/twin/v1/twin/{id}` |
| `predictive-maintenance` | Risk table + "run prediction" trigger | `/api/fleet/v1/maintenance/predictions`, `POST /api/ml/v1/predict` |
| `digital-twin` | Per-bus twin gauges, status | `/api/twin/v1/twin/{id}` |
| `fuel-monitoring` | Tank levels, onboard H2, estimated range | `/api/fleet/v1/fuel/levels` |
| `route-energy-optimizer` | Optimize form + plan w/ refuel stops | `POST /api/optimize/v1/optimize/route` |
| `refueling-stations` | Station gauges, queue, low-inventory alerts | `/api/infra/v1/stations` |
| `leak-detection` | Incident table (ack/resolve) + leak feed | `/api/infra/v1/incidents` (+ `?type=leak`) |
| `dispatch-workforce` | Driver shift table | `/api/infra/v1/dispatch/jobs` (+ `POST /v1/dispatch/jobs/{id}/accept`) |
| `compliance-reporting` | Reports + generate | `/api/infra/v1/compliance/reports` (+ `POST /v1/compliance/reports/generate`) |
| `depot-management` | Bay grid + work orders | `/api/infra/v1/depot/bays`, `/v1/depot/work-orders` |
| `passenger-pwa` | Arrivals board, journey planner, alerts | `/api/citizen/v1/passenger/stops|arrivals|journey|alerts` |
| `mobile-app` | Driver portal (jobs, incident report) | `/api/infra/v1/dispatch/jobs`, `POST /v1/incidents` |
| `demand-responsive` | DRT request form + status list | `/api/citizen/v1/drt/requests` |
| `carbon-credits` | Credits chart + issuance history | `/api/citizen/v1/carbon/credits` |
| `open-data-portal` | GTFS/GTFS-RT dataset table | `/api/citizen/v1/opendata/datasets` |
| `fare-payments` | Payments table w/ status filter | `/api/commerce/v1/payments` |
| `loyalty-marketplace` | Offer cards + redeem | `/api/commerce/v1/marketplace/offers` |
| `energy-trading` | Trade ledger + new trade | `/api/commerce/v1/energy/trades` |
| `advertising` | Campaign table w/ flight progress | `/api/commerce/v1/ads/campaigns` |
| — | Admin: toggle switches for all 20 modules (role `platform-admin`) | `PUT /api/toggles/v1/toggles/{module}` |

Field names mirror the Postgres schemas in SPEC §3.4 (snake_case).

## Toggle client

The PWA consumes the shared TS SDK with a `file:` dependency
(`"@h2fleet/toggle-client": "file:../../packages/toggle-client/ts"`). The package entry
point is its TypeScript source, compiled by Vite as part of the app build — no separate
build step needed. Contract: 5s cache, fail-closed (SPEC §3.2).

## Development

```bash
cp .env.example .env        # optional; defaults work out of the box
npm install
npm run dev                 # http://localhost:5173, /api proxied to APISIX on :9080
```

Without the middleware stack, Keycloak init fails → dev falls back to the mock admin;
pages render their empty/error states (fail-closed toggles hide all modules until the
toggle service answers).

## Build & container

```bash
npm run build               # tsc --noEmit + vite build → dist/
docker build -f apps/pwa/Dockerfile -t h2fleet/pwa .   # build context = repo root
```

The image is `node:20` (build) → `nginx:1.27-alpine` (serve) with an SPA fallback and an
`/api/` reverse proxy to `apisix:9080` (see `nginx.conf`).

## Design

Low-saturation warm palette — stone base, amber (`#b45309`) and teal (`#0f766e`) accents,
generous whitespace, hand-rolled shadcn-style primitives in `src/components/ui.tsx`.
No gradients, no blue-purple.
