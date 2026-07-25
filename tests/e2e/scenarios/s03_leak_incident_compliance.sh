#!/usr/bin/env bash
# S3 — H2 leak detected at a station -> incident -> escalation after timeout
# -> compliance report includes it.
# Actors: leak sensor (machine token), on-call operator, compliance officer.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

ESCALATE_AFTER="${S3_ESCALATE_AFTER:-10}"   # seconds an open leak may sit before escalation

echo "== S3 leak -> incident -> escalation -> compliance report"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S3; exit 1; }

station="$(req GET /api/infra/v1/stations "$OP" | jq -r '([.. | objects | .id? // empty] | first) // empty')"

# Sensor webhook: shared LEAK token, not a user JWT (dedicated gateway route).
leak="$(curl -s -w '\n%{http_code}' -X POST "$GATEWAY_URL/api/infra/v1/safety/leak" \
  -H 'Content-Type: application/json' -H "X-Sensor-Token: $LEAK_INGEST_TOKEN" \
  -d "{\"sensor_id\": \"s3-scenario-sensor\", \"station_id\": \"${station:-00000000-0000-0000-0000-000000000000}\", \"severity\": \"high\", \"h2_ppm\": 950}")"
HTTP_STATUS="$(echo "$leak" | tail -1)"
echo "$leak" | head -n -1 > /tmp/h2scenario_body
expect_status 201 "POST /api/infra/v1/safety/leak (sensor webhook)"
incident_id="$(jq -r '.incident_id // .id // empty' /tmp/h2scenario_body)"

# Incident visible to ops.
if [ -n "$incident_id" ]; then
  wait_json GET "/api/infra/v1/incidents" "$OP" \
    "[.. | objects | select(.id == \"$incident_id\")] | length == 1" \
    "incident $incident_id listed for ops" || true
else
  wait_json GET "/api/infra/v1/incidents" "$OP" \
    '[.. | objects | select(.type == "leak")] | length > 0' \
    "a leak incident is listed for ops" || true
fi

# Escalation: leak left open past the timeout -> on-call acknowledges
# (escalation path per runbook) instead of silently resolving.
info "waiting ${ESCALATE_AFTER}s escalation timeout (incident unattended)"
sleep "$ESCALATE_AFTER"
if [ -n "$incident_id" ]; then
  state="$(req GET /api/infra/v1/incidents "$OP" | jq -r "[.. | objects | select(.id == \"$incident_id\") | .status] | first // \"gone\"")"
  if [ "$state" = "open" ]; then
    req POST "/api/infra/v1/incidents/$incident_id/ack" "$OP"
    expect_status 200 "escalation: on-call acknowledges after timeout"
  else
    ok "incident already $state before timeout (no escalation needed)"
  fi
fi

# Compliance report must include the leak.
rep="$(req POST /api/infra/v1/compliance/reports/generate "$OP" '{}')"
expect_status 201 "POST /api/infra/v1/compliance/reports/generate"
rep_id="$(echo "$rep" | jq -r '.id // empty')"
if [ -n "$rep_id" ]; then
  body="$(req GET "/api/infra/v1/compliance/reports/$rep_id" "$OP")"
  expect_status 200 "GET /api/infra/v1/compliance/reports/{id}"
  echo "$body" | jq -e '.. | strings | select(test("leak"))' >/dev/null 2>&1 \
    && ok "compliance report includes the leak incident" \
    || bad "compliance report does not mention the leak"
fi

scenario_summary S3
