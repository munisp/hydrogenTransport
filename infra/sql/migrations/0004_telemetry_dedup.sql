-- =============================================================================
-- H2Fleet — 0004_telemetry_dedup.sql (goose)
-- Telemetry dedup + lifecycle management for the fleet.telemetry hypertable:
--   * UNIQUE(bus_id, ts) — Timescale-safe because it includes the partition
--     column `ts`; telemetry-ingest inserts use ON CONFLICT DO NOTHING, so
--     at-least-once redeliveries and intra-batch duplicates are idempotent.
--   * retention policy: drop chunks older than 90 days.
--   * compression (segment by bus, order by ts) + policy: compress chunks
--     older than 7 days.
-- =============================================================================

-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS telemetry_bus_ts_uq ON fleet.telemetry (bus_id, ts);

SELECT add_retention_policy('fleet.telemetry', INTERVAL '90 days', if_not_exists => TRUE);

ALTER TABLE fleet.telemetry SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'bus_id',
    timescaledb.compress_orderby = 'ts DESC'
);
SELECT add_compression_policy('fleet.telemetry', INTERVAL '7 days', if_not_exists => TRUE);

-- +goose Down
SELECT remove_compression_policy('fleet.telemetry', if_exists => TRUE);
ALTER TABLE fleet.telemetry SET (timescaledb.compress = false);
SELECT remove_retention_policy('fleet.telemetry', if_exists => TRUE);
DROP INDEX IF EXISTS fleet.telemetry_bus_ts_uq;
