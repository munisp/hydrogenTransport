# H2Fleet Incident Response — H2 leak alarm path (end-to-end)

Hydrogen leaks are the platform's safety-critical flow. This document traces
the full path and the operator actions at each hop, then gives severity and
escalation rules.

## 1. Alarm path

```
Bus/station H2 sensor
   │  POST /api/infra/v1/safety/leak        (sensor token: LEAK_INGEST_TOKEN, or JWT)
   ▼
infra-api  ── validates token, rate-limits, persists infra.incidents (type=leak,
   │          severity from ppm reading), status=open
   ├──▶ Kafka: safety.leak.detected envelope (SPEC §3.3)
   ├──▶ Temporal: incident-response workflow signal (when TEMPORAL_HOST set;
   │    being implemented — degrades to logged no-op, Postgres row remains truth)
   └──▶ Redis/pubsub: live alarm to operator consoles
        │
        ├──▶ citizen-api: suppresses affected stops/arrivals on public pages
        ├──▶ fleet-api: flags the bus on the live map
        ├──▶ compliance-reporting (cron binding): queues the incident for the
        │    next regulatory report
        └──▶ Alertmanager (platform alerts) — station/service health changes
             surface in Grafana + alertmanager UI
   ▼
Operator acknowledges:  POST /api/infra/v1/incidents/{id}/ack     (JWT operator)
Dispatch mitigation:    POST /api/infra/v1/dispatch/jobs          (JWT operator)
Resolve after all-clear: POST /api/infra/v1/incidents/{id}/resolve (JWT operator)
```

Detection-to-visible target: **< 5 s** (gateway + ingest + Kafka hop).
Acknowledgement target: **< 2 min** during operations hours.

## 2. Severity model

| Severity | Trigger (sensor ppm / context) | Required action |
|---|---|---|
| `low` | > 1,000 ppm, ventilated area | auto-log, operator review within 1 h |
| `medium` | > 5,000 ppm or persistent low reading | immediate ack, bus to depot, work order auto-created |
| `high` | > 10,000 ppm (~25% LEL) or station sensor | page on-call, bus/station evacuated & isolated, emergency services per site plan |

The sensor payload's `severity` is advisory; infra-api may upgrade it from
reading thresholds. Never downgrade below the sensor-reported value.

## 3. On-call playbook (high severity)

1. **Ack** the incident (stops repeat paging; timestamps response).
2. **Isolate**: set the bus status or station to offline
   (`PATCH /api/infra/v1/stations/{id}/status`), dispatch a tow/tech job.
3. **Verify telemetry**: check the bus twin (`/api/twin/v1/twin/{bus_id}`) —
   a stationary, powered-down bus should show `fuel_cell_kw ≈ 0` and stable
   `h2_level_pct`; a dropping level confirms ongoing leakage.
4. **Escalate** to the depot safety officer and, per site plan, emergency
   services. The platform informs but never replaces site emergency procedure.
5. **Resolve** only after an all-clear reading; attach measurements to
   `infra.incidents.meta`. The compliance cron picks the incident up for the
   regulatory report automatically.

## 4. Degraded-mode rules

* **Kafka down**: the incident row is still written synchronously — the API
  call does NOT fail. Alarms queue in the producer buffer; expect delayed
  fan-out (RUNBOOK §2). Treat any leak alarm as delivered even if downstream
  notifications lag.
* **toggle-service down**: `leak-detection` routes fail-closed 404 like
  everything else (RUNBOOK §1). Sensor gateways must retry with backoff —
  restoring toggle-service is P1.
* **Module `leak-detection` toggled OFF**: ingest 404s by design; this must
  never be off in a deployment with active sensors — the deploy profile
  review checklist (docs/DEPLOYMENT.md §3) gates it.

## 5. Drills

Quarterly: replay a synthetic `safety.leak.detected` (fixture in
`packages/events/fixtures/`) through the simulator path, verify
incident row + ack/resolve round-trip + Temporal signal log line, and time
detection→ack. Record results in the ops log.

## 6. Credential compromise — revocation & rotation runbook

Symptoms: unexpected 401/403 spikes, audit anomaly alert firing on
`actor_sub` mismatch, a secret appearing anywhere it shouldn't (logs, ticket,
chat), or a departed employee/contractor who held credentials.

Order matters: **revoke first, rotate second, verify third.**

### 6.1 Audit ingest token (`AUDIT_INGEST_TOKEN`) — zero-downtime rollover

The audit-log service accepts a **comma-separated token list** (Wave-6
A3-08), so rotation needs no producer/consumer restart coordination:

1. Set `AUDIT_INGEST_TOKEN="new-token,old-token"` on audit-log and reload —
   both tokens are now accepted.
2. Cut every producer (leak-ingest, services using `pkg/auditclient`) to
   `new-token`.
3. Remove the old token: `AUDIT_INGEST_TOKEN="new-token"`, reload.
4. **Compromise case**: skip step 1's grace — set the new token alone
   immediately, accept the ingest gap, and pull the missed window from the
   producers' retry queues (auditclient buffers and retries on 401).

### 6.2 Keycloak (JWT signing, client secrets, user credentials)

1. **User/operator account**: disable in the realm admin console → active
   tokens remain valid until expiry (max 15 min access-token lifespan) —
   force it now: *Sessions → Sign out all sessions* for the user, and for a
   service account revoke its client session too.
2. **Realm signing key compromise** (worst case — every JWT forgeable):
   *Clients → realm keys → rotate RS256 keypair*, which invalidates all
   outstanding tokens platform-wide; every service re-fetches JWKS
   automatically (JWKS cache TTL). Expect a brief 401 wave; do this in a
   maintenance window if possible.
3. **Client secrets** (service-to-service): regenerate in the client tab,
   update the service's secret mount, restart that service only.

### 6.3 Webhook signing secrets (`infra.webhook_subscriptions.secret`)

1. Platform-admin: `POST /api/infra/v1/webhooks/{id}/rotate-secret` issues a
   new secret (returned once, in the response only).
2. Deliver the new secret to the subscriber out-of-band; they verify
   `X-H2Fleet-Signature` with it from the next delivery.
3. **Compromise case**: disable the subscription (`PATCH ... {"enabled":false}`)
   first — an attacker holding the secret can forge event payloads at the
   subscriber.

### 6.4 Database / TigerBeetle / Mojaloop credentials

1. Postgres role password: `ALTER ROLE ... PASSWORD`, update the secret
   mount, rolling-restart the services (pool drains pick up the new
   password; old connections die on their own).
2. Mojaloop JWS keys: rotate per the switch's key ceremony
   (docs/MOJALOOP.md); the platform side fails closed — a wrong key rejects
   transfers, it never silently accepts.
3. TigerBeetle has no per-client auth (network-segmented); compromise
   response is network isolation — see docs/INSIDER_THREAT.md.

### 6.5 Verification (always)

- `GET /api/audit/v1/audit/verify` → `ok: true` (chain intact through the
  incident window).
- Query the trail for everything the compromised credential did:
  `GET /api/audit/v1/audit?actor_sub=<sub>&from=<compromise-start>` —
  that slice, plus the evidence-pack endpoint if an incident was opened,
  is the forensics package.
- Record: what was revoked, when, what it could reach, what it did reach,
  and the residual-risk sign-off in the ops log (feeds DPIA review,
  docs/DPIA.md §6).
