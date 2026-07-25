#!/usr/bin/env bash
# S1 — Morning rush telemetry surge (50 buses x 5s).
# Actor: telemetry-simulator (machine) + NOC operator observing.
# Expect: fleet-api healthy; latest telemetry covers ~all 50 buses and keeps
# refreshing on the 5s cadence (two samples show movement).
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S1 morning rush telemetry surge (gateway=$GATEWAY_URL)"

req GET /api/fleet/healthz; expect_status 200 "fleet-api healthy through gateway"

OP="$(operator_token)"; [ -n "$OP" ] || info "no operator token; telemetry/latest is public, continuing unauthenticated"
TOK="${OP:--}"

first="$(req GET /api/fleet/v1/telemetry/latest "$TOK")"
expect_status 200 "GET /api/fleet/v1/telemetry/latest"
buses="$(echo "$first" | jq '[.. | .bus_id? // empty] | unique | length')"
info "buses reporting in latest snapshot: $buses"
[ "${buses:-0}" -ge 40 ] && ok "surge coverage: >=40 of 50 buses reporting" || bad "only $buses buses reporting"

sleep 6
second="$(req GET /api/fleet/v1/telemetry/latest "$TOK")"
expect_status 200 "second sample after 5s cadence"
if [ "$(echo "$first" | jq -cS '[.. | .ts? // empty] | sort | .[-5:]')" != \
     "$(echo "$second" | jq -cS '[.. | .ts? // empty] | sort | .[-5:]')" ]; then
  ok "telemetry stream advancing on the 5s cadence"
else
  bad "telemetry timestamps did not advance (simulator not running?)"
fi

req GET /api/twin/v1/twin "$TOK"; expect_status 200 "digital-twin hot state readable"

scenario_summary S1
