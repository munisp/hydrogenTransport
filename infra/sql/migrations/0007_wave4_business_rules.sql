-- =============================================================================
-- H2Fleet — 0007_wave4_business_rules.sql (goose)
-- Wave-4 business-rule completion (docs/BUSINESS_LOGIC_AUDIT.md re-verification):
--   W1  fleet.fuel_consumption — per-bus learned H2 consumption (kg/100km),
--       maintained by the fuel-monitoring pipeline (telemetry-ingest produces
--       fuel.reading; the consumer updates this table). fleet-api range math
--       reads it instead of one fleet-wide constant.
--   W2  commerce.ad_placements.cost_minor — per-placement spend so campaign
--       budgets are tracked against committed placement cost
--       (sum(cost_minor) <= ad_campaigns.budget_minor enforced in commerce-api).
--   W3  infra.work_orders open-prediction dedup: at most one OPEN work order
--       per maintenance prediction, so the maintenance.predicted consumer can
--       retry/replay without spawning duplicates.
-- Everything is idempotent (IF NOT EXISTS), same contract as 0003/0005/0006.
-- =============================================================================

-- +goose Up

-- W1 — per-bus learned consumption (fuel-monitoring).
CREATE TABLE IF NOT EXISTS fleet.fuel_consumption (
    bus_id        uuid PRIMARY KEY REFERENCES fleet.vehicles(id),
    kg_per_100km  float8 NOT NULL,              -- learned running average
    sample_km     float8 NOT NULL DEFAULT 0,    -- distance behind the estimate
    samples       integer NOT NULL DEFAULT 0,   -- reading pairs behind the estimate
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- W2 — placement spend (advertising budget tracking).
ALTER TABLE commerce.ad_placements ADD COLUMN IF NOT EXISTS cost_minor bigint NOT NULL DEFAULT 0;

-- W2b — fare capping: the amount actually charged after the daily cap
-- (amount_minor stays the requested fare; NULL = pre-capping row).
ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS charged_minor bigint;

-- W3b — queue completion timestamp for service-time (wait estimate) stats.
ALTER TABLE infra.station_queue ADD COLUMN IF NOT EXISTS completed_at timestamptz;

-- W3c — incident resolution timestamp for compliance MTTR reporting.
ALTER TABLE infra.incidents ADD COLUMN IF NOT EXISTS resolved_at timestamptz;

-- W3 — one open work order per prediction (predictive-maintenance closed loop).
CREATE UNIQUE INDEX IF NOT EXISTS work_orders_open_prediction_uq
    ON infra.work_orders (prediction_id)
    WHERE prediction_id IS NOT NULL AND status <> 'closed';

-- +goose Down

DROP INDEX IF EXISTS infra.work_orders_open_prediction_uq;
ALTER TABLE infra.station_queue DROP COLUMN IF EXISTS completed_at;
ALTER TABLE infra.incidents DROP COLUMN IF EXISTS resolved_at;
ALTER TABLE commerce.ad_placements DROP COLUMN IF EXISTS cost_minor;
ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS charged_minor;
DROP TABLE IF EXISTS fleet.fuel_consumption;
-- =============================================================================
