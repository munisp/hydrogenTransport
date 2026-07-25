-- =============================================================================
-- H2Fleet — 0006_trades_idempotency.sql (goose)
-- Absorbs commerce-api's runtime EnsureSchema DDL for energy-trade
-- idempotency (services/go/commerce-api/internal/handlers/common.go) into a
-- versioned migration:
--   * commerce.trades.idempotency_key — client-chosen Idempotency-Key header
--     value for POST /v1/energy/trades (same precedent as
--     commerce.fare_payments.idempotency_key from 0003);
--   * partial unique index so retries replay the original trade instead of
--     double-settling (NULL keys — e.g. seeded rows — stay unrestricted).
-- Idempotent (IF NOT EXISTS), so it coexists with the runtime EnsureSchema
-- during mixed-version rollouts, same contract as 0003/0005.
-- =============================================================================

-- +goose Up

ALTER TABLE commerce.trades ADD COLUMN IF NOT EXISTS idempotency_key text;

CREATE UNIQUE INDEX IF NOT EXISTS trades_idempotency_key_uq
    ON commerce.trades (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS trades_idempotency_key_uq;
ALTER TABLE commerce.trades DROP COLUMN IF EXISTS idempotency_key;
-- =============================================================================
