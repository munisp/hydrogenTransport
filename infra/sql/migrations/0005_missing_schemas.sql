-- =============================================================================
-- H2Fleet — 0005_missing_schemas.sql (goose)
-- Closes the missing-schema inventory from docs/BUSINESS_LOGIC_AUDIT.md
-- (items S1–S13) plus the platform-owned tables that previously existed
-- only as runtime EnsureSchema DDL (audit-log, admin-api) and deterministic
-- human-readable incident numbers.
--
-- Item map (audit "Missing-Schema Inventory"):
--   S1  fleet.twin_snapshots.ts        -> resolved in CODE, not DDL: the
--      digital-twin snapshot insert no longer references a `ts` column
--      (services/rust/digital-twin/src/twin.rs inserts (bus_id, state) and
--      lets `updated_at` default to now()). The audit's alternative ("add ts
--      column OR change insert") is satisfied by the code fix; adding a
--      second drifting timestamp column is deliberately avoided.
--   S2  UNIQUE(period) on citizen.carbon_credits (double-issuance guard).
--   S3  citizen.drt_requests: pickup_label / dropoff_label / passengers.
--   S4  citizen.drt_requests: vehicle_id / driver_sub / assigned_at.
--   S5  infra.dispatch_jobs.ends_at (shift_end; overlap checks expressible).
--   S6  infra.drivers reference table + NOT VALID FKs on dispatch_jobs.
--   S7  infra.station_queue (SPEC §1 "queue mgmt").
--   S8  commerce.ad_inventory + commerce.ad_placements (+ overlap exclusion).
--   S9  commerce.loyalty_accounts (rider_sub), loyalty_ledger,
--       loyalty_redemptions.
--   S10 commerce.trades.tb_transfer_id.
--   S11 commerce.fare_payments.refund_of / refunded_at.
--   S12 infra.work_orders: bus_id / prediction_id / assignee / started_at.
--   S13 fleet.stops / fleet.routes / fleet.route_stops (GTFS-like) +
--       fleet.depot_zones / fleet.route_corridors (lakehouse geo_enrich).
-- Extra:
--   X1  platform.audit_log — hash-chained append-only audit trail; mirrors
--       services/go/audit-log/internal/store/store.go EnsureSchema EXACTLY.
--   X2  platform.onboarding_requests — mirrors admin-api onboarding
--       EnsureSchema EXACTLY (services/go/admin-api/internal/onboarding/store.go).
--   X3  infra.incidents.incident_no — deterministic human-readable incident
--       number (sequence-backed default, backfilled, unique).
--
-- Analytics note: services/ts/analytics-bff only queries tables that already
-- exist (fleet.vehicles/telemetry, infra.stations, commerce.fare_payments/
-- trades, citizen.carbon_credits) — no extra views are required; its
-- fleet-summary / revenue-daily / carbon-daily endpoints aggregate those
-- sources directly.
--
-- Everything is idempotent (IF NOT EXISTS / existence-checked DO blocks), so
-- this file coexists with the services' runtime EnsureSchema calls during
-- mixed-version rollouts, same contract as 0003.
-- =============================================================================

-- +goose Up

-- ---------------------------------------------------------------------------
-- X1/X2 — platform-owned tables (previously runtime EnsureSchema only)
-- ---------------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS platform;

