-- =============================================================================
-- H2Fleet — 0011_wave8_onboarding.sql (goose)
-- Wave-8 onboarding hardening (plan-wave8.md). The onboarding_requests table
-- is owned at runtime by admin-api's EnsureSchema; this migration is the
-- canonical schema and upgrades existing deployments idempotently:
--   W8-1 pending-dedup backstop: partial UNIQUE (persona,email) WHERE pending
--   W8-2 email index for the per-email velocity count
--   W8-7 'expired' status for pending-TTL expiry
-- Everything is idempotent, same contract as 0003–0010.
-- =============================================================================

-- +goose Up

CREATE SCHEMA IF NOT EXISTS platform;

-- Fresh databases get the final shape directly (superset of the pre-Wave-8
-- EnsureSchema definition: named status CHECK incl. 'expired').
CREATE TABLE IF NOT EXISTS platform.onboarding_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    persona      text NOT NULL,
    email        text NOT NULL,
    display_name text NOT NULL,
    org          text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'pending',
    keycloak_sub text NOT NULL DEFAULT '',
    meta         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   text NOT NULL DEFAULT ''
);

-- W8-7 — status domain gains 'expired' (pending requests outlive the TTL).
-- The pre-Wave-8 constraint was an inline CHECK (auto-named); drop whatever
-- status CHECK exists and re-add it named, so re-runs are no-ops.
DO $$
DECLARE
    cname text;
BEGIN
    SELECT conname INTO cname
    FROM pg_constraint
    WHERE conrelid = 'platform.onboarding_requests'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%status%';
    IF cname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE platform.onboarding_requests DROP CONSTRAINT %I', cname);
    END IF;
END $$;
ALTER TABLE platform.onboarding_requests
    ADD CONSTRAINT onboarding_requests_status_check
    CHECK (status IN ('pending','approved','rejected','completed','expired'));

-- W8-1 — at most ONE pending request per (persona, email). The app dedups
-- before insert; this index is the race-safe backstop (23505 → re-read).
CREATE UNIQUE INDEX IF NOT EXISTS onboarding_requests_pending_uq
    ON platform.onboarding_requests (persona, email) WHERE status = 'pending';

-- W8-2 — per-email velocity count over the last 24 h.
CREATE INDEX IF NOT EXISTS onboarding_requests_email_created_idx
    ON platform.onboarding_requests (email, created_at DESC);

CREATE INDEX IF NOT EXISTS onboarding_requests_status_idx  ON platform.onboarding_requests (status);
CREATE INDEX IF NOT EXISTS onboarding_requests_persona_idx ON platform.onboarding_requests (persona);

-- +goose Down

DROP INDEX IF EXISTS platform.onboarding_requests_email_created_idx;
DROP INDEX IF EXISTS platform.onboarding_requests_pending_uq;

DO $$
DECLARE
    cname text;
BEGIN
    SELECT conname INTO cname
    FROM pg_constraint
    WHERE conrelid = 'platform.onboarding_requests'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%status%';
    IF cname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE platform.onboarding_requests DROP CONSTRAINT %I', cname);
    END IF;
END $$;
ALTER TABLE platform.onboarding_requests
    ADD CONSTRAINT onboarding_requests_status_check
    CHECK (status IN ('pending','approved','rejected','completed'));
