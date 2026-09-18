-- =============================================================================
-- H2Fleet — 0012_wave9_onboarding.sql (goose)
-- Wave-9 stakeholder-onboarding audit (plan-wave9.md), W9-2: email identity
-- is case-insensitive. Before this migration the intake dedup replay, the
-- per-email velocity cap and the 0011 pending-dedup unique index all matched
-- email byte-for-byte, so `Victim@Example.com` vs `victim@example.com`
-- bypassed every one of them.
--
--   1. Backfill: stored emails lowercased (handler lowercases at validate;
--      this heals legacy rows).
--   2. Rebuild the 0011 indexes as expression indexes on lower(email) so the
--      database itself enforces case-insensitive uniqueness even for writers
--      that bypass the handler.
--
-- +goose Up
-- +goose StatementBegin
-- -----------------------------------------------------------------------------

UPDATE platform.onboarding_requests SET email = lower(email) WHERE email <> lower(email);

DROP INDEX IF EXISTS platform.onboarding_requests_pending_uq;
CREATE UNIQUE INDEX IF NOT EXISTS onboarding_requests_pending_uq
    ON platform.onboarding_requests (persona, lower(email)) WHERE status = 'pending';

DROP INDEX IF EXISTS platform.onboarding_requests_email_created_idx;
CREATE INDEX IF NOT EXISTS onboarding_requests_email_created_idx
    ON platform.onboarding_requests (lower(email), created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS platform.onboarding_requests_pending_uq;
CREATE UNIQUE INDEX IF NOT EXISTS onboarding_requests_pending_uq
    ON platform.onboarding_requests (persona, email) WHERE status = 'pending';

DROP INDEX IF EXISTS platform.onboarding_requests_email_created_idx;
CREATE INDEX IF NOT EXISTS onboarding_requests_email_created_idx
    ON platform.onboarding_requests (email, created_at DESC);
-- (the lowercased emails are not restored — case-folded form is canonical)
-- +goose StatementEnd
