#!/usr/bin/env bash
# H2Fleet scenario lib — shared helpers for tests/e2e/scenarios/s*.sh.
# Source this file; do not execute directly.
#
# Env contract (all optional except a live stack):
#   GATEWAY_URL (default http://localhost:9080)  — APISIX gateway base
#   KEYCLOAK_URL (default http://localhost:8088) — browser-facing Keycloak
#   KEYCLOAK_REALM (default h2fleet)
#   KC_CLIENT_ID / KC_CLIENT_SECRET              — confidential client (realm: services)
#   H2_ADMIN_USER/H2_ADMIN_PASSWORD              — platform-admin (admin/admin123)
#   H2_OPERATOR_USER/H2_OPERATOR_PASSWORD        — operator (operator/operator123)
#   H2_DRIVER_USER/H2_DRIVER_PASSWORD            — driver (driver/driver123)
#   H2_CITIZEN_USER/H2_CITIZEN_PASSWORD          — citizen (citizen/citizen123)
#   LEAK_INGEST_TOKEN                            — sensor shared token
#   SCENARIO_TIMEOUT (default 60)                — per-wait deadline, seconds

GATEWAY_URL="${GATEWAY_URL:-${GATEWAY:-http://localhost:9080}}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8088}"
KEYCLOAK_REALM="${KEYCLOAK_REALM:-h2fleet}"
KC_CLIENT_ID="${KC_CLIENT_ID:-services}"
KC_CLIENT_SECRET="${KC_CLIENT_SECRET:-${KEYCLOAK_SERVICES_CLIENT_SECRET:-h2fleet-services-secret-change-me}}"
LEAK_INGEST_TOKEN="${LEAK_INGEST_TOKEN:-dev-leak-token-change-me}"
SCENARIO_TIMEOUT="${SCENARIO_TIMEOUT:-60}"

S_PASS=0; S_FAIL=0
ok()   { S_PASS=$((S_PASS+1)); echo "  [PASS] $*"; }
bad()  { S_FAIL=$((S_FAIL+1)); echo "  [FAIL] $*"; }
info() { echo "  [INFO] $*"; }

command -v jq >/dev/null || { echo "jq is required for scenario scripts"; exit 2; }

# kc_token <user> <password> -> access token on stdout (empty on failure).
kc_token() {
  curl -sf -X POST \
    "$KEYCLOAK_URL/realms/$KEYCLOAK_REALM/protocol/openid-connect/token" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d "grant_type=password&client_id=$KC_CLIENT_ID&client_secret=$KC_CLIENT_SECRET&username=$1&password=$2" \
    | jq -r '.access_token // empty'
}
admin_token()    { kc_token "${H2_ADMIN_USER:-admin}" "${H2_ADMIN_PASSWORD:-admin123}"; }
operator_token() { kc_token "${H2_OPERATOR_USER:-operator}" "${H2_OPERATOR_PASSWORD:-operator123}"; }
driver_token()   { kc_token "${H2_DRIVER_USER:-driver}" "${H2_DRIVER_PASSWORD:-driver123}"; }
citizen_token()  { kc_token "${H2_CITIZEN_USER:-citizen}" "${H2_CITIZEN_PASSWORD:-citizen123}"; }

# req <method> <gateway-path> [token|-] [json-body|-] [extra curl args...]
# Body on stdout; HTTP status in $HTTP_STATUS.
HTTP_STATUS=""
req() {
  local method="$1" path="$2" token="${3:--}" data="${4:--}"; shift 4 || true
  local args=(-s -o /tmp/h2scenario_body -w '%{http_code}' -X "$method")
  [ "$token" != "-" ] && args+=(-H "Authorization: Bearer $token")
  if [ "$data" != "-" ]; then
    args+=(-H 'Content-Type: application/json' -d "$data")
  fi
  HTTP_STATUS="$(curl "${args[@]}" "$@" "$GATEWAY_URL$path" || true)"
  cat /tmp/h2scenario_body 2>/dev/null || true
}

expect_status() { # <want> <label>
  if [ "$HTTP_STATUS" = "$1" ]; then ok "$2 ($HTTP_STATUS)"; else bad "$2 (want $1, got $HTTP_STATUS)"; fi
}

# wait_json <method> <path> <token> <jq-filter-true> <label> [timeout]
wait_json() {
  local method="$1" path="$2" token="$3" filter="$4" label="$5" timeout="${6:-$SCENARIO_TIMEOUT}"
  local deadline=$((SECONDS + timeout)) body=""
  while [ "$SECONDS" -lt "$deadline" ]; do
    body="$(req "$method" "$path" "$token")"
    if [ "$HTTP_STATUS" = "200" ] && echo "$body" | jq -e "$filter" >/dev/null 2>&1; then
      ok "$label"
      return 0
    fi
    sleep 3
  done
  bad "$label (timed out after ${timeout}s; last status $HTTP_STATUS)"
  return 1
}

scenario_summary() { # <scenario-id> — exits non-zero on any failure
  echo "== $1: $S_PASS passed, $S_FAIL failed"
  [ "$S_FAIL" -eq 0 ]
}
