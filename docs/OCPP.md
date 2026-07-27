# OCPP Gateway (`services/python/ocpp-gateway`, port 8100)

OCPP 1.6J **Central System (CSMS)** that lets EV charge points connect to the
H2Fleet platform as part of the wave-5 multi-energy generalization. Charger
state lands in `infra.charge_points` / `infra.charging_sessions` (wave-5 schema
contract, migration 0008) and status changes are published to Kafka as
`station.status.changed` events (`station_type='ev_charger'`, additive per the
contract). A JWT-gated REST read API exposes charge points and sessions to the
platform.

## Architecture

```
EV charge point                 ocpp-gateway (:8100)                    platform
----------------                ----------------------                  --------
ws://…/ocpp/{cp_id}  ──OCPP-J──▶ FastAPI WebSocket route
(subprotocol ocpp1.6)           ChargePointHandler (ocpp lib, v16) ──▶ Postgres
                                                                   infra.charge_points
                                                                   infra.charging_sessions
                                EventPublisher (aiokafka, lazy)    ──▶ Kafka
                                                                   station.status.changed
REST /v1/ocpp/* (Bearer JWT) ──▶ h2fleet_auth (Keycloak RS256 JWKS)
/healthz, /metrics             ──▶ ops (prometheus-fastapi-instrumentator)
```

* One `ChargePointHandler` instance per connected charge point; the
  `charge_point_id` path parameter is the OCPP identity (`ocpp_id` column).
* OCPP transaction ids are per-connection counters mapped in-memory to
  `charging_sessions.id`. **Limitation (v1):** a CSMS restart forgets in-flight
  mappings; a reconnected charger must start a new transaction (its
  `StopTransaction` for a forgotten id is acknowledged without a session
  update and logged).
* Meter math: `meter_start` / `meter_stop` store the raw register values;
  `kwh` is always kWh — `kwh = (meter_stop − meter_start) × kwh_factor`, where
  `kwh_factor` comes from `OCPP_METER_UNIT` (`wh` → 0.001, the OCPP 1.6 default
  for `Energy.Active.Import.Register`; `kwh` → 1). A `sampledValue` with an
  explicit `unit='kWh'` is honoured per-sample in MeterValues.
* Kafka outages degrade gracefully: DB writes succeed, the event is dropped
  with an error log, and `/healthz` reports `kafka_connected: false`.

## Supported messages (OCPP 1.6J core profile)

| Charge-point → CSMS message | Handler behaviour | DB effect | Event |
|---|---|---|---|
| `BootNotification` | upsert charge point (vendor, model); reply `Accepted` + `interval` (`OCPP_BOOT_INTERVAL`, default 300 s) | `infra.charge_points` upsert, `status='Available'`, `last_heartbeat=now()` | — |
| `Heartbeat` | reply `currentTime` | `last_heartbeat=now()` | — |
| `StatusNotification` | persist raw OCPP status | `charge_points.status` | `station.status.changed` (status mapped to online/offline/degraded; additive `station_type='ev_charger'`, `available_kwh` when known, `ocpp_id`, `ocpp_status`) |
| `Authorize` | id_tag policy (below) | — | — |
| `StartTransaction` | authorize id_tag, open session, reply `transactionId` | insert `charging_sessions` (`status='active'`, `meter_start`, `id_tag`, `bus_id` when resolved) | — |
| `MeterValues` | running energy from `Energy.Active.Import.Register` (default measurand) | update session `kwh` | — |
| `StopTransaction` | finalize session | `meter_stop`, `kwh`, `stopped_at=now()`, `status='completed'` | — |

CS → CP messages (remote start/stop, smart charging, etc.) are **not**
implemented in v1.

## REST read API (JWT-gated, Keycloak RS256 via `h2fleet_auth`)

| Endpoint | Description |
|---|---|
| `GET /v1/ocpp/charge-points` | list all charge points |
| `GET /v1/ocpp/charge-points/{ocpp_id}` | one charge point (404 when unknown) |
| `GET /v1/ocpp/sessions?station_id=&status=&limit=` | sessions, optional filters (`status=active|completed`) |
| `GET /healthz` | `db` truthfully probed, websocket path + `active_charge_points`, `kafka_connected`, `open_charging` |
| `GET /metrics` | Prometheus (job `h2fleet-services`) |

## Security notes

* **Transport:** v1 speaks plain `ws://` inside the compose network. In
  production terminate **TLS at the reverse proxy / ingress** (wss://) in
  front of port 8100 — same pattern as the REST APIs behind APISIX.
* **id_tag policy** (`Authorize`, and re-checked on `StartTransaction`):
  1. id_tags matching a known bus (`fleet.vehicles` uuid or `fleet_no`) are
     always accepted and attributed (`charging_sessions.bus_id`);
  2. otherwise the `OCPP_OPEN_ID_TAGS` whitelist applies;
  3. default `OCPP_OPEN_ID_TAGS=*` is **open charging** — every tag accepted
     with a loud warning log (startup + per-tag). DEV ONLY; set an explicit
     whitelist in production (`/healthz` exposes `open_charging`).
* **REST API:** Bearer JWT required (RS256 against the realm JWKS,
  `KEYCLOAK_ISSUER`); fail-closed 503 when unconfigured, 401 on bad tokens.
* **Charge-point authentication:** OCPP 1.6J basic-auth profiles are out of
  scope for v1; network isolation + proxy ACLs are the compensating control.

## OCPP 2.0.1 upgrade path

The pinned `ocpp` library line (>=0.26,<1.0) ships both `ocpp.v16` and
`ocpp.v201` modules, so upgrading is an additive handler module
(`ocpp.v201.ChargePoint`) plus subprotocol negotiation (`ocpp2.0.1`) on the
same route — no transport changes. v1 ships 1.6J only because it covers the
deployed charger base; 2.0.1 is explicitly out of scope (plan-wave5).

## Depot charge-management limitation (v1)

No smart-charging profiles: `SetChargingProfile` / `GetCompositeSchedule` /
`ClearChargingProfile` are not implemented, so depot charge scheduling and
load balancing must be managed out-of-band (e.g. charger-side schedules). The
session data collected here (`charging_sessions`, `station.status.changed`)
is the intended input for a future depot energy-management workstream.

## Development

```bash
python -m venv /tmp/ocpp-venv && /tmp/ocpp-venv/bin/pip install -r requirements.txt
python -m pytest services/python/ocpp-gateway/tests -q   # 37 tests, mocked CP over real websockets
docker compose -f infra/docker-compose.yml --profile apps up -d ocpp-gateway
# charge points then connect to ws://localhost:18100/ocpp/{charge_point_id}
```
