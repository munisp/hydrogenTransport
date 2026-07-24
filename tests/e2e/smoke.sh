#!/usr/bin/env bash
# H2Fleet — end-to-end smoke suite (see tests/e2e/README.md).
#
# Expects the full compose stack up (`make up-all`). Validates, through the
# APISIX gateway only:
#   a) every /api/<prefix>/healthz answers 200 (NOT 404 — a 404 means the
#      gateway route, not the service, is missing);
#   b) feature-toggle contract: flip a module OFF -> its route 404s ->
#      flip it back ON -> route recovers (SPEC §3.2);
#   c) payment creation honours Idempotency-Key: 201 (settled) or 502
#      (ledger/rails down) — never a fabricated success; replay returns the
#      same payment (no duplicate);
#   d) after the simulator has run, /api/fleet/v1/telemetry/latest has rows;
#   e) GET /api/twin/v1/twin is a 200 JSON array.
#
# Prints a PASS/FAIL summary and exits non-zero on any failure.
set -euo pipefail

# ---------------------------------------------------------------- config
GATEWAY="${GATEWAY:-http://localhost:9080}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8088}"
KEYCLOAK_REALM="${KEYCLOAK_REALM:-h2fleet}"
ADMIN_USER="${SMOKE_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${SMOKE_ADMIN_PASSWORD:-admin123}"
CLIENT_ID="${SMOKE_CLIENT_ID:-services}"
CLIENT_SECRET="${KEYCLOAK_SERVICES_CLIENT_SECRET:-h2fleet-services-secret-change-me}"
# Module/route pair used for the toggle flip (fuel-monitoring is read-only
# and seed-backed, so ON-state reliably answers 200).
TOGGLE_MODULE="${SMOKE_TOGGLE_MODULE:-fuel-monitoring}"
TOGGLE_ROUTE="${SMOKE_TOGGLE_ROUTE:-/api/fleet/v1/fuel/levels}"
GATEWAY_TIMEOUT="${SMOKE_GATEWAY_TIMEOUT:-180}"  # seconds to wait for the stack
FLIP_TIMEOUT="${SMOKE_FLIP_TIMEOUT:-30}"         # toggle propagation (5s SDK cache)
SIM_WAIT="${SMOKE_SIM_WAIT:-30}"                 # simulator warm-up for check (d)

PASS=0
FAIL=0
ok()   { PASS=$((PASS+1)); echo "[PASS] $*"; }
bad()  { FAIL=$((FAIL+1)); echo "[FAIL] $*"; }
info() { echo "[INFO] $*"; }

# code <method> <url> [curl-args...] -> prints HTTP status code
code() { local method="$1" url="$2"; shift 2; curl -s -o /dev/null -w '%{http_code}' -X "$method" "$@" "$url"; }
# body <method> <url> [curl-args...] -> prints response body
body() { local method="$1" url="$2"; shift 2; curl -s -X "$method" "$@" "$url"; }

# wait_for_code <expected> <method> <url> <timeout-s> [curl-args...]
wait_for_code() {
  local want="$1" method="$2" url="$3" timeout="$4"; shift 4
  local deadline=$((SECONDS + timeout)) got=""
  while [ "$SECONDS" -lt "$deadline" ]; do
    got="$(code "$method" "$url" "$@" || true)"
    [ "$got" = "$want" ] && return 0
    sleep 2
  done
  [ -n "$got" ] && info "last status for $url: $got"
  return 1
}

echo "=== H2Fleet E2E smoke ==="
info "gateway=$GATEWAY keycloak=$KEYCLOAK_URL realm=$KEYCLOAK_REALM"

# ---------------------------------------------------------------- (a) healthz
info "waiting up to ${GATEWAY_TIMEOUT}s for the gateway..."
if ! wait_for_code 200 GET "$GATEWAY/api/toggles/healthz" "$GATEWAY_TIMEOUT"; then
  bad "gateway never answered /api/toggles/healthz 200 — is 'make up-all' running?"
  echo "=== SMOKE SUMMARY: $PASS passed, $FAIL failed ==="
  exit 1
