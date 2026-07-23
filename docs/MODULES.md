# H2Fleet Modules

The 20 capability modules (SPEC §3.1) grouped by domain. Module ids are exact
contract strings used everywhere: `feature_toggles.module`, toggle SDK calls,
PWA registry, Kafka consumer guards.

| # | Module id | Domain | Purpose | Owning service(s) | Events (produces ▸ consumes) | Behavior when toggled OFF |
|---|---|---|---|---|---|---|
| 1 | `telematics` | fleet | Real-time vehicle telemetry ingestion & live map | telemetry-ingest, fleet-api | ▸ `telemetry.enriched` ◂ `telemetry.raw` | Ingest consumer idles; `/api/fleet/telemetry*` and live map 404/hidden |
| 2 | `predictive-maintenance` | fleet | ML failure prediction on fuel-cell / battery / H2 systems | predictive-maintenance | ▸ `maintenance.predicted` ◂ `twin.updated` | Scoring loop stops; `/api/ml/*` 404; Maintenance page hidden |
| 3 | `digital-twin` | fleet | Per-bus digital twin state (Rust hot path) | digital-twin | ▸ `twin.updated` ◂ `telemetry.enriched` | Twin engine idles; `/api/twin/*` 404 |
| 4 | `fuel-monitoring` | fleet | H2 tank levels, consumption, range prediction | fleet-api | ▸ `fuel.reading` ◂ `telemetry.enriched` | Fuel gauges/range endpoints 404; fuel widgets hidden |
| 5 | `route-energy-optimizer` | fleet | Route + refueling schedule optimization | route-optimizer | ◂ `station.status.changed`, `fuel.reading` | `/api/optimize/*` 404; optimizer jobs skipped |
| 6 | `refueling-stations` | infra | Station status, queue management, H2 inventory | infra-api | ▸ `station.status.changed` | Station endpoints 404; Stations page hidden |
| 7 | `leak-detection` | infra | H2 leak sensor ingestion, alarms, incident workflow | infra-api | ▸ `safety.leak.detected` | Alarm endpoints 404; incident Temporal workflow not started |
| 8 | `dispatch-workforce` | infra | Driver scheduling & dispatch (Temporal workflows) | infra-api | ▸ `dispatch.job.assigned` | Dispatch endpoints 404; workflows not started |
| 9 | `compliance-reporting` | infra | Regulatory & safety compliance reports | infra-api (+ cron binding) | ◂ `safety.leak.detected`, `maintenance.predicted` | Report generation cron skipped; endpoints 404 |
| 10 | `depot-management` | infra | Depot bays, fueling/charging assets, work orders | infra-api | ◂ `maintenance.predicted` | Depot/work-order endpoints 404 |
| 11 | `passenger-pwa` | citizen | Citizen PWA: arrivals, journey planner, service alerts | citizen-api, apps/pwa | ◂ `station.status.changed` | Citizen pages hidden; public arrival endpoints 404 |
| 12 | `mobile-app` | citizen | Native mobile (Expo) citizen + driver apps | citizen-api, apps/mobile | ◂ `drt.requested`, `dispatch.job.assigned` | Mobile-facing endpoints 404; app shows module-off screen |
| 13 | `demand-responsive` | citizen | DRT on-demand shuttle requests | citizen-api | ▸ `drt.requested` ◂ `dispatch.job.assigned` | DRT booking endpoints 404; DRT page hidden |
| 14 | `carbon-credits` | citizen | CO2-avoided accounting + credit issuance | carbon-analytics | ▸ `carbon.credit.issued` | Daily issuance cron skipped; Carbon page hidden |
| 15 | `open-data-portal` | citizen | GTFS/GTFS-RT + open datasets + OpenSearch API | citizen-api | ◂ lakehouse ETL output | `/api/citizen/open-data*` + `/api/open-data/*` 404 |
| 16 | `fare-payments` | commerce | Fare collection (Mojaloop rails, TigerBeetle ledger) | commerce-api | ▸ `fare.payment.initiated`, `fare.payment.settled` | Payment endpoints 404; settlement workflow/cron skipped |
| 17 | `loyalty-marketplace` | commerce | Citizen rewards, local business marketplace | commerce-api | ◂ `fare.payment.settled`, `carbon.credit.issued` | Marketplace endpoints 404; page hidden |
| 18 | `energy-trading` | commerce | Surplus H2/energy trading ledger | commerce-api | ▸ `energy.trade.executed` | Trading endpoints 404; ledger postings stop |
| 19 | `gov-dashboard` | commerce | City KPI dashboard (cost, emissions, ridership, uptime) | commerce-api (+ lakehouse) | ◂ all domain topics (aggregations) | KPI endpoints 404; Dashboard page hidden |
| 20 | `advertising` | commerce | On-bus / digital ad inventory & campaigns | commerce-api | — | Ad inventory endpoints 404; page hidden |

## Toggle contract recap (SPEC §3.2)

* `GET /v1/toggles` → all modules; `GET /v1/toggles/{module}` → one;
  `PUT /v1/toggles/{module}` (role `platform-admin`) flips it.
* Storage `public.feature_toggles`; Redis cache `toggles:<module>` TTL 30 s;
  `toggle.changed` broadcast on Kafka.
* SDK semantics identical in Go/TS/Python: `isEnabled(module)`, 5 s local
  cache, **fail-closed** (disabled on error).
* OFF ⇒ routes 404, UI nav/bundle gone, Kafka consumers idle, Temporal
  workflows not started, scoped Dapr components not loaded.
