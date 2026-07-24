#!/bin/sh
# H2Fleet backup runner.
#
#   backup.sh loop   (default) run a backup every BACKUP_INTERVAL_SECONDS
#   backup.sh once   run a single backup and exit (used by `make backup`)
#
# What gets backed up to s3://$BACKUP_BUCKET/ on MinIO:
#   postgres/     pg_dump -Fc of the main h2fleet database
#   temporal/     pg_dump -Fc of the temporal database (postgres-temporal)
#   tigerbeetle/  crash-consistent copy of the 0_0.tigerbeetle data file
#
# TigerBeetle note: TB is a single-writer append-only store; a plain file copy
# of the data file is crash-consistent (it may contain un-checkpointed tail,
# which TB recovery handles on start). For a guaranteed quiescent snapshot,
# stop the tigerbeetle container first — see docs/DR.md.
#
# Required env: POSTGRES_PASSWORD, TEMPORAL_POSTGRES_PASSWORD,
#               MINIO_ROOT_USER, MINIO_ROOT_PASSWORD
# Optional env: BACKUP_BUCKET (h2-backups), BACKUP_INTERVAL_SECONDS (86400),
#               BACKUP_RETENTION_DAYS (14)
set -eu

BACKUP_BUCKET=${BACKUP_BUCKET:-h2-backups}
BACKUP_INTERVAL_SECONDS=${BACKUP_INTERVAL_SECONDS:-86400}
BACKUP_RETENTION_DAYS=${BACKUP_RETENTION_DAYS:-14}
MINIO_ALIAS=dst
TB_FILE=/tigerbeetle/0_0.tigerbeetle

log() { echo "[backup $(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

run_backup() {
  TS=$(date -u +%Y%m%dT%H%M%SZ)
  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT

  mc alias set "$MINIO_ALIAS" "http://minio:9000" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
  mc mb -p "$MINIO_ALIAS/$BACKUP_BUCKET" >/dev/null 2>&1 || true

  log "pg_dump h2fleet -> $BACKUP_BUCKET/postgres/h2fleet_$TS.dump"
  PGPASSWORD=$POSTGRES_PASSWORD pg_dump \
    -h postgres -U h2 -d h2fleet -Fc -f "$WORK/h2fleet_$TS.dump"
  mc cp "$WORK/h2fleet_$TS.dump" "$MINIO_ALIAS/$BACKUP_BUCKET/postgres/"

  log "pg_dump temporal -> $BACKUP_BUCKET/temporal/temporal_$TS.dump"
  PGPASSWORD=$TEMPORAL_POSTGRES_PASSWORD pg_dump \
    -h postgres-temporal -U temporal -d temporal -Fc -f "$WORK/temporal_$TS.dump"
  mc cp "$WORK/temporal_$TS.dump" "$MINIO_ALIAS/$BACKUP_BUCKET/temporal/"

  if [ -f "$TB_FILE" ]; then
    log "tigerbeetle snapshot -> $BACKUP_BUCKET/tigerbeetle/0_0_$TS.tigerbeetle"
    cp "$TB_FILE" "$WORK/0_0_$TS.tigerbeetle"
    mc cp "$WORK/0_0_$TS.tigerbeetle" "$MINIO_ALIAS/$BACKUP_BUCKET/tigerbeetle/"
  else
    log "WARN: tigerbeetle data file $TB_FILE not found; skipping TB snapshot"
  fi

  log "pruning objects older than ${BACKUP_RETENTION_DAYS}d"
  mc rm --recursive --force --older-than "${BACKUP_RETENTION_DAYS}d" \
    "$MINIO_ALIAS/$BACKUP_BUCKET/" >/dev/null 2>&1 || true

  rm -rf "$WORK"
  trap - EXIT
  log "backup $TS complete"
}

case "${1:-loop}" in
  once)
    run_backup
    ;;
  loop)
    log "starting backup loop (interval ${BACKUP_INTERVAL_SECONDS}s, bucket $BACKUP_BUCKET)"
    while :; do
      run_backup || log "ERROR: backup iteration failed (will retry next interval)"
      sleep "$BACKUP_INTERVAL_SECONDS"
    done
    ;;
  *)
    echo "usage: backup.sh [loop|once]" >&2
    exit 2
    ;;
esac
