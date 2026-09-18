-- =============================================================================
-- H2Fleet — 0009_wave6_gaps.sql (goose)
-- Wave-6 gap-audit fixes (docs/GAP_AUDIT.md):
--   G1  commerce.fare_products + commerce.rider_entitlements — passes,
--       concessions and free-travel entitlements (A2-01). CreatePayment
--       resolves the best active entitlement before the daily cap.
--   G2  commerce.fare_payments.refunded_minor — cumulative refunded amount
--       so partial refunds leave a refundable remainder (A2-02).
--   G3  fleet.vehicles.wheelchair_accessible + citizen.drt_requests
--       .requires_wheelchair — accessible DRT matching (A2-04).
--   G4  infra.webhook_subscriptions + infra.webhook_deliveries — outbound,
--       HMAC-signed partner notifications on the incident lifecycle (A3-03).
--   G5  infra.incidents.acknowledged_at — evidence-pack timeline (A4-04).
-- Everything is idempotent (IF NOT EXISTS), same contract as 0003–0008.
-- =============================================================================

-- +goose Up

-- G1 — fare product catalog (pass | discount | free).
CREATE TABLE IF NOT EXISTS commerce.fare_products (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code            text NOT NULL UNIQUE,          -- e.g. 'monthly-pass', 'student-50'
    kind            text NOT NULL CHECK (kind IN ('pass','discount','free')),
    discount_pct    integer CHECK (discount_pct BETWEEN 0 AND 100),  -- kind='discount' only
    price_minor     bigint NOT NULL DEFAULT 0 CHECK (price_minor >= 0),
    duration_days   integer NOT NULL CHECK (duration_days > 0),      -- entitlement validity on grant
    description     text NOT NULL DEFAULT '',
    active          boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- G1 — per-rider entitlements granted from products.
CREATE TABLE IF NOT EXISTS commerce.rider_entitlements (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rider_sub       text NOT NULL,
    product_id      uuid NOT NULL REFERENCES commerce.fare_products(id),
    valid_from      timestamptz NOT NULL DEFAULT now(),
    valid_to        timestamptz NOT NULL,
    created_by      text NOT NULL DEFAULT '',      -- operator sub who granted it
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (valid_to > valid_from)
);
CREATE INDEX IF NOT EXISTS rider_entitlements_rider_valid_to
    ON commerce.rider_entitlements (rider_sub, valid_to);

-- G2 — cumulative refunded amount (partial refunds; 0005 added refund_of /
-- refunded_at / 'refunded' status; this adds the running total so a
-- partially refunded payment stays refundable for the remainder).
ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS refunded_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE commerce.fare_payments DROP CONSTRAINT IF EXISTS fare_payments_refunded_minor_check;
ALTER TABLE commerce.fare_payments ADD CONSTRAINT fare_payments_refunded_minor_check
    CHECK (refunded_minor >= 0) NOT VALID;

-- G3 — accessibility attributes (accessible DRT matching).
ALTER TABLE fleet.vehicles ADD COLUMN IF NOT EXISTS wheelchair_accessible boolean NOT NULL DEFAULT false;
ALTER TABLE citizen.drt_requests ADD COLUMN IF NOT EXISTS requires_wheelchair boolean NOT NULL DEFAULT false;

-- G4 — outbound partner webhooks (incident lifecycle).
CREATE TABLE IF NOT EXISTS infra.webhook_subscriptions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    url             text NOT NULL,
    secret          text NOT NULL,                 -- HMAC-SHA256 key, injected via env-rendered seed/ops
    min_severity    text NOT NULL DEFAULT 'high' CHECK (min_severity IN ('low','medium','high','critical')),
    active          boolean NOT NULL DEFAULT true,
    description     text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS infra.webhook_deliveries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id uuid NOT NULL REFERENCES infra.webhook_subscriptions(id),
    incident_id     uuid,
    event_type      text NOT NULL,                 -- incident.opened|escalated|resolved
    payload         jsonb NOT NULL,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz
);
CREATE INDEX IF NOT EXISTS webhook_deliveries_pending
    ON infra.webhook_deliveries (created_at) WHERE status = 'pending';

-- G5 — incident acknowledgement timestamp (evidence-pack timeline).
ALTER TABLE infra.incidents ADD COLUMN IF NOT EXISTS acknowledged_at timestamptz;

-- +goose Down

ALTER TABLE infra.incidents DROP COLUMN IF EXISTS acknowledged_at;
DROP TABLE IF EXISTS infra.webhook_deliveries;
DROP TABLE IF EXISTS infra.webhook_subscriptions;
ALTER TABLE citizen.drt_requests DROP COLUMN IF EXISTS requires_wheelchair;
ALTER TABLE fleet.vehicles DROP COLUMN IF EXISTS wheelchair_accessible;
ALTER TABLE commerce.fare_payments DROP CONSTRAINT IF EXISTS fare_payments_refunded_minor_check;
ALTER TABLE commerce.fare_payments DROP COLUMN IF EXISTS refunded_minor;
DROP TABLE IF EXISTS commerce.rider_entitlements;
DROP TABLE IF EXISTS commerce.fare_products;