fi

for p in toggles fleet infra citizen commerce ml optimize twin; do
  c="$(code GET "$GATEWAY/api/$p/healthz")"
  if [ "$c" = "200" ]; then
    ok "GET /api/$p/healthz -> 200"
  elif [ "$c" = "404" ]; then
    bad "GET /api/$p/healthz -> 404 (gateway route missing in infra/apisix/apisix.yaml, not a service outage)"
  else
    bad "GET /api/$p/healthz -> $c (want 200)"
  fi
done

# ---------------------------------------------------------------- admin token
TOKEN_JSON="$(curl -s -X POST "$KEYCLOAK_URL/realms/$KEYCLOAK_REALM/protocol/openid-connect/token" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" -d "username=$ADMIN_USER" -d "password=$ADMIN_PASSWORD")"
TOKEN="$(printf '%s' "$TOKEN_JSON" | jq -r '.access_token // empty')"
if [ -n "$TOKEN" ]; then
  ok "admin token acquired (password grant, user '$ADMIN_USER')"
else
  bad "admin token grant failed: $(printf '%s' "$TOKEN_JSON" | jq -rc '.error_description // .' | head -c 200)"
  echo "=== SMOKE SUMMARY: $PASS passed, $FAIL failed ==="
  exit 1
fi
AUTH=(-H "Authorization: Bearer $TOKEN")

# ---------------------------------------------------------------- (b) toggle flip
put_toggle() { # <true|false> -> status
  code PUT "$GATEWAY/api/toggles/v1/toggles/$TOGGLE_MODULE" \
    "${AUTH[@]}" -H 'Content-Type: application/json' -d "{\"enabled\": $1}"
}

c="$(put_toggle false)"
if [ "$c" = "200" ] || [ "$c" = "204" ]; then
  ok "PUT /api/toggles/v1/toggles/$TOGGLE_MODULE {enabled:false} -> $c"
else
  bad "toggle OFF -> $c (want 200/204; check platform-admin role + Permify)"
fi

if wait_for_code 404 GET "$GATEWAY$TOGGLE_ROUTE" "$FLIP_TIMEOUT"; then
  ok "toggled-OFF module route $TOGGLE_ROUTE -> 404 (SPEC §3.2)"
else
  bad "toggled-OFF module route $TOGGLE_ROUTE did not 404 within ${FLIP_TIMEOUT}s"
fi

c="$(put_toggle true)"
if [ "$c" = "200" ] || [ "$c" = "204" ]; then
  ok "toggle $TOGGLE_MODULE flipped back ON -> $c"
else
  bad "toggle ON -> $c (want 200/204)"
fi

if wait_for_code 200 GET "$GATEWAY$TOGGLE_ROUTE" "$FLIP_TIMEOUT"; then
  ok "re-enabled module route $TOGGLE_ROUTE recovered -> 200"
else
  bad "re-enabled module route $TOGGLE_ROUTE did not recover within ${FLIP_TIMEOUT}s"
fi

# ---------------------------------------------------------------- (c) payment idempotency
IDEM_KEY="smoke-$(date +%s)-$$"
PAY_BODY='{"amount_minor": 250, "currency": "EUR"}'
pay() { # -> "<code> <body>"
  curl -s -w '\n%{http_code}' -X POST "$GATEWAY/api/commerce/v1/payments" \
    "${AUTH[@]}" -H 'Content-Type: application/json' -H "Idempotency-Key: $IDEM_KEY" -d "$PAY_BODY"
}
resp="$(pay)"; c1="$(printf '%s' "$resp" | tail -n1)"; b1="$(printf '%s' "$resp" | sed '$d')"

