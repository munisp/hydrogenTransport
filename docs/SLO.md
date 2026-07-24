# H2Fleet Service Level Objectives

Targets per route group (through the APISIX gateway). These are the
**production aspirations** that dashboards and alerts key off; the local
compose stack has no redundancy and will not meet all of them.

## Availability (monthly)

| Route group | Services | Target | Notes |
|---|---|---|---|
| `/api/toggles/*` | toggle-service | 99.9% | Control plane; outage degrades the whole platform to fail-closed 404s (see RUNBOOK §1) — highest priority |
| `/api/fleet/*`, `/api/twin/*` | fleet-api, digital-twin | 99.9% | Live map & twin reads are operator-facing safety tooling |
| `/api/infra/*` | infra-api | 99.9% | Leak alarms and dispatch ride here (safety) |
| `/api/citizen/*` | citizen-api | 99.5% | Public-facing; degradation acceptable off-peak |
| `/api/commerce/*` | commerce-api | 99.9% | Money movement; failed payments must 502 cleanly, never double-charge |
| `/api/ml/*`, `/api/optimize/*` | predictive-maintenance, route-optimizer | 99.0% | Batch-tolerant; callers retry |
| Telemetry pipeline (simulator→ingest→twin) | telemetry-ingest, digital-twin | 99.9% of messages processed | at-least-once; lag-based alerting, not loss |

## Latency (gateway-measured)

| Route group | p50 | p95 | p99 |
|---|---|---|---|
| `/api/toggles/*` | 10 ms | 50 ms | 150 ms |
| `/api/fleet/*`, `/api/twin/*` | 25 ms | 150 ms | 500 ms |
| `/api/citizen/*` reads | 50 ms | 250 ms | 800 ms |
| `/api/commerce/*` payment POSTs | 150 ms | 800 ms | 2 s |
| `/api/ml/*`, `/api/optimize/*` | 500 ms | 3 s | 10 s |

## Freshness

| Signal | Target | Alert |
|---|---|---|
| Twin update age per bus | < 60 s | `TwinStale` at 120 s |
| Telemetry ingest lag | < 1000 msgs / 10 min | `KafkaConsumerLagHigh` (placeholder until exporter ships) |
| Payment failure rate | < 1% steady state | `PaymentFailureRateHigh` at 5% / 15 min |

## Error budget policy

* 99.9% ≈ 43 min/month. Burning > 2× the budget rate for 1 h pages the
  on-call (Alertmanager `severity=critical` route).
* Fail-closed toggle behaviour means toggle-service downtime consumes the
  **whole platform's** budget — its budget is therefore managed separately
  and guarded first.
* Local/dev environments have no SLO; alerts there are for signal wiring
  verification only.
