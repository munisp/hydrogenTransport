#!/bin/sh
# =============================================================================
# replica-entrypoint.sh — postgres-replica bootstrap.
#
# 1. Wait for the primary to accept connections.
# 2. If PGDATA is empty, take a base backup with -R (writes standby.signal +
#    primary_conninfo) bound to the persistent replication slot
#    h2_replica_slot (created by 004_replication.sh on the primary).
# 3. Hand off to the stock image entrypoint.
#
# Re-runs are safe: an already-initialized PGDATA skips the base backup.
# =============================================================================
set -eu

PRIMARY_HOST="${PRIMARY_HOST:-postgres}"
PRIMARY_PORT="${PRIMARY_PORT:-5432}"
PGDATA="${PGDATA:-/home/postgres/pgdata/data}"
SLOT="${REPLICATION_SLOT:-h2_replica_slot}"
export PGPASSWORD="${REPLICATOR_PASSWORD:?set_REPLICATOR_PASSWORD_in_.env}"

echo "replica: waiting for primary ${PRIMARY_HOST}:${PRIMARY_PORT}"
until pg_isready -h "$PRIMARY_HOST" -p "$PRIMARY_PORT" -U replicator >/dev/null 2>&1; do
  sleep 2
done

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "replica: empty PGDATA, taking base backup (slot: $SLOT)"
  # Ensure the slot exists (004_replication.sh only runs on FRESH primary
  # volumes; on pre-existing primaries create it here idempotently via the
  # admin role).
  PGPASSWORD="${POSTGRES_PASSWORD}" psql \
    "host=$PRIMARY_HOST port=$PRIMARY_PORT user=$POSTGRES_USER dbname=$POSTGRES_DB" <<'SQL' \
    || echo "replica: slot pre-creation failed; pg_basebackup will use the existing slot"
SELECT pg_create_physical_replication_slot('h2_replica_slot', true, false)
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = 'h2_replica_slot');
SQL

  mkdir -p "$PGDATA"
  chmod 700 "$PGDATA"
  pg_basebackup \
    --host="$PRIMARY_HOST" --port="$PRIMARY_PORT" \
    --username=replicator \
    --pgdata="$PGDATA" \
    --slot="$SLOT" \
    --write-recovery-conf \
    --wal-method=stream \
    --progress
  echo "replica: base backup complete"
else
  echo "replica: existing PGDATA found, resuming streaming"
fi

# Hand off to the stock image entrypoint (timescaledb-ha keeps the postgres
# docker entrypoint contract).
if [ -x /docker-entrypoint.sh ]; then
  exec /docker-entrypoint.sh postgres
fi
exec postgres
