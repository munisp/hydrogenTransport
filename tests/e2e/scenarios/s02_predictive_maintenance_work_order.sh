#!/usr/bin/env bash
# S2 — Predictive maintenance flags a compressor -> operator opens a work
# order -> bus dispatched to depot.
# Actors: predictive-maintenance service, operator, dispatcher.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S2 predictive maintenance -> work order -> depot"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S2; exit 1; }

req GET /api/fleet/v1/maintenance/predictions "$OP"
expect_status 200 "GET /api/fleet/v1/maintenance/predictions"

# Highest-risk prediction (or fall back to a scored live prediction).
bus="$(req GET /api/fleet/v1/maintenance/predictions "$OP" | jq -r '([.. | objects | select(.risk_score != null)] | sort_by(.risk_score) | last | .bus_id) // empty')"
if [ -z "$bus" ]; then
  bus="$(req GET /api/fleet/v1/vehicles "$OP" | jq -r '([.. | objects | .id? // empty] | first) // empty')"
  info "no stored predictions; scoring bus $bus live"
  req POST /api/ml/v1/predict "$OP" "{\"bus_id\": \"$bus\"}"
  expect_status 200 "POST /api/ml/v1/predict (compressor risk score)"
fi
[ -n "$bus" ] || { bad "no bus available for scenario"; scenario_summary S2; exit 1; }
info "flagged bus: $bus"

wo="$(req POST /api/infra/v1/depot/work-orders "$OP" \
  "{\"title\": \"S2 compressor inspection\", \"description\": \"Predictive maintenance flagged compressor\", \"asset_ref\": \"$bus\"}")"
expect_status 201 "POST /api/infra/v1/depot/work-orders"
wo_id="$(echo "$wo" | jq -r '.id // empty')"
[ -n "$wo_id" ] && ok "work order created ($wo_id)" || bad "work order id missing"

DJ="$(driver_token)"
job="$(req POST /api/infra/v1/dispatch/jobs "$OP" \
  "{\"driver_sub\": \"${H2_DRIVER_USER:-driver}\", \"vehicle_id\": \"$bus\", \"route\": \"Riverside Depot\"}")"
expect_status 201 "POST /api/infra/v1/dispatch/jobs (bus to depot)"
job_id="$(echo "$job" | jq -r '.id // empty')"

if [ -n "$job_id" ] && [ -n "$DJ" ]; then
  req POST "/api/infra/v1/dispatch/jobs/$job_id/accept" "$DJ"
  expect_status 200 "driver accepts depot dispatch"
fi

scenario_summary S2