-- X1: append-only, hash-chained audit trail (docs/INSIDER_THREAT.md §2.2).
-- DDL mirrors services/go/audit-log/internal/store/store.go verbatim.
CREATE TABLE IF NOT EXISTS platform.audit_log (
    id          bigserial PRIMARY KEY,
    actor_sub   text        NOT NULL,
    actor_roles jsonb       NOT NULL DEFAULT '[]'::jsonb,
    action      text        NOT NULL,
    entity      text        NOT NULL,
    entity_id   text        NOT NULL DEFAULT '',
    before      jsonb,
    after       jsonb,
    ip          text        NOT NULL DEFAULT '',
    ua          text        NOT NULL DEFAULT '',
    ts          timestamptz NOT NULL DEFAULT now(),
    prev_hash   text        NOT NULL DEFAULT '',
    hash        text        NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx  ON platform.audit_log (actor_sub, ts DESC);
CREATE INDEX IF NOT EXISTS audit_log_entity_idx ON platform.audit_log (entity, entity_id, ts DESC);
CREATE INDEX IF NOT EXISTS audit_log_ts_idx     ON platform.audit_log (ts DESC);

-- X2: stakeholder onboarding intake (admin-api). DDL mirrors
-- services/go/admin-api/internal/onboarding/store.go verbatim.
CREATE TABLE IF NOT EXISTS platform.onboarding_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    persona      text NOT NULL,
    email        text NOT NULL,
    display_name text NOT NULL,
    org          text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','approved','rejected','completed')),
    keycloak_sub text NOT NULL DEFAULT '',
    meta         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS onboarding_requests_status_idx  ON platform.onboarding_requests (status);
CREATE INDEX IF NOT EXISTS onboarding_requests_persona_idx ON platform.onboarding_requests (persona);

-- ---------------------------------------------------------------------------
-- X3 — deterministic human-readable incident numbers
-- Sequence-backed DEFAULT so every new incident gets INC-000123 style
-- numbers without app-side coordination; existing rows are backfilled.
-- ---------------------------------------------------------------------------
CREATE SEQUENCE IF NOT EXISTS infra.incident_number_seq;
ALTER TABLE infra.incidents ADD COLUMN IF NOT EXISTS incident_no text;
UPDATE infra.incidents
SET incident_no = 'INC-' || lpad(nextval('infra.incident_number_seq')::text, 6, '0')
WHERE incident_no IS NULL;
ALTER TABLE infra.incidents
    ALTER COLUMN incident_no SET DEFAULT
        ('INC-' || lpad(nextval('infra.incident_number_seq'::regclass)::text, 6, '0'));
ALTER TABLE infra.incidents ALTER COLUMN incident_no SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS incidents_incident_no_uq ON infra.incidents (incident_no);

-- ---------------------------------------------------------------------------
-- S2 — carbon double-issuance guard: one credit row per period.
-- Defensive dedupe first (keep the newest issuance per period), then the
-- unique index that carbon-analytics' INSERT ... ON CONFLICT (period)
-- targets.
-- ---------------------------------------------------------------------------
DELETE FROM citizen.carbon_credits a
USING citizen.carbon_credits b
WHERE a.period = b.period
  AND (a.issued_at < b.issued_at
       OR (a.issued_at = b.issued_at AND a.ctid < b.ctid));
CREATE UNIQUE INDEX IF NOT EXISTS carbon_credits_period_uq
    ON citizen.carbon_credits (period);

-- ---------------------------------------------------------------------------
-- S3/S4 — demand-responsive transport: passenger-supplied labels and
-- assignment columns (vehicle/driver/assigned_at).
-- ---------------------------------------------------------------------------
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS pickup_label  text NOT NULL DEFAULT '';
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS dropoff_label text NOT NULL DEFAULT '';
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS passengers    integer NOT NULL DEFAULT 1;
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS vehicle_id    uuid REFERENCES fleet.vehicles(id);
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS driver_sub    text;
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS assigned_at   timestamptz;
CREATE INDEX IF NOT EXISTS drt_requests_user_idx    ON citizen.drt_requests (user_sub, requested_at DESC);
CREATE INDEX IF NOT EXISTS drt_requests_vehicle_idx ON citizen.drt_requests (vehicle_id) WHERE vehicle_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- S5/S6 — dispatch workforce: shift end, drivers reference table, and
-- (non-blocking, NOT VALID) FKs so existing free-text rows don't fail the
-- migration while new writes are checked.
-- ---------------------------------------------------------------------------
ALTER TABLE infra.dispatch_jobs ADD COLUMN IF NOT EXISTS ends_at timestamptz;

CREATE TABLE IF NOT EXISTS infra.drivers (
    sub        text PRIMARY KEY,                     -- Keycloak subject
    name       text NOT NULL DEFAULT '',
    license_no text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'active',       -- active|off-duty|suspended
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'dispatch_jobs_vehicle_fk') THEN
        ALTER TABLE infra.dispatch_jobs
            ADD CONSTRAINT dispatch_jobs_vehicle_fk
            FOREIGN KEY (vehicle_id) REFERENCES fleet.vehicles(id) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'dispatch_jobs_driver_fk') THEN
        ALTER TABLE infra.dispatch_jobs
            ADD CONSTRAINT dispatch_jobs_driver_fk
            FOREIGN KEY (driver_sub) REFERENCES infra.drivers(sub) NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

-- Double-booking guard (complements S5/S6 and the app-level transactional
-- conflict check in infra-api): at most one ACTIVE dispatch job per driver
-- and per vehicle. Partial unique indexes, so completed/cancelled history
-- is unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS dispatch_jobs_active_driver_uq
    ON infra.dispatch_jobs (driver_sub)
    WHERE status IN ('assigned', 'accepted', 'in_progress');
CREATE UNIQUE INDEX IF NOT EXISTS dispatch_jobs_active_vehicle_uq
    ON infra.dispatch_jobs (vehicle_id)
    WHERE vehicle_id IS NOT NULL AND status IN ('assigned', 'accepted', 'in_progress');

