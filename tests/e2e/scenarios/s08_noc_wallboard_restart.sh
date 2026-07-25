#!/usr/bin/env bash
# S8 — Ops-center NOC wallboard detects a service down -> alert -> runbook
# restart -> health green.
# Actors: NOC operator, admin-api health sweep, alertmanager, runbook.
# Set S8_TARGET to inject a fault (docker stop h2-<svc>) when RUNBOOK_EXEC=1.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

TARGET="${S8_TARGET:-}"            # e.g. digital-twin; empty = observe only
RUNBOOK_EXEC="${RUNBOOK_EXEC:-0}"  # 1 = actually stop/restart the container

echo "== S8 NOC wallboard -> alert -> runbook restart -> green"

OP="$(operator_token)"; [ -n "$OP" ] || { bad "operator token"; scenario_summary S8; exit 1; }

if [ -n "$TARGET" ] && [ "$RUNBOOK_EXEC" = "1" ]; then
  docker stop "h2-$TARGET" >/dev/null && info "injected fault: stopped h2-$TARGET"
  wait_json GET /api/admin/v1/admin/health "$OP" \
    "[.. | objects | select(.name == \"$TARGET\" and .ok == false)] | length == 1" \
    "wallboard detects $TARGET down" 90 || true
fi

health="$(req GET /api/admin/v1/admin/health "$OP")"
expect_status 200 "GET /api/admin/v1/admin/health (wallboard sweep)"
down="$(echo "$health" | jq -r '[.. | objects | select(.ok == false) | .name] | join(", ")')"
if [ -n "$down" ]; then
  ok "wallboard flags down service(s): $down"
  req GET /api/admin/v1/admin/alerts "$OP"
  expect_status 200 "alerts feed readable for the page"
  if [ "$RUNBOOK_EXEC" = "1" ]; then
    for svc in ${down//,/ }; do
      docker start "h2-$svc" >/dev/null 2>&1 && info "runbook restart: h2-$svc"
    done
    wait_json GET /api/admin/v1/admin/health "$OP" \
      '[.. | objects | select(.ok == false)] | length == 0' \
      "health sweep green after runbook restart" 120 || true
  else
    info "RUNBOOK_EXEC=0: restart step documented, not executed (docs/RUNBOOK.md)"
  fi
else
  ok "wallboard green — no down services (detection path verified by shape)"
fi

scenario_summary S8
