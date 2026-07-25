#!/bin/sh
# =============================================================================
# 004_replication.sh — runs ONCE at first initdb of the primary (fresh pg_data
# volume only, via /docker-entrypoint-initdb.d). Enables streaming replication:
#   * wal_level=replica, enough WAL senders/slots, hot_standby
#   * pg_hba entry for the replicator role
#   * replicator role + a persistent replication slot for postgres-replica
#
# On an EXISTING volume this script does not run; apply the same settings with
# ALTER SYSTEM + a restart instead (docs/RUNBOOK.md §Postgres replica).
# Idempotent by construction: initdb.d runs only on empty PGDATA.
# =============================================================================
set -eu

PGDATA="${PGDATA:-/home/postgres/pgdata/data}"
REPLICATOR_PASSWORD="${REPLICATOR_PASSWORD:?set_REPLICATOR_PASSWORD_in_.env}"

# --- 1. WAL/replication settings (postmaster context → applied when the -----
# --- final server starts after the init phase) -------------------------------
cat >> "$PGDATA/postgresql.conf" <<'EOF'

# --- h2fleet prod overlay: streaming replication -----------------------------
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
hot_standby_feedback = on
wal_keep_size = 1GB
synchronous_commit = on          # async replica; flip to 'remote_apply' with
                                 # synchronous_standby_names for zero-RPO
EOF

# --- 2. pg_hba: allow replication connections from the overlay network -------
echo "host replication replicator 0.0.0.0/0 scram-sha-256" >> "$PGDATA/pg_hba.conf"

# --- 3. replicator role + persistent slot (temp server is live during init) --
# psql here talks to the temporary init server over the local socket.
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'replicator') THEN
    CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD '${REPLICATOR_PASSWORD}';
  END IF;
END
\$\$;
SELECT pg_create_physical_replication_slot('h2_replica_slot', true, false)
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = 'h2_replica_slot');
SQL

echo "004_replication.sh: replication configured (slot h2_replica_slot)"
