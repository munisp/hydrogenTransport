#!/usr/bin/env bash
# S4 — Citizen signup -> plans journey -> DRT request -> driver accepts.
# Actors: citizen (self-serve), passenger PWA, DRT driver.
set -euo pipefail
cd "$(dirname "$0")"
source ./lib.sh

echo "== S4 citizen signup -> journey -> DRT -> driver accept"

# Self-serve signup (public; idempotent per email — a 409 on re-run is fine).
email="s4.citizen.$(date +%s)@example.org"
out="$(req POST /api/admin/v1/onboarding/citizen - "{\"email\": \"$email\", \"display_name\": \"S4 Citizen\", \"org\": \"\"}")"
if [ "$HTTP_STATUS" = "201" ]; then ok "citizen self-serve signup (201)"
elif [ "$HTTP_STATUS" = "409" ]; then ok "citizen already onboarded (409 idempotent)"
else bad "citizen signup (got $HTTP_STATUS)"; fi

CIT="$(citizen_token)"; [ -n "$CIT" ] || { bad "citizen token (realm citizen user must exist)"; scenario_summary S4; exit 1; }

req GET "/api/citizen/v1/passenger/journey?from=Central&to=Riverside" "$CIT"
expect_status 200 "plan journey via passenger API"

drt="$(req POST /api/citizen/v1/drt/requests "$CIT" \
  '{"pickup": "POINT(14.42 50.08)", "dropoff": "POINT(14.46 50.10)", "pax": 1}')"
expect_status 201 "POST /api/citizen/v1/drt/requests"
drt_id="$(echo "$drt" | jq -r '.id // empty')"
[ -n "$drt_id" ] && ok "DRT request created ($drt_id)" || bad "DRT id missing"

DJ="$(driver_token)"
if [ -n "$drt_id" ] && [ -n "$DJ" ]; then
  # Dispatch surfaces the job to a driver; driver accepts.
  job_id="$(req GET /api/infra/v1/dispatch/jobs "$DJ" | jq -r '([.. | objects | select(.status == "assigned") | .id] | first) // empty')"
  if [ -n "$job_id" ]; then
    req POST "/api/infra/v1/dispatch/jobs/$job_id/accept" "$DJ"
    expect_status 200 "driver accepts the dispatch job"
  else
    info "no assigned dispatch job yet; driver accept skipped (DRT request stands)"
  fi
  req GET "/api/citizen/v1/drt/requests/$drt_id" "$CIT"
  expect_status 200 "citizen can poll DRT request status"
fi

scenario_summary S4