-- ---------------------------------------------------------------------------
-- S7 — station queue management (SPEC §1: "station status, queue mgmt,
-- inventory"). One active (waiting/serving) entry per bus per station.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS infra.station_queue (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    station_id uuid NOT NULL REFERENCES infra.stations(id),
    bus_id     uuid NOT NULL REFERENCES fleet.vehicles(id),
    joined_at  timestamptz NOT NULL DEFAULT now(),
    status     text NOT NULL DEFAULT 'waiting'       -- waiting|serving|completed|left
);
CREATE UNIQUE INDEX IF NOT EXISTS station_queue_active_uq
    ON infra.station_queue (station_id, bus_id)
    WHERE status IN ('waiting', 'serving');
CREATE INDEX IF NOT EXISTS station_queue_station_idx
    ON infra.station_queue (station_id, status, joined_at);

-- ---------------------------------------------------------------------------
-- S8 — advertising: inventory + campaign placements with an overlap
-- exclusion constraint (double-booking a slot is impossible at the DB
-- level). btree_gist is required for the equality half of the exclusion.
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS commerce.ad_inventory (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL,                        -- bus-side|bus-interior|shelter|digital-screen
    bus_id     uuid REFERENCES fleet.vehicles(id),   -- NULL = off-bus inventory
    label      text NOT NULL DEFAULT '',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS commerce.ad_placements (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id  uuid NOT NULL REFERENCES commerce.ad_campaigns(id) ON DELETE CASCADE,
    inventory_id uuid NOT NULL REFERENCES commerce.ad_inventory(id),
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ad_placements_no_overlap') THEN
        ALTER TABLE commerce.ad_placements
            ADD CONSTRAINT ad_placements_no_overlap
            EXCLUDE USING gist (
                inventory_id WITH =,
                tstzrange(starts_at, ends_at) WITH &&
            );
    END IF;
END $$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- S9 — loyalty marketplace. Canonical contract (coded against verbatim):
--   loyalty_accounts(rider_sub PK, points, updated_at)
--   loyalty_ledger(id, rider_sub, delta, reason, ref_id UNIQUE, created_at)
--   loyalty_redemptions(id, rider_sub, offer_id, points_spent,
--                       idempotency_key UNIQUE, status, created_at)
-- 0003 created loyalty_accounts with `user_sub`; rename to `rider_sub` when
-- the old shape is present (fresh and existing databases converge).
-- ---------------------------------------------------------------------------
-- Note: the CHECK (points >= 0) matches the 0003 shape so fresh-create and
-- rename paths converge on identical constraints.
CREATE TABLE IF NOT EXISTS commerce.loyalty_accounts (
    rider_sub  text PRIMARY KEY,
    points     integer NOT NULL DEFAULT 0 CHECK (points >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'commerce' AND table_name = 'loyalty_accounts'
                 AND column_name = 'user_sub')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                       WHERE table_schema = 'commerce' AND table_name = 'loyalty_accounts'
                         AND column_name = 'rider_sub') THEN
        ALTER TABLE commerce.loyalty_accounts RENAME COLUMN user_sub TO rider_sub;
    END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS commerce.loyalty_ledger (
    id         uuid PRIMARY KEY,
    rider_sub  text NOT NULL,
    delta      integer NOT NULL,
    reason     text NOT NULL,
    ref_id     text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS loyalty_ledger_rider_idx ON commerce.loyalty_ledger (rider_sub, created_at DESC);

CREATE TABLE IF NOT EXISTS commerce.loyalty_redemptions (
    id              uuid PRIMARY KEY,
    rider_sub       text NOT NULL,
    offer_id        uuid NOT NULL,
    points_spent    integer NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    status          text NOT NULL DEFAULT 'completed',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS loyalty_redemptions_rider_idx ON commerce.loyalty_redemptions (rider_sub, created_at DESC);

-- ---------------------------------------------------------------------------
-- S10/S11 — commerce columns: trade ↔ TigerBeetle reconciliation, refunds.
-- ---------------------------------------------------------------------------
ALTER TABLE commerce.trades ADD COLUMN IF NOT EXISTS tb_transfer_id text;

ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS refund_of uuid REFERENCES commerce.fare_payments(id);
ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS refunded_at timestamptz;

-- ---------------------------------------------------------------------------
-- S12 — depot work orders: link to bus / maintenance prediction, assignee,
-- started_at (predictive-maintenance → work-order loop becomes expressible).
-- ---------------------------------------------------------------------------
ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS bus_id uuid REFERENCES fleet.vehicles(id);
ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS prediction_id uuid REFERENCES fleet.maintenance_predictions(id);
ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS assignee text;
ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS started_at timestamptz;

-- ---------------------------------------------------------------------------
-- S13 — GTFS-like route network + geofence reference data.
-- fleet.stops / fleet.routes / fleet.route_stops replace the hardcoded Go
-- literals in citizen-api and the randomly generated stops in
-- route-optimizer; fleet.depot_zones / fleet.route_corridors are the
-- reference tables lakehouse geo_enrich looks for (with synthetic fallback).
-- Seeds below mirror today's hardcoded dataset so DB-first consumers see the
-- same network the API currently serves.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS fleet.stops (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text UNIQUE NOT NULL,                       -- e.g. 'S001'
    name text NOT NULL,
    geom geometry(Point, 4326) NOT NULL
);

CREATE TABLE IF NOT EXISTS fleet.routes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text UNIQUE NOT NULL,                 -- e.g. 'R10'
    short_name text NOT NULL DEFAULT '',
    long_name  text NOT NULL DEFAULT '',
    headway_min integer,
    active     boolean NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS fleet.route_stops (
    route_id uuid NOT NULL REFERENCES fleet.routes(id) ON DELETE CASCADE,
    stop_id  uuid NOT NULL REFERENCES fleet.stops(id),
    seq      integer NOT NULL,
    PRIMARY KEY (route_id, seq),
    UNIQUE (route_id, stop_id)
);

CREATE TABLE IF NOT EXISTS fleet.depot_zones (
    id   text PRIMARY KEY,                           -- e.g. 'DEPOT-CENTRAL'
    name text NOT NULL,
    geom geometry(Polygon, 4326) NOT NULL
);

CREATE TABLE IF NOT EXISTS fleet.route_corridors (
    id   text PRIMARY KEY,                           -- e.g. 'R12'
    name text NOT NULL,
    geom geometry(LineString, 4326) NOT NULL
);

-- Seed: the 8 stops / 3 routes currently hardcoded in
-- services/go/citizen-api/internal/handlers/passenger.go.
INSERT INTO fleet.stops (code, name, geom) VALUES
    ('S001', 'Central Station',   ST_SetSRID(ST_MakePoint(13.4050, 52.5200), 4326)),
    ('S002', 'Museum Island',     ST_SetSRID(ST_MakePoint(13.4010, 52.5169), 4326)),
    ('S003', 'City Hall',         ST_SetSRID(ST_MakePoint(13.4081, 52.5186), 4326)),
    ('S004', 'Riverside Depot',   ST_SetSRID(ST_MakePoint(13.4310, 52.5050), 4326)),
    ('S005', 'North H2 Hub',      ST_SetSRID(ST_MakePoint(13.3900, 52.5400), 4326)),
    ('S006', 'University Campus', ST_SetSRID(ST_MakePoint(13.3260, 52.5120), 4326)),
    ('S007', 'Market Square',     ST_SetSRID(ST_MakePoint(13.4180, 52.5155), 4326)),
    ('S008', 'Stadium Park',      ST_SetSRID(ST_MakePoint(13.3890, 52.4990), 4326))
ON CONFLICT (code) DO NOTHING;

INSERT INTO fleet.routes (code, short_name, long_name, headway_min) VALUES
    ('R10', '10', 'Central Station — North H2 Hub', 10),
    ('R21', '21', 'University — Riverside Depot',   15),
    ('R42', '42', 'Stadium Park — City Hall',       20)
ON CONFLICT (code) DO NOTHING;

INSERT INTO fleet.route_stops (route_id, stop_id, seq)
SELECT r.id, s.id, x.seq
FROM (VALUES
    ('R10', 'S001', 1), ('R10', 'S002', 2), ('R10', 'S003', 3), ('R10', 'S005', 4),
    ('R21', 'S006', 1), ('R21', 'S001', 2), ('R21', 'S007', 3), ('R21', 'S004', 4),
    ('R42', 'S008', 1), ('R42', 'S007', 2), ('R42', 'S003', 3), ('R42', 'S001', 4)
) AS x(route_code, stop_code, seq)
JOIN fleet.routes r ON r.code = x.route_code
JOIN fleet.stops  s ON s.code = x.stop_code
ON CONFLICT (route_id, seq) DO NOTHING;

-- Seed: depot zones / route corridors matching the geo_enrich synthetic
-- fallback (services/python/lakehouse-etl/jobs/geo_enrich.py), so the
-- lakehouse job joins against real reference data instead of its fallback.
INSERT INTO fleet.depot_zones (id, name, geom) VALUES
    ('DEPOT-CENTRAL', 'Central Depot', ST_Buffer(ST_SetSRID(ST_MakePoint(13.4050, 52.5200), 4326)::geography, 2200)::geometry),
    ('DEPOT-NORTH',   'North Depot',   ST_Buffer(ST_SetSRID(ST_MakePoint(13.3900, 52.5800), 4326)::geography, 2200)::geometry),
    ('DEPOT-SOUTH',   'South Depot',   ST_Buffer(ST_SetSRID(ST_MakePoint(13.4200, 52.4600), 4326)::geography, 2200)::geometry)
ON CONFLICT (id) DO NOTHING;

INSERT INTO fleet.route_corridors (id, name, geom) VALUES
    ('R12', 'Ring 12',        ST_GeomFromText('LINESTRING(13.30 52.52, 13.50 52.52)', 4326)),
    ('R45', 'North-South 45', ST_GeomFromText('LINESTRING(13.405 52.44, 13.405 52.60)', 4326)),
    ('R7',  'Diagonal 7',     ST_GeomFromText('LINESTRING(13.32 52.46, 13.49 52.58)', 4326))
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM fleet.route_corridors WHERE id IN ('R12', 'R45', 'R7');
DELETE FROM fleet.depot_zones WHERE id IN ('DEPOT-CENTRAL', 'DEPOT-NORTH', 'DEPOT-SOUTH');
DELETE FROM fleet.route_stops;
DELETE FROM fleet.routes WHERE code IN ('R10', 'R21', 'R42');
DELETE FROM fleet.stops WHERE code IN ('S001','S002','S003','S004','S005','S006','S007','S008');
DROP TABLE IF EXISTS fleet.route_corridors;
DROP TABLE IF EXISTS fleet.depot_zones;
DROP TABLE IF EXISTS fleet.route_stops;
DROP TABLE IF EXISTS fleet.routes;
DROP TABLE IF EXISTS fleet.stops;

ALTER TABLE infra.work_orders DROP COLUMN IF EXISTS started_at;
ALTER TABLE infra.work_orders DROP COLUMN IF EXISTS assignee;
ALTER TABLE infra.work_orders DROP COLUMN IF EXISTS prediction_id;
ALTER TABLE infra.work_orders DROP COLUMN IF EXISTS bus_id;

ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS refunded_at;
ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS refund_of;
ALTER TABLE commerce.trades DROP COLUMN IF EXISTS tb_transfer_id;

DROP TABLE IF EXISTS commerce.loyalty_redemptions;
DROP TABLE IF EXISTS commerce.loyalty_ledger;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'commerce' AND table_name = 'loyalty_accounts'
                 AND column_name = 'rider_sub') THEN
        ALTER TABLE commerce.loyalty_accounts RENAME COLUMN rider_sub TO user_sub;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE commerce.ad_placements DROP CONSTRAINT IF EXISTS ad_placements_no_overlap;
DROP TABLE IF EXISTS commerce.ad_placements;
DROP TABLE IF EXISTS commerce.ad_inventory;

DROP TABLE IF EXISTS infra.station_queue;

DROP INDEX IF EXISTS infra.dispatch_jobs_active_vehicle_uq;
DROP INDEX IF EXISTS infra.dispatch_jobs_active_driver_uq;
ALTER TABLE infra.dispatch_jobs DROP CONSTRAINT IF EXISTS dispatch_jobs_driver_fk;
ALTER TABLE infra.dispatch_jobs DROP CONSTRAINT IF EXISTS dispatch_jobs_vehicle_fk;
DROP TABLE IF EXISTS infra.drivers;
ALTER TABLE infra.dispatch_jobs DROP COLUMN IF EXISTS ends_at;

DROP INDEX IF EXISTS citizen.drt_requests_vehicle_idx;
DROP INDEX IF EXISTS citizen.drt_requests_user_idx;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS assigned_at;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS driver_sub;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS vehicle_id;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS passengers;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS dropoff_label;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS pickup_label;

DROP INDEX IF EXISTS citizen.carbon_credits_period_uq;

DROP INDEX IF EXISTS infra.incidents_incident_no_uq;
ALTER TABLE infra.incidents ALTER COLUMN incident_no DROP NOT NULL;
ALTER TABLE infra.incidents ALTER COLUMN incident_no DROP DEFAULT;
ALTER TABLE infra.incidents DROP COLUMN IF EXISTS incident_no;
DROP SEQUENCE IF EXISTS infra.incident_number_seq;

DROP TABLE IF EXISTS platform.onboarding_requests;
DROP TABLE IF EXISTS platform.audit_log;
-- =============================================================================
