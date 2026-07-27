-- =============================================================================
-- H2Fleet — 0008_energy_vectors.sql (goose)
-- Wave-5 multi-energy fleet generalization (plan-wave5.md schema contract):
--   1. fleet.vehicles.energy_type — h2|battery|diesel|cng (DEFAULT 'h2', so
--      the existing fleet and all current writes stay H2 unchanged).
--   2. fleet.telemetry — additive generic energy columns (energy_level_pct,
--      powertrain_kw, energy_type). h2_level_pct/fuel_cell_kw stay; H2 buses
--      write both. Backfilled from the H2 columns.
--   3. infra.stations — station_type (h2|ev_charger|diesel|cng|mixed,
--      DEFAULT 'h2'), available_kwh, charger_count. available_kg stays the
--      H2/diesel/CNG inventory field.
--   4. NEW infra.charge_points — OCPP charger inventory (W4 ocpp-gateway).
--   5. NEW infra.charging_sessions — OCPP charging sessions (W4 ocpp-gateway).
-- Everything is idempotent (IF NOT EXISTS / DO blocks), same contract as
-- 0003/0005/0006/0007. 100% backward compatible: no renames, no drops in Up.
-- =============================================================================

-- +goose Up

-- 1 — vehicle energy vector.
ALTER TABLE fleet.vehicles ADD COLUMN IF NOT EXISTS energy_type text NOT NULL DEFAULT 'h2';
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'vehicles_energy_type_check') THEN
        ALTER TABLE fleet.vehicles
            ADD CONSTRAINT vehicles_energy_type_check
            CHECK (energy_type IN ('h2','battery','diesel','cng'));
    END IF;
END $$;
-- +goose StatementEnd

-- 2 — generic telemetry energy columns (additive, nullable).
ALTER TABLE fleet.telemetry ADD COLUMN IF NOT EXISTS energy_level_pct numeric;
ALTER TABLE fleet.telemetry ADD COLUMN IF NOT EXISTS powertrain_kw numeric;
ALTER TABLE fleet.telemetry ADD COLUMN IF NOT EXISTS energy_type text;

-- Backfill: existing rows are all H2 buses (telemetry-ingest wrote
-- h2_level_pct/fuel_cell_kw), so mirror them into the generic fields.
UPDATE fleet.telemetry
SET energy_level_pct = h2_level_pct,
    powertrain_kw    = fuel_cell_kw,
    energy_type      = 'h2'
WHERE energy_type IS NULL;

-- 3 — station type + EV inventory.
ALTER TABLE infra.stations ADD COLUMN IF NOT EXISTS station_type text NOT NULL DEFAULT 'h2';
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'stations_station_type_check') THEN
        ALTER TABLE infra.stations
            ADD CONSTRAINT stations_station_type_check
            CHECK (station_type IN ('h2','ev_charger','diesel','cng','mixed'));
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE infra.stations ADD COLUMN IF NOT EXISTS available_kwh numeric;
ALTER TABLE infra.stations ADD COLUMN IF NOT EXISTS charger_count integer;

-- 4 — OCPP charge-point inventory (written by services/python/ocpp-gateway).
CREATE TABLE IF NOT EXISTS infra.charge_points (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    station_id     uuid NOT NULL REFERENCES infra.stations(id),
    ocpp_id        text UNIQUE NOT NULL,
    vendor         text,
    model          text,
    status         text NOT NULL DEFAULT 'Unavailable',
    last_heartbeat timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS charge_points_station_idx ON infra.charge_points (station_id);

-- 5 — OCPP charging sessions.
CREATE TABLE IF NOT EXISTS infra.charging_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    charge_point_id uuid NOT NULL REFERENCES infra.charge_points(id),
    bus_id          text,
    connector_id    integer NOT NULL,
    id_tag          text,
    meter_start     numeric NOT NULL,
    meter_stop      numeric,
    kwh             numeric,
    started_at      timestamptz NOT NULL,
    stopped_at      timestamptz,
    status          text NOT NULL DEFAULT 'active'   -- active|completed|failed
);
CREATE INDEX IF NOT EXISTS charging_sessions_cp_idx ON infra.charging_sessions (charge_point_id, started_at DESC);

-- +goose Down

DROP TABLE IF EXISTS infra.charging_sessions;
DROP TABLE IF EXISTS infra.charge_points;

ALTER TABLE infra.stations DROP COLUMN IF EXISTS charger_count;
ALTER TABLE infra.stations DROP COLUMN IF EXISTS available_kwh;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'stations_station_type_check') THEN
        ALTER TABLE infra.stations DROP CONSTRAINT stations_station_type_check;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE infra.stations DROP COLUMN IF EXISTS station_type;

ALTER TABLE fleet.telemetry DROP COLUMN IF EXISTS energy_type;
ALTER TABLE fleet.telemetry DROP COLUMN IF EXISTS powertrain_kw;
ALTER TABLE fleet.telemetry DROP COLUMN IF EXISTS energy_level_pct;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'vehicles_energy_type_check') THEN
        ALTER TABLE fleet.vehicles DROP CONSTRAINT vehicles_energy_type_check;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE fleet.vehicles DROP COLUMN IF EXISTS energy_type;
