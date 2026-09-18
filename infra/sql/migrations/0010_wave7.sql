-- =============================================================================
-- H2Fleet — 0010_wave7.sql (goose)
-- Wave-7 builds the two operator-valuable DECISION items from the Wave-6
-- gap audit (docs/GAP_AUDIT.md, plan-wave7.md):
--   W7-1 (A2-06) corporate/employer group settlement:
--     commerce.billing_accounts + commerce.billing_charges +
--     commerce.invoices + rider_entitlements.payer_account. A payer-backed
--     entitlement covers the ride for the rider; the covered amount accrues
--     to the corporate billing account and is settled by periodic invoice.
--   W7-2 (A2-09) charter/school block bookings:
--     infra.charter_bookings + infra.charter_vehicles — a transactional
--     multi-vehicle block reservation backed by dispatch jobs
--     (route='charter:<reference>'), so the existing overlap engine and
--     partial unique indexes enforce exclusivity.
-- Everything is idempotent (IF NOT EXISTS), same contract as 0003–0009.
-- =============================================================================

-- +goose Up

-- W7-1 — corporate payer accounts. ledger_account_id is the TigerBeetle
-- clearing account (5xxx, allocated sequentially from 5001 by the service).
CREATE TABLE IF NOT EXISTS commerce.billing_accounts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('corporate','school','agency','municipal')),
    contact_email     text NOT NULL DEFAULT '',
    ledger_account_id bigint NOT NULL UNIQUE,
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','closed')),
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- W7-1 — link an entitlement to its corporate payer (null = city/rider funded).
ALTER TABLE commerce.rider_entitlements ADD COLUMN IF NOT EXISTS payer_account uuid;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rider_entitlements_payer_account_fk') THEN
        ALTER TABLE commerce.rider_entitlements
            ADD CONSTRAINT rider_entitlements_payer_account_fk
            FOREIGN KEY (payer_account) REFERENCES commerce.billing_accounts(id) NOT VALID;
    END IF;
END $$;

-- W7-1 — per-ride accrual against a billing account. The covered amount
-- (requested fare minus what the rider was charged) is recorded in the SAME
-- transaction as the payment — a settled corporate ride never exists
-- without its accrual. invoice_id is null until swept into an invoice.
CREATE TABLE IF NOT EXISTS commerce.billing_charges (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    billing_account_id uuid NOT NULL REFERENCES commerce.billing_accounts(id),
    payment_id         uuid NOT NULL UNIQUE,          -- one accrual per payment
    entitlement_id     uuid NOT NULL REFERENCES commerce.rider_entitlements(id),
    amount_minor       bigint NOT NULL CHECK (amount_minor > 0),
    invoice_id         uuid,                          -- null = uninvoiced
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS billing_charges_uninvoiced
    ON commerce.billing_charges (billing_account_id, created_at) WHERE invoice_id IS NULL;

-- W7-1 — periodic settlement artifact. UNIQUE(account, period) makes
-- generation idempotent; 'issued' → 'paid' posts the clearing transfer.
CREATE TABLE IF NOT EXISTS commerce.invoices (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    billing_account_id uuid NOT NULL REFERENCES commerce.billing_accounts(id),
    period_start       timestamptz NOT NULL,
    period_end         timestamptz NOT NULL,
    amount_minor       bigint NOT NULL CHECK (amount_minor > 0),
    charge_count       integer NOT NULL CHECK (charge_count > 0),
    status             text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued','paid','void')),
    tb_transfer_id     text,
    issued_at          timestamptz NOT NULL DEFAULT now(),
    paid_at            timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (billing_account_id, period_start, period_end),
    CHECK (period_end > period_start)
);

-- W7-2 — charter/school block booking header.
CREATE TABLE IF NOT EXISTS infra.charter_bookings (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reference     text NOT NULL UNIQUE,             -- customer-facing booking ref
    customer_name text NOT NULL,
    contact       text NOT NULL DEFAULT '',
    starts_at     timestamptz NOT NULL,
    ends_at       timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed','cancelled','completed')),
    notes         text NOT NULL DEFAULT '',
    created_by    text NOT NULL DEFAULT '',          -- operator sub
    created_at    timestamptz NOT NULL DEFAULT now(),
    cancelled_at  timestamptz,
    CHECK (ends_at > starts_at)
);

-- W7-2 — vehicles blocked by a booking; each row backs exactly one
-- dispatch job (route='charter:<reference>'), so the dispatch overlap
-- engine, the DRT dispatch-busy guard and the partial unique vehicle index
-- all treat charter vehicles as booked without any special-casing.
CREATE TABLE IF NOT EXISTS infra.charter_vehicles (
    booking_id      uuid NOT NULL REFERENCES infra.charter_bookings(id) ON DELETE CASCADE,
    vehicle_id      uuid NOT NULL,
    dispatch_job_id uuid NOT NULL UNIQUE,
    PRIMARY KEY (booking_id, vehicle_id)
);

-- +goose Down

DROP TABLE IF EXISTS infra.charter_vehicles;
DROP TABLE IF EXISTS infra.charter_bookings;
DROP TABLE IF EXISTS commerce.invoices;
DROP TABLE IF EXISTS commerce.billing_charges;
ALTER TABLE commerce.rider_entitlements DROP CONSTRAINT IF EXISTS rider_entitlements_payer_account_fk;
ALTER TABLE commerce.rider_entitlements DROP COLUMN IF EXISTS payer_account;
DROP TABLE IF EXISTS commerce.billing_accounts;
