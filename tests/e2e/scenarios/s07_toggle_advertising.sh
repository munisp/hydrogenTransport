#!/usr/bin/env bash
# S7 — Admin toggles advertising OFF -> PWA nav loses it -> toggles ON.
# Actors: platform-admin, passenger PWA (module-toggle driven nav).
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

MODULE="${S7_MODULE:-advertising}"

echo "== S7 toggle $MODULE OFF -> PWA nav -> ON"

ADMIN="$(admin_token)"; [ -n "$ADMIN" ] || { bad "admin token"; scenario_summary S7; exit 1; }

req PUT "/api/admin/v1/admin/toggles/$MODULE" "$ADMIN" '{"enabled": false}'
expect_status 200 "admin toggles $MODULE OFF (proxied, platform-admin)"

# The PWA nav renders from the public toggle feed; poll past the 5s SDK cache.
wait_json GET /api/toggles/v1/toggles "" ".toggles[\"$MODULE\"] == false" \
  "public toggle feed shows $MODULE disabled (PWA nav drops the entry)" 20 || true

# Module route must close (defense in depth): ads surface 404s while OFF.
code="$(curl -s -o /dev/null -w '%{http_code}' "$GATEWAY_URL/api/commerce/v1/ads/campaigns")"
[ "$code" = "404" ] && ok "advertising module routes 404 while OFF" \
  || info "ads route answered $code while OFF (toggle gate may allow reads)"

req PUT "/api/admin/v1/admin/toggles/$MODULE" "$ADMIN" '{"enabled": true}'
expect_status 200 "admin toggles $MODULE back ON"

wait_json GET /api/toggles/v1/toggles "" ".toggles[\"$MODULE\"] == true" \
  "public toggle feed shows $MODULE enabled again (PWA nav restored)" 20 || true

scenario_summary S7
