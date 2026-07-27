# Wave 5 — Multi-Energy Fleet Generalization (H2 + Battery + Diesel + CNG)

Goal (user-approved 2026-07-26): make the platform work with non-hydrogen buses and mixed fleets — energy-vector schema abstraction, station-type abstraction + OCPP charger integration, safety domain packs, twin generalization, compliance template packs. Preserve 100% backward compat for the H2 fleet.

## Schema contract (fixed upfront — all agents code against this; W1 writes migration 0008)

- `fleet.vehicles`: `energy_type text NOT NULL DEFAULT 'h2' CHECK (energy_type IN ('h2','battery','diesel','cng'))`
- telemetry hypertable: additive nullable `energy_level_pct numeric`, `powertrain_kw numeric`, `energy_type text NULL`; h2_level_pct/fuel_cell_kw stay (H2 writes both)
- `infra.stations`: `station_type text NOT NULL DEFAULT 'h2' CHECK IN ('h2','ev_charger','diesel','cng','mixed')`, `available_kwh numeric NULL`, `charger_count integer NULL`
- NEW `infra.charge_points(id uuid pk, station_id uuid fk, ocpp_id text unique not null, vendor text, model text, status text not null default 'Unavailable', last_heartbeat timestamptz, created_at timestamptz default now())`
- NEW `infra.charging_sessions(id uuid pk, charge_point_id uuid fk, bus_id text null, connector_id int not null, id_tag text null, meter_start numeric not null, meter_stop numeric null, kwh numeric null, started_at timestamptz not null, stopped_at timestamptz null, status text not null default 'active')`
- Events (ADDITIVE, backward compatible): telemetry.raw/enriched += energy_level_pct, powertrain_kw, energy_type (all optional); fuel.reading += energy_level_pct, energy_unit ('kg'|'kwh'|'liters'); station.status.changed += station_type, available_kwh; energy trades kind enum += 'ev-v2g-export','ev-charge-purchase'
- Twin: status labels — keep 'refueling' (h2) + add 'charging' (battery), 'refueling_diesel' (diesel); generalized energy-replenishment trend detection
- Drizzle mirror for all of the above in packages/db

## Workstreams (parallel, strict ownership)

- **W1** (schemas+Go): migration 0008, packages/events schemas+fixtures+asyncapi+validator, packages/db, services/go/fleet-api (energy_type on vehicles), services/go/infra-api (station types, EV inventory in kWh, charger read APIs, compliance template packs per domain), services/go/commerce-api (new trade kinds incl. V2G settlement through clearing account pattern), PWA energy-type labels (minor).
- **W2** (Rust): digital-twin — generic energy-replenishment detection + new labels + energy fields in twin state; telemetry-ingest — accept/map generic fields (energy_level_pct ⇄ h2_level_pct compat). cargo check/test --locked.
- **W3** (Python, existing services): safety domain packs — leak autoencoder generalized to anomaly domains ('h2' ppm / 'ev_thermal' cell temp+voltage), synth generator multi-energy fleets, carbon baselines per energy_type (diesel-reference methodology), route-optimizer consumption/range per energy type, ml-platform feature names generalized (h2 features kept as one domain).
- **W4** (new OCPP service): services/python/ocpp-gateway (:8100) — OCPP 1.6J CSMS (Python `ocpp` lib, websockets): BootNotification/Heartbeat/StatusNotification/MeterValues/StartTransaction/StopTransaction → infra.charge_points + infra.charging_sessions (contract above) + Kafka station.status.changed events; /healthz + /metrics; Dockerfile + compose entry (profile apps/all) + README + openapi-ish endpoint doc for its REST read API; pytest with a mock charge point.

## Wave 5B — integration gate + push
Full compile gate (Go/Rust/Python/TS + validators + scenario validator stays green), then delta push to GitHub main, byte-verify.

## Explicitly out of scope (honest)
OCPP 2.0.1 (1.6J covers the deployed base; note the upgrade path), real charger hardware certification, EV thermal model *training* (data-bound; pack ships with synth-trained weights + continuous-training path), diesel-specific telematics DBCs.
