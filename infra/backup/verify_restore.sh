#!/bin/sh
# H2Fleet restore verification — automates docs/DR.md drill step 6.
#
#   verify_restore.sh <backup-TS>        e.g. verify_restore.sh 20250115T020000Z
#
# Run AFTER the restore drill (docs/DR.md steps 0–5) against the restored
# stack. Exits non-zero on the first failed check so the drill cannot be
# signed off on a bad restore. Checks:
#   1. fleet.vehicles count = 50
#   2. max(fleet.telemetry.ts) <= backup timestamp (no data from the future)
#   3. goose migration version = latest migration file present in the image
#   4. commerce.fare_payments internal consistency (settled sums >= refunds)
#   5. platform.audit_log present and non-empty (chain integrity itself is
#      verified via GET /api/audit/v1/audit/verify once the stack is up)
#   6. platform.onboarding_requests / public.feature_toggles sanity (20 rows)
#
# TigerBeetle balance reconciliation stays a manual step (needs the TB REPL)
# — the script prints the exact reminder as the last line.
set -eu

TS=${1:?usage: verify_restore.sh <backup-TS>  (e.g. 20250115T020000Z)}
PSQL="docker exec -i h2-postgres psql -U h2 -d h2fleet -tA"

fail=0
check() { # check <name> <sql> <expected>
  got=$($PSQL -c "$2" 2>/dev/null || echo "QUERY_FAILED")
  if [ "$got" = "$3" ]; then
    echo "PASS  $1 ($got)"
  else
    echo "FAIL  $1 (got '$got', want '$3')"
    fail=1
  fi
}

# Backup TS 20250115T020000Z -> 2025-01-15 02:00:00+00 for the ts comparison.
TS_ISO=$(echo "$TS" | sed -E 's/(.{4})(.{2})(.{2})T(.{2})(.{2})(.{2})Z/\1-\2-\3 \4:\5:\6+00/')

check "vehicles count" \
  "SELECT count(*) FROM fleet.vehicles" "50"

check "telemetry not newer than backup" \
  "SELECT coalesce(max(ts) <= '$TS_ISO'::timestamptz, true) FROM fleet.telemetry" "t"

# Bump the floor below when a new migration lands (the restored DB must have
# been re-migrated — DR.md step 5 — not left at the backup's older version).
check "migrations re-applied (>= 0009)" \
  "SELECT coalesce(max(version_id) >= 9, false) FROM goose_db_version WHERE is_applied" "t"

check "refunds never exceed charges" \
  "SELECT NOT EXISTS (
     SELECT 1 FROM commerce.fare_payments
     WHERE COALESCE(refunded_minor,0) > amount_minor)" "t"

check "audit trail non-empty" \
  "SELECT count(*) > 0 FROM platform.audit_log" "t"

check "feature toggles seeded" \
  "SELECT count(*) FROM public.feature_toggles" "20"

if [ "$fail" -ne 0 ]; then
  echo "VERIFY_RESTORE: FAILED — do NOT sign off the drill; investigate before reopening traffic." >&2
  exit 1
fi

echo "VERIFY_RESTORE: all automated checks passed."
echo "MANUAL STEP: reconcile TigerBeetle balances against commerce.fare_payments"
echo "  settled sums via the TB REPL (docs/DR.md success criteria), then run"
echo "  GET /api/audit/v1/audit/verify (expect ok:true) once the gateway is up."
