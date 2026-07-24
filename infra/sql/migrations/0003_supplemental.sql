-- =============================================================================
-- H2Fleet — 0003_supplemental.sql (goose)
-- Supplemental service-owned tables/columns. Moves the DDL that commerce-api
-- and infra-api previously applied at runtime (EnsureSchema in
-- services/go/commerce-api/internal/handlers/common.go and
-- services/go/infra-api/internal/handlers/common.go) into a versioned
-- migration. The Go EnsureSchema calls remain idempotent (IF NOT EXISTS), so
-- old and new codepaths coexist safely during rollout.
-- Also includes commerce.rider_accounts (fare wallet ↔ TigerBeetle account).
-- =============================================================================

-- +goose Up
-- ------------------------------------------------------- commerce domain ----
ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS idempotency_key text;
ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS tb_transfer_id text;
CREATE UNIQUE INDEX IF NOT EXISTS fare_payments_idempotency_key_uq
    ON commerce.fare_payments (idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS commerce.loyalty_accounts (
    user_sub   text PRIMARY KEY,
    points     integer NOT NULL DEFAULT 0 CHECK (points >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS commerce.marketplace_offers (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    partner     text NOT NULL DEFAULT '',
    cost_points integer NOT NULL CHECK (cost_points > 0),
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS commerce.ad_campaigns (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    advertiser   text NOT NULL DEFAULT '',
    budget_minor bigint NOT NULL DEFAULT 0,
    status       text NOT NULL DEFAULT 'draft',
    starts_at    timestamptz,
    ends_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Rider wallet ↔ TigerBeetle account mapping (RIDER_WALLET accounts = 1xxx).
CREATE TABLE IF NOT EXISTS commerce.rider_accounts (
    rider_sub  text PRIMARY KEY,
    account_id bigint UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------- infra domain ----
CREATE TABLE IF NOT EXISTS infra.compliance_reports (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    generated_at timestamptz NOT NULL DEFAULT now(),
    report       jsonb NOT NULL
);

CREATE TABLE IF NOT EXISTS infra.work_orders (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    asset_ref   text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'open',
    opened_at   timestamptz NOT NULL DEFAULT now(),
    closed_at   timestamptz
);

CREATE TABLE IF NOT EXISTS infra.dispatch_jobs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_sub text NOT NULL,
    vehicle_id uuid,
    route      text NOT NULL DEFAULT '',
    starts_at  timestamptz,
    status     text NOT NULL DEFAULT 'assigned',
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE infra.dispatch_jobs ADD COLUMN IF NOT EXISTS accepted_at timestamptz;

CREATE TABLE IF NOT EXISTS infra.depot_bays (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    depot       text NOT NULL,
    label       text NOT NULL,
    kind        text NOT NULL DEFAULT 'parking',
    occupied_by uuid,
    status      text NOT NULL DEFAULT 'free',
    UNIQUE (depot, label)
);

INSERT INTO infra.depot_bays (depot, label, kind, status) VALUES
    ('Riverside Depot', 'F-01', 'fueling', 'free'),
    ('Riverside Depot', 'F-02', 'fueling', 'free'),
    ('Riverside Depot', 'C-01', 'charging', 'free'),
    ('Riverside Depot', 'P-01', 'parking', 'free'),
    ('Riverside Depot', 'P-02', 'parking', 'occupied'),
    ('Riverside Depot', 'W-01', 'workshop', 'out_of_service')
ON CONFLICT (depot, label) DO NOTHING;

-- +goose Down
DELETE FROM infra.depot_bays WHERE depot = 'Riverside Depot';
DROP TABLE IF EXISTS infra.depot_bays;
ALTER TABLE infra.dispatch_jobs DROP COLUMN IF EXISTS accepted_at;
DROP TABLE IF EXISTS infra.dispatch_jobs;
DROP TABLE IF EXISTS infra.work_orders;
DROP TABLE IF EXISTS infra.compliance_reports;

DROP TABLE IF EXISTS commerce.rider_accounts;
DROP TABLE IF EXISTS commerce.ad_campaigns;
DROP TABLE IF EXISTS commerce.marketplace_offers;
DROP TABLE IF EXISTS commerce.loyalty_accounts;
DROP INDEX IF EXISTS commerce.fare_payments_idempotency_key_uq;
ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS tb_transfer_id;
ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS idempotency_key;
