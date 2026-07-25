#!/usr/bin/env bash
# H2Fleet stakeholder scenarios — live runner.
# Runs S1..S10 against the gateway (env contract in lib.sh). Prints a
# per-scenario and total summary; exits non-zero if any scenario failed.
#
#   tests/e2e/scenarios/run_all.sh            # all scenarios
#   ONLY="s01 s07" run_all.sh                 # subset
#   CI: make validate-scenarios               # static, no live stack needed
set -uo pipefail
cd "$(dirname "$0")"

scripts="$(ls s[0-9][0-9]_*.sh | sort)"
if [ -n "${ONLY:-}" ]; then
  scripts="$(for s in $ONLY; do ls ${s}_*.sh 2>/dev/null; done)"
fi

total_pass=0; total_fail=0; failed=""
for s in $scripts; do
  echo "----------------------------------------------------------------"
  if bash "$s"; then total_pass=$((total_pass+1)); else
    total_fail=$((total_fail+1)); failed="$failed $s"
  fi
done

echo "================================================================"
echo "SCENARIOS: $total_pass passed, $total_fail failed${failed:+ (failed:$failed)}"
[ "$total_fail" -eq 0 ]
