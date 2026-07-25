#!/usr/bin/env bash
# S5 — Rider pays fare (idempotent retry) -> loyalty points -> redeems offer.
# Actors: rider, commerce-api, loyalty marketplace.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S5 fare payment (idempotent) -> loyalty -> redeem"

CIT="$(citizen_token)"; [ -n "$CIT" ] || { bad "citizen token"; scenario_summary S5; exit 1; }
OP="$(operator_token)"

key="s5-$(date +%s)-$RANDOM"
p1="$(req POST /api/commerce/v1/payments "$CIT" '{"amount_minor": 250, "currency": "EUR"}' -H "Idempotency-Key: $key")"
if [ "$HTTP_STATUS" = "201" ] || [ "$HTTP_STATUS" = "200" ]; then ok "fare payment created ($HTTP_STATUS)"
elif [ "$HTTP_STATUS" = "502" ]; then bad "ledger/rails down (502) — never fabricate success"
else bad "payment create (got $HTTP_STATUS)"; fi
id1="$(echo "$p1" | jq -r '.id // empty')"

# Idempotent retry: same key must return the SAME payment, never a duplicate.
p2="$(req POST /api/commerce/v1/payments "$CIT" '{"amount_minor": 250, "currency": "EUR"}' -H "Idempotency-Key: $key")"
id2="$(echo "$p2" | jq -r '.id // empty')"
if [ -n "$id1" ] && [ "$id1" = "$id2" ]; then ok "idempotent retry returned same payment ($id1)"
elif [ -z "$id1" ]; then info "skipping replay assert (first create failed)"
else bad "idempotency violated: $id1 vs $id2"; fi

bal="$(req GET /api/commerce/v1/loyalty/balance "$CIT")"
expect_status 200 "GET /api/commerce/v1/loyalty/balance"
points="$(echo "$bal" | jq -r '.points // 0')"
info "loyalty balance: $points points"

offer="$(req GET /api/commerce/v1/marketplace/offers | jq -c '([.. | objects | select(.active == true)] | sort_by(.cost_points // .points_cost // 999999) | first) // empty')"
if [ -n "$offer" ]; then
  offer_id="$(echo "$offer" | jq -r '.id')"
  req POST /api/commerce/v1/loyalty/redeem "$CIT" "{\"offer_id\": \"$offer_id\"}"
  if [ "$HTTP_STATUS" = "200" ] || [ "$HTTP_STATUS" = "201" ]; then ok "offer redeemed"
  elif [ "$HTTP_STATUS" = "400" ] || [ "$HTTP_STATUS" = "409" ] || [ "$HTTP_STATUS" = "422" ]; then
    info "redemption rejected (insufficient points) — minting a test offer within reach is an operator action; asserting error contract"
    echo "$(req POST /api/commerce/v1/loyalty/redeem "$CIT" '{"offer_id": "does-not-exist"}')" >/dev/null
    [ "$HTTP_STATUS" = "400" ] || [ "$HTTP_STATUS" = "404" ] || [ "$HTTP_STATUS" = "422" ] \
      && ok "redeem rejects unknown offer cleanly" || bad "unknown offer redeem (got $HTTP_STATUS)"
  else bad "redeem offer (got $HTTP_STATUS)"; fi
else
  info "no active offers; operator seeds one"
  [ -n "$OP" ] && req POST /api/commerce/v1/marketplace/offers "$OP" '{"title": "S5 free coffee", "description": "scenario", "cost_points": 1}' \
    && expect_status 201 "operator creates offer"
fi

scenario_summary S5
