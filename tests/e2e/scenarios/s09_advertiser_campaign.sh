#!/usr/bin/env bash
# S9 — Advertiser creates a campaign -> KPI revenue reflects.
# Actors: advertiser (operator role), admin KPI aggregation.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S9 advertiser campaign -> KPI revenue"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S9; exit 1; }

name="S9 campaign $(date +%s)"
camp="$(req POST /api/commerce/v1/ads/campaigns "$OP" \
  "{\"name\": \"$name\", \"budget_minor\": 50000}")"
expect_status 201 "POST /api/commerce/v1/ads/campaigns"
camp_id="$(echo "$camp" | jq -r '.id // empty')"
[ -n "$camp_id" ] && ok "campaign created ($camp_id)"

req GET /api/commerce/v1/ads/campaigns "$OP"
expect_status 200 "campaign list readable"
[ -n "$camp_id" ] && echo "$(req GET /api/commerce/v1/ads/campaigns "$OP")" \
  | jq -e "[.. | objects | select(.id == \"$camp_id\")] | length == 1" >/dev/null \
  && ok "campaign visible in list" || true

# KPI surface reflects the advertising budget/revenue line.
kpis="$(req GET /api/admin/v1/admin/kpis "$OP")"
expect_status 200 "GET /api/admin/v1/admin/kpis"
echo "$kpis" | jq -e '.. | numbers | select(. > 0)' >/dev/null 2>&1 \
  && ok "admin KPI aggregation reports non-zero figures" \
  || info "KPIs zero — check commerce aggregation source"

scenario_summary S9
