# H2Fleet Runbook — failure modes & recovery

Local stack commands assume `docker compose -f infra/docker-compose.yml`
(`$COMPOSE`). On k8s, substitute `kubectl -n h2fleet ...` equivalents.
Alerts referenced here are defined in `infra/observability/alerts.yml`;
targets in `docs/SLO.md`.

## 1. toggle-service down → platform-wide fail-closed 404s

**Symptoms.** Every service's toggle-client SDK loses its refresh source.
Caches expire (5 s local, 30 s Redis) and the SDK is **fail-closed**
(fail-open=false, SPEC §3.2): module routes start returning
`404 {"error":"module disabled"}` across ALL domains, PWA nav empties.
`ServiceDown{service="toggle-service"}` fires.

**Diagnose.**
```bash
$COMPOSE ps toggle-service; $COMPOSE logs --tail=100 toggle-service
curl -s localhost:18080/healthz          # direct debug port
```
Usual causes: Postgres unreachable (`DATABASE_URL`), Redis down (cache
writes), crash-loop on bad config.

**Recover.**
1. Restore its dependencies first (Postgres §4, Redis §5) if they are the cause.
2. `$COMPOSE up -d --build toggle-service` (or
   `kubectl rollout restart deploy/toggle-service`).
3. Verify: `curl localhost:9080/api/toggles/v1/toggles` returns the map and
   a spot-checked route (e.g. `/api/fleet/v1/vehicles`) stops 404ing within
   ~35 s (Redis TTL + SDK cache).

**Mitigation note.** The Redis `toggles:<module>` cache gives a ~30 s grace
window; a short toggle-service blip is absorbed. Do NOT "fix" 404s by
flipping SDKs to fail-open — that silently enables disabled modules.

## 2. Kafka down → telemetry + events stop flowing

**Symptoms.** telemetry-simulator producer errors, telemetry-ingest /
digital-twin consumer lag grows (`KafkaConsumerLagHigh` once the exporter
ships), `twin.updated` stops (`TwinStale`), Dapr pubsub publish errors in
citizen-api/commerce-api logs.

**Diagnose.**
```bash
$COMPOSE logs --tail=100 kafka zookeeper
docker exec h2-kafka kafka-topics.sh --bootstrap-server localhost:9092 --list
```
Common causes: ZooKeeper unhealthy first (Kafka depends on it), disk full on
`kafka_data` volume, broker OOM.

**Recover.**
1. `$COMPOSE restart zookeeper`, wait healthy, then `$COMPOSE restart kafka`.
2. If the volume is corrupt (dev only): `$COMPOSE down`, remove `kafka_data`
   (topics are auto-created; acceptable locally, **never** in prod).
3. Consumers resume automatically (idempotent producer + consumer group
   offsets); the simulator keeps publishing where it left off. H2 levels /
   odometers continue from their last state — no data repair needed for
   simulated traffic. For real gaps, telemetry is at-least-once: duplicates
   in `fleet.telemetry` are tolerated by the `DISTINCT ON (bus_id)` readers.

## 3. Temporal down → workflow signals lost

**Symptoms.** infra-api logs "workflow signal failed" (or "TEMPORAL_HOST not
set" on misconfiguration); dispatch/incident workflows don't progress.
Temporal workflows are **being implemented** — user-facing impact is limited
to workflow-backed transitions (incident escalation, settlement).

**Recover.**
1. `$COMPOSE ps postgres-temporal` — fix the DB first if down.
2. `$COMPOSE restart temporal` (auto-setup re-applies schema; idempotent).
3. Verify `tctl --address ... cluster health` inside the container or the UI
   at http://127.0.0.1:8233.
4. infra-api does not need a restart (client reconnects), but
   `$COMPOSE restart infra-api` is safe. Signals sent while Temporal was
   down are logged and dropped — re-drive them manually (e.g. re-ack the
   incident via `POST /api/infra/v1/incidents/{id}/ack`).

## 4. Redis down → toggle cache, twin hot state, sessions, Dapr state lost

**Symptoms.** `/api/twin/*` empty/404s; toggle lookups fall through to
Postgres (slower, still correct); Dapr state operations error in
citizen-api/commerce-api; simulator `bus:meta:*` enrichment writes fail.

**Recover.**
1. `$COMPOSE restart redis` (AOF is on: `--appendonly yes`; state survives).
2. Twin hot state rebuilds automatically from the `telemetry.enriched`
   stream; `bus:meta:*` is rewritten by the simulator on its next tick.
3. If AOF is corrupt (dev only): remove `redis_data`; twin/meta rebuild as
   above, sessions are lost (users re-login).

## 5. Postgres down → everything degrades

All APIs fail readiness (`ServiceDown` fan-out). Restore order:
`postgres` → `migrator` (one-shot, `make migrate`) → app services. Temporal
has its OWN postgres (`postgres-temporal`) — a main-DB outage does not stop
Temporal and vice versa.

## 6. Permify down

AuthZ is a role+fallback hybrid: realm-role checks still enforce, Permify
checks fail closed to roles. Admin operations stay available to
`platform-admin`. Recover with `$COMPOSE up -d permify` (its data is in the
`permify` database; re-run `permify-setup` only if the schema was wiped).

## 7. TigerBeetle / Mojaloop down

Payment routes return `502 bad_gateway` and publish `fare.payment.failed`
(expected — the ledger refuses negative balances). Restart
`tigerbeetle` / `mojaloop-simulator`; re-try failed payments from the client.
Never "repair" the ledger by editing Postgres `commerce.*` rows — the ledger
is the source of truth (see `docs/DR.md` for ledger restore).

## 8. Per-service restart procedures

| Service | Restart | Post-check |
|---|---|---|
| toggle-service | `$COMPOSE up -d --build toggle-service` | `GET /api/toggles/v1/toggles` 200 |
| fleet-api | `$COMPOSE restart fleet-api` | `GET /api/fleet/v1/vehicles` 200 |
| infra-api | `$COMPOSE restart infra-api` | `GET /api/infra/v1/stations` 200 |
| citizen-api (+daprd) | `$COMPOSE restart citizen-api citizen-api-daprd` | `GET /api/citizen/v1/passenger/stops` 200 |
| commerce-api (+daprd) | `$COMPOSE restart commerce-api commerce-api-daprd` | `GET /api/commerce/v1/gov/kpis` 200 |
| telemetry-ingest | `$COMPOSE restart telemetry-ingest` | consumer lag drains |
| digital-twin | `$COMPOSE restart digital-twin` | `GET /api/twin/v1/twin` count → 50 |
| predictive-maintenance | `$COMPOSE restart predictive-maintenance` | `POST /api/ml/v1/predict` (JWT) 200 |
| route-optimizer | `$COMPOSE restart route-optimizer` | `POST /api/optimize/v1/optimize/route` (JWT) 200 |
| carbon-analytics | `$COMPOSE restart carbon-analytics` | logs resume batch loop |
| telemetry-simulator | `make simulate` | `docker logs h2-telemetry-simulator` ticks |
| pwa | `$COMPOSE restart pwa` | http://localhost:3000 loads |

Dapr sidecars share the app's network namespace (`network_mode:
service:…`) — always restart the pair together. On k8s use
`kubectl -n h2fleet rollout restart deploy/<name>`; HPAs keep min replicas
during the rollout and PDBs gate voluntary disruptions.
