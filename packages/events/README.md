# packages/events — H2Fleet Event Catalog

AsyncAPI 3.0 catalog (`asyncapi.yaml`) of every Kafka topic in SPEC.md §3.3, with a
JSON Schema (draft 2020-12) per topic in `schemas/<topic>.json`.

## Envelope
Every message is a CloudEvents-ish JSON object:

```json
{ "id": "<uuid>", "type": "<topic>", "source": "<service>", "time": "<rfc3339>", "data": { ... } }
```

`type` always equals the topic name. Message **key** is the entity id (bus/station/payment uuid).

## Topics
| topic | producer | main consumers |
|---|---|---|
| telemetry.raw | edge gateways (Fluvio bridge) | telemetry-ingest |
| telemetry.enriched | telemetry-ingest | digital-twin, fleet-api, OpenSearch sink |
| twin.updated | digital-twin | fleet-api, PWA live map |
| maintenance.predicted | predictive-maintenance | fleet-api, depot-management |
| fuel.reading | fuel-monitoring | fleet-api |
| safety.leak.detected | leak-detection | infra-api, incident workflow |
| dispatch.job.assigned | dispatch-workforce | fleet-api |
| drt.requested | demand-responsive | citizen-api |
| fare.payment.initiated / fare.payment.settled | fare-payments | commerce-api, TigerBeetle poster |
| carbon.credit.issued | carbon-analytics | citizen-api, gov-dashboard |
| energy.trade.executed | energy-trading | commerce-api |
| toggle.changed | toggle-service | all services (cache refresh) |
| station.status.changed | refueling-stations | infra-api |

## Validation
```bash
pip install check-jsonschema
check-jsonschema --schemafile schemas/telemetry.enriched.json sample.json
# or lint the catalog:
npx @asyncapi/cli validate asyncapi.yaml
```
