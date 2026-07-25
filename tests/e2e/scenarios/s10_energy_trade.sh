#!/usr/bin/env bash
# S10 — Energy surplus trade (h2-sale) -> physical backing check -> TigerBeetle
# settlement, idempotent on Idempotency-Key.
# Actors: energy trader (operator role), commerce-api, TigerBeetle ledger.
#
# Contract (services/go/commerce-api/openapi.yaml POST /v1/energy/trades):
#   body: { kind: h2-sale|h2-purchase|energy-export, quantity_kg > 0, price_minor >= 1 }
#   header: Idempotency-Key REQUIRED (400 without it)
#   outcomes: 201 executed (tb_transfer_id set) | 402 clearing account
#   unfunded (trade marked failed) | 409 quantity exceeds station surplus
#   (trade marked failed) | 200 idempotent replay of the same key.
# We assert the executed ledger line ONLY when the clearing account is
# actually funded — a fresh stack has no buyer settlement funding energy
# clearing (3001), so 402/failed is the honest outcome there and asserting
# "executed" unconditionally would fabricate success.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S10 energy surplus trade -> physical backing -> ledger settlement"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S10; exit 1; }

# Contract guard: the header is mandatory.
req POST /api/commerce/v1/energy/trades "$OP" \
  '{"kind": "h2-sale", "quantity_kg": 25, "price_minor": 84000}' >/dev/null
expect_status 400 "POST /v1/energy/trades without Idempotency-Key is rejected"

key="s10-$(date +%s)-$RANDOM"
body='{"kind": "h2-sale", "quantity_kg": 25, "price_minor": 84000}'
trade="$(req POST /api/commerce/v1/energy/trades "$OP" "$body" -H "Idempotency-Key: $key")"
case "$HTTP_STATUS" in
  201) ok "trade executed ($HTTP_STATUS)";;
  402) ok "trade proposed but clearing account unfunded -> failed ($HTTP_STATUS, honest)";;
  409) ok "trade exceeds recorded station surplus -> failed ($HTTP_STATUS, honest)";;
  *)   bad "trade create (want 201/402/409, got $HTTP_STATUS)";;
esac

# 201 returns the trade at top level; 402/409 wrap it as {error, message, trade}.
tid="$(echo "$trade" | jq -r '.id // .trade.id // empty')"
status="$(echo "$trade" | jq -r '.status // .trade.status // empty')"
[ -n "$tid" ] && ok "trade recorded ($tid, status=$status)"

if [ "$HTTP_STATUS" = "201" ]; then
  # Ledger balance: TigerBeetle rejects unbalanced transfers, so an executed
  # trade with a persisted tb_transfer_id is the double-entry proof.
  tb="$(echo "$trade" | jq -r '.tb_transfer_id // empty')"
  [ "$status" = "executed" ] && [ -n "$tb" ] \
    && ok "ledger accepted the balanced transfer (tb_transfer_id=$tb)" \
    || bad "executed trade missing tb_transfer_id"
else
  info "settlement not asserted: clearing account needs a funded buyer settlement first"
fi

# Idempotent retry: same key must return the SAME trade, never a duplicate
# (no second transfer, no second surplus draw-down).
replay="$(req POST /api/commerce/v1/energy/trades "$OP" "$body" -H "Idempotency-Key: $key")"
rid="$(echo "$replay" | jq -r '.id // empty')"
if [ -n "$tid" ] && [ "$HTTP_STATUS" = "200" ] && [ "$rid" = "$tid" ]; then
  ok "idempotent replay returned same trade ($rid)"
elif [ -z "$tid" ]; then
  info "skipping replay assert (first create failed)"
else
  bad "idempotency violated (status=$HTTP_STATUS, $tid vs $rid)"
fi

wait_json GET /api/commerce/v1/energy/trades "$OP" \
  "[.. | objects | select(.id == \"$tid\")] | length == 1" \
  "trade visible on the trades feed" || true

gov="$(req GET /api/commerce/v1/gov/kpis "$OP")"
expect_status 200 "GET /api/commerce/v1/gov/kpis (energy revenue line)"

scenario_summary S10
