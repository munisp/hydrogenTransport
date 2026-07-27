# ocpp-gateway

OCPP 1.6J central system (CSMS) for EV charge points — wave-5 multi-energy
platform service. Full documentation: **[docs/OCPP.md](../../../docs/OCPP.md)**.

* WebSocket CSMS endpoint `/ocpp/{charge_point_id}` (subprotocol `ocpp1.6`):
  BootNotification, Heartbeat, StatusNotification, Authorize,
  StartTransaction, MeterValues, StopTransaction → `infra.charge_points` +
  `infra.charging_sessions`, status changes → Kafka `station.status.changed`.
* JWT-gated REST read API `/v1/ocpp/*`, `/healthz`, `/metrics` on port 8100.

```bash
python -m pytest services/python/ocpp-gateway/tests -q
docker compose -f infra/docker-compose.yml --profile apps up -d ocpp-gateway
```
