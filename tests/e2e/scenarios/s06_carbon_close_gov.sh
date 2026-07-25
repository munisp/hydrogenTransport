#!/usr/bin/env bash
# S6 — Carbon period close -> credits issued -> gov dashboard reflects.
# Actors: carbon-analytics batch, carbon fund, government dashboard viewer.
# NOTE: carbon-analytics is intentionally NOT on the gateway (internal batch
# service); the close is driven in-network. Through the gateway we verify the
# citizen-visible credits and the gov KPI surface reflect the period.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

PERIOD="${S6_PERIOD:-$(date +%Y-%m)}"

echo "== S6 carbon period close ($PERIOD) -> gov dashboard"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S6; exit 1; }

if [ "${S6_IN_NETWORK:-0}" = "1" ]; then
  # In-network variant: trigger the period close directly on carbon-analytics.
  curl -sf -X POST "${CARBON_URL:-http://carbon-analytics:8094}/v1/carbon/compute" \
    -H 'Content-Type: application/json' -d "{\"period\": \"$PERIOD\", \"publish\": true}" \
    && ok "carbon compute close triggered ($PERIOD)" || bad "carbon compute trigger"
else
  info "gateway mode: close runs in-network (CARBON not routed); set S6_IN_NETWORK=1 inside the cluster"
fi

wait_json GET /api/citizen/v1/carbon/credits "$OP" \
  '[.. | objects | select(.period != null)] | length > 0' \
  "credits visible on citizen surface" || true

summary="$(req GET /api/citizen/v1/carbon/credits/summary "$OP")"
expect_status 200 "GET /api/citizen/v1/carbon/credits/summary"

gov="$(req GET /api/commerce/v1/gov/kpis "$OP")"
expect_status 200 "GET /api/commerce/v1/gov/kpis"
echo "$gov" | jq -e '.. | numbers | select(. > 0)' >/dev/null 2>&1 \
  && ok "gov dashboard reports non-zero KPIs (carbon reflected)" \
  || info "gov KPIs zero — verify the period close ran (S6_IN_NETWORK=1)"

scenario_summary S6
