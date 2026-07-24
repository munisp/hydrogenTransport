-- =============================================================================
-- H2Fleet — 0001_core.sql (goose)
-- Extensions, per-domain schemas and ALL core tables (SPEC §3.4).
-- Canonical schema source; supersedes infra/sql/001_init.sql (kept for
-- docker-entrypoint-initdb.d compatibility on fresh boots).
-- Image: timescale/timescaledb-ha (ships postgis + timescaledb).
-- NOTE: feature_toggles.enabled defaults to TRUE (reconciled with the seed
-- behaviour — all modules ship enabled unless explicitly toggled off).
-- =============================================================================

-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()

CREATE SCHEMA IF NOT EXISTS fleet;
CREATE SCHEMA IF NOT EXISTS infra;
CREATE SCHEMA IF NOT EXISTS citizen;
CREATE SCHEMA IF NOT EXISTS commerce;

-- ---------------------------------------------------------------------------
-- Platform-level: feature toggles (SPEC §3.2). Queried by toggle-service,
-- cached in Redis (toggles:<module>, TTL 30s), changes published to Kafka
-- topic `toggle.changed`.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.feature_toggles (
    module     text PRIMARY KEY,
    domain     text NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- DOMAIN 1 — fleet
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS fleet.vehicles (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fleet_no       text UNIQUE NOT NULL,
    vin            text,
    model          text,
    h2_capacity_kg numeric,
    status         text NOT NULL DEFAULT 'active',   -- active|maintenance|depot|retired
    geom           geometry(Point, 4326)
);

-- Telemetry hypertable: primary hot path written by telemetry-ingest (Rust).
CREATE TABLE IF NOT EXISTS fleet.telemetry (
    bus_id          uuid        NOT NULL REFERENCES fleet.vehicles(id),
    ts              timestamptz NOT NULL,
    speed_kph       numeric,
    h2_level_pct    numeric,
    fuel_cell_kw    numeric,
    battery_soc_pct numeric,
    odometer_km     numeric,
    geom            geometry(Point, 4326)
);
SELECT create_hypertable('fleet.telemetry', 'ts', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS telemetry_bus_ts_idx ON fleet.telemetry (bus_id, ts DESC);

CREATE TABLE IF NOT EXISTS fleet.maintenance_predictions (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bus_id               uuid NOT NULL REFERENCES fleet.vehicles(id),
    component            text NOT NULL,              -- fuel-cell|battery|h2-system|powertrain
    risk_score           numeric NOT NULL,           -- 0..1
    predicted_failure_at timestamptz,
    model_version        text,
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS maintenance_pred_bus_idx ON fleet.maintenance_predictions (bus_id, created_at DESC);

-- Digital twin snapshots: persisted state behind the Redis hot state.
CREATE TABLE IF NOT EXISTS fleet.twin_snapshots (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bus_id     uuid NOT NULL REFERENCES fleet.vehicles(id),
    state      jsonb NOT NULL,   -- {speed_kph,h2_level_pct,fuel_cell_kw,battery_soc_pct,odometer_km,geom,...}
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS twin_snapshots_bus_idx ON fleet.twin_snapshots (bus_id, updated_at DESC);

-- ---------------------------------------------------------------------------
-- DOMAIN 2 — infra
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS infra.stations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    capacity_kg  numeric,
    available_kg numeric,
    status       text NOT NULL DEFAULT 'online',     -- online|offline|maintenance
    geom         geometry(Point, 4326)
);

CREATE TABLE IF NOT EXISTS infra.incidents (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type       text NOT NULL,                        -- leak|collision|fuel-cell-fault|station-fault|other
    severity   text NOT NULL DEFAULT 'low',          -- low|medium|high|critical
    bus_id     uuid REFERENCES fleet.vehicles(id),
    station_id uuid REFERENCES infra.stations(id),
    status     text NOT NULL DEFAULT 'open',         -- open|acknowledged|resolved
    opened_at  timestamptz NOT NULL DEFAULT now(),
    meta       jsonb
);

-- ---------------------------------------------------------------------------
-- DOMAIN 3 — citizen
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS citizen.drt_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_sub     text NOT NULL,                      -- Keycloak subject
    pickup       geometry(Point, 4326) NOT NULL,
    dropoff      geometry(Point, 4326) NOT NULL,
    status       text NOT NULL DEFAULT 'requested',  -- requested|assigned|enroute|completed|cancelled
    requested_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS citizen.carbon_credits (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    period          text NOT NULL,                   -- e.g. '2024-05' or '2024-W22'
    kg_co2_avoided  numeric NOT NULL,
    credits         numeric NOT NULL,
    issued_at       timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- DOMAIN 4 — commerce
-- TigerBeetle holds the authoritative double-entry ledger; account id ranges:
--   RIDER_WALLET=1xxx, OPERATOR_REVENUE=2xxx, ENERGY_TRADE=3xxx, CARBON_FUND=4xxx
-- These tables are the query/index side of the ledger.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS commerce.fare_payments (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rider_sub            text NOT NULL,              -- Keycloak subject
    amount_minor         bigint NOT NULL,            -- minor units (cents)
    currency             text NOT NULL DEFAULT 'EUR',
    mojaloop_transfer_id text,                       -- FSPIOP transfer id from simulator
    status               text NOT NULL DEFAULT 'initiated', -- initiated|settled|failed|refunded
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS fare_payments_rider_idx ON commerce.fare_payments (rider_sub, created_at DESC);

CREATE TABLE IF NOT EXISTS commerce.trades (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        text NOT NULL,                       -- h2-sale|h2-purchase|energy-export
    quantity_kg numeric NOT NULL,
    price_minor bigint NOT NULL,
    status      text NOT NULL DEFAULT 'executed',    -- proposed|executed|settled|cancelled
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Convenience: updated_at maintenance for toggles.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.touch_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END; $$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS feature_toggles_touch ON public.feature_toggles;
CREATE TRIGGER feature_toggles_touch
    BEFORE UPDATE ON public.feature_toggles
    FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS feature_toggles_touch ON public.feature_toggles;
DROP FUNCTION IF EXISTS public.touch_updated_at();

DROP TABLE IF EXISTS commerce.trades;
DROP TABLE IF EXISTS commerce.fare_payments;
DROP TABLE IF EXISTS citizen.carbon_credits;
DROP TABLE IF EXISTS citizen.drt_requests;
DROP TABLE IF EXISTS infra.incidents;
DROP TABLE IF EXISTS infra.stations;
DROP TABLE IF EXISTS fleet.twin_snapshots;
DROP TABLE IF EXISTS fleet.maintenance_predictions;
DROP TABLE IF EXISTS fleet.telemetry;
DROP TABLE IF EXISTS fleet.vehicles;
DROP TABLE IF EXISTS public.feature_toggles;

DROP SCHEMA IF EXISTS commerce;
DROP SCHEMA IF EXISTS citizen;
DROP SCHEMA IF EXISTS infra;
DROP SCHEMA IF EXISTS fleet;

DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS timescaledb;
DROP EXTENSION IF EXISTS postgis;
