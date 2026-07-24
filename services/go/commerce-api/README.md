# commerce-api

Domain 4 API — Commerce & Finance (SPEC §3.4 `commerce` schema). Port **8084**
(gateway prefix `/api/commerce/*`). Each route group is gated behind its module
toggle; a disabled module returns **404** (fail-closed).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| POST | `/v1/payments` — requires `Idempotency-Key` header. Creates `commerce.fare_payments` row, posts TigerBeetle transfer (rider wallet `1xxx` → operator revenue `2001`), publishes `fare.payment.initiated` + `fare.payment.settled`. With `use_mojaloop: true` also runs a Mojaloop transfer (real POST to `MOJALOOP_ENDPOINT`, simulated ID otherwise). Idempotent replay returns the original payment with 200. | `fare-payments` | JWT |
| GET  | `/v1/payments?rider_sub=&status=`, `/v1/payments/{id}` (status polling) | `fare-payments` | — |
| GET  | `/v1/loyalty/balance` | `loyalty-marketplace` | JWT |
| POST | `/v1/loyalty/redeem` `{offer_id}` (atomic points deduction) | `loyalty-marketplace` | JWT |
| GET  | `/v1/marketplace/offers` | `loyalty-marketplace` | — |
| POST | `/v1/marketplace/offers` | `loyalty-marketplace` | JWT |
| GET  | `/v1/energy/trades` | `energy-trading` | — |
| POST | `/v1/energy/trades` — ledger transfer (`3001` energy trade → `2001` operator), publishes `energy.trade.executed` | `energy-trading` | JWT |
| GET  | `/v1/gov/kpis` — SQL rollups: 30d revenue/payments (= ridership estimate), CO2 avoided, credits, fleet uptime %, station H2 inventory, open incidents | `gov-dashboard` | — |
| GET  | `/v1/ads/campaigns`, `/v1/ads/campaigns/{id}` | `advertising` | — |
| POST | `/v1/ads/campaigns`, PATCH `/v1/ads/campaigns/{id}` | `advertising` | JWT |
| GET  | `/healthz` | — | — |

## Ledger (SPEC §3.4)

TigerBeetle double-entry ledger, ledger ID 1; accounts:
`RIDER_WALLET=1xxx` (deterministic from rider sub), `OPERATOR_REVENUE=2001`,
`ENERGY_TRADE=3001`, `CARBON_FUND=4001`. When `TIGERBEETLE_ADDR` is unset a
simulated in-memory ledger is used (SPEC §4 fallback) and a warning is logged.

Note: `tigerbeetle-go` requires **CGO** (bundled native client), so the
Dockerfile builds on `golang:1.22-bookworm` and ships `distroless/cc`.

## Configuration (env, SPEC §3.5)

| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8084` | |
| `DATABASE_URL` | — | required |
| `TOGGLE_URL` | — | fail-closed when unset |
| `KAFKA_BROKERS` | — | no-op logging publisher when unset |
| `KEYCLOAK_ISSUER` | — | in-network realm URL; `KEYCLOAK_ISSUER_ALT` also accepted |
| `TIGERBEETLE_ADDR` | — | e.g. `tigerbeetle:3000` |
| `MOJALOOP_ENDPOINT` | — | e.g. `http://mojaloop:4040` |

## Run

```sh
go run ./cmd/server
docker build -f services/go/commerce-api/Dockerfile -t h2fleet/commerce-api .   # context = repo root
```