case "$c1" in
  201)
    id1="$(printf '%s' "$b1" | jq -r '.id // empty')"
    st1="$(printf '%s' "$b1" | jq -r '.status // empty')"
    tb1="$(printf '%s' "$b1" | jq -r '.tb_transfer_id // empty')"
    if [ -n "$id1" ] && [ "$st1" = "settled" ] && [ -n "$tb1" ]; then
      ok "payment create -> 201 settled (id=$id1, tb_transfer_id present)"
    else
      bad "payment create -> 201 but body lacks id/settled/tb_transfer_id (fabrication?): $b1"
    fi
    ;;
  502)
    # Ledger or Mojaloop rails down: acceptable ONLY as an explicit failure,
    # never as a fabricated success.
    if printf '%s' "$b1" | jq -e '.error' >/dev/null 2>&1; then
      ok "payment create -> 502 with explicit error (rails down, no fabrication): $(printf '%s' "$b1" | jq -r '.error' | head -c 80)"
    else
      bad "payment create -> 502 without error body: $b1"
    fi
    id1="$(printf '%s' "$b1" | jq -r '.payment.id // empty')"
    ;;
  404)
    bad "payment create -> 404 (gateway route or fare-payments module missing)"
    id1=""
    ;;
  *)
    bad "payment create -> $c1 (want 201 or honest 502): $(printf '%s' "$b1" | head -c 160)"
    id1=""
    ;;
esac

# Replay the SAME Idempotency-Key: must return the same payment, never a duplicate.
resp="$(pay)"; c2="$(printf '%s' "$resp" | tail -n1)"; b2="$(printf '%s' "$resp" | sed '$d')"
id2="$(printf '%s' "$b2" | jq -r '.id // empty')"
if [ "$c2" = "200" ] && [ -n "$id1" ] && [ "$id2" = "$id1" ]; then
  ok "idempotent replay -> 200 with SAME payment id ($id2, no duplicate)"
elif [ "$c2" = "200" ] && [ -z "$id1" ]; then
  ok "idempotent replay -> 200 (id=$id2)"
else
  bad "idempotent replay -> $c2 id=$id2 (first id=$id1) — replay must return the original payment"
fi

# Missing Idempotency-Key must be rejected 400.
c="$(code POST "$GATEWAY/api/commerce/v1/payments" "${AUTH[@]}" -H 'Content-Type: application/json' -d "$PAY_BODY")"
if [ "$c" = "400" ]; then
  ok "payment without Idempotency-Key -> 400"
else
  bad "payment without Idempotency-Key -> $c (want 400)"
fi

# ---------------------------------------------------------------- (d) telemetry flow
LATEST="$GATEWAY/api/fleet/v1/telemetry/latest"
rows="$(body GET "$LATEST" | jq 'if type=="array" then length else -1 end' 2>/dev/null || echo -1)"
if [ "$rows" -le 0 ]; then
  info "no telemetry yet; waiting ${SIM_WAIT}s for the simulator..."
  sleep "$SIM_WAIT"
  rows="$(body GET "$LATEST" | jq 'if type=="array" then length else -1 end' 2>/dev/null || echo -1)"
fi
if [ "$rows" -gt 0 ]; then
  ok "GET /api/fleet/v1/telemetry/latest -> $rows bus sample(s) after simulator run"
else
  bad "GET /api/fleet/v1/telemetry/latest -> $rows rows (want > 0; is telemetry-simulator running?)"
fi

# ---------------------------------------------------------------- (e) twin API
c="$(code GET "$GATEWAY/api/twin/v1/twin")"
is_array="$(body GET "$GATEWAY/api/twin/v1/twin" | jq 'type=="array"' 2>/dev/null || echo false)"
if [ "$c" = "200" ] && [ "$is_array" = "true" ]; then
  ok "GET /api/twin/v1/twin -> 200 JSON array"
elif [ "$c" = "404" ]; then
  bad "GET /api/twin/v1/twin -> 404 (gateway route or digital-twin module missing)"
else
  bad "GET /api/twin/v1/twin -> $c array=$is_array (want 200 JSON array)"
fi

# ---------------------------------------------------------------- summary
echo "=== SMOKE SUMMARY: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
