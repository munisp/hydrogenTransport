# commerce-api

Domain 4 API — Commerce & Finance (SPEC §3.4 `commerce` schema). Port **8084**
(gateway prefix `/api/commerce/*`). Each route group is gated behind its module
toggle; a disabled module returns **404** (fail-closed).

## API

| Method | Path | Module gate | Auth |
|--------|------|-------------|------|
| POST | `/v1/payments` — requires `Idempotency-Key` header. Creates `commerce.fare_payments` row, posts TigerBeetle transfer (rider wallet `1xxx` → operator revenue `2001`), publishes `fare.payment.initiated` + `fare.payment.settled`. The paying rider is **always the JWT subject** (P0-1): a body `rider_sub` not matching the JWT subject → 403; a matching/absent one is ignored. Unfunded wallet → 402 `insufficient_funds`. Daily fare capping: once a rider's settled charges for the UTC day reach `FARE_DAILY_CAP_MINOR` (default 800 = €8.00; 0 disables), further rides settle at `charged_minor = 0` (the requested `amount_minor` is still recorded; pre-capping rows have `charged_minor` NULL). Settled fares accrue loyalty points (1 pt per full €1 of charged fare, idempotent per payment id). With `use_mojaloop: true` also runs a Mojaloop transfer (real POST to `MOJALOOP_ENDPOINT`; without an endpoint the payment fails closed as `mojaloop_unavailable` unless the dev opt-in `H2_SIMULATED_MOJALOOP=true` is set). Idempotent replay returns the original payment with 200. | `fare-payments` | JWT |
| GET  | `/v1/payments?rider_sub=&status=`, `/v1/payments/{id}` (status polling) | `fare-payments` | — |
| POST | `/v1/payments/{id}/refund` — full refund of a settled payment (reversal `2001` → rider wallet for the charged amount, loyalty clawback, `fare.payment.refunded`; idempotent retry, 409 unless settled) | `fare-payments` | JWT (operator) |
| POST | `/v1/wallets/topup` `{amount_minor}` — dev/simulated wallet funding from the platform cash-in account `2002`; requires `Idempotency-Key`. Enabled by default only with the simulated ledger (`H2_SIMULATED_LEDGER=true`) or `WALLET_TOPUP_ENABLED=true` | `fare-payments` | JWT |
| GET  | `/v1/loyalty/balance` — real balance from `commerce.loyalty_accounts` (lazily created, 0 for new riders) | `loyalty-marketplace` | JWT |
| POST | `/v1/loyalty/redeem` `{offer_id}` (atomic balance check + deduction + redemption record; idempotent on `Idempotency-Key`, default `offer_id`+subject; 402 insufficient points) | `loyalty-marketplace` | JWT |
| GET  | `/v1/marketplace/offers` | `loyalty-marketplace` | — |
| POST | `/v1/marketplace/offers` | `loyalty-marketplace` | JWT |
| GET  | `/v1/energy/trades` | `energy-trading` | — |
| POST | `/v1/energy/trades` — requires `Idempotency-Key`. `kind` ∈ `h2-sale\|h2-purchase\|energy-export\|ev-v2g-export\|ev-charge-purchase` (Wave 5). Sale/export kinds draw down station surplus (h2 kinds: `infra.stations.available_kg`; `ev-v2g-export`: `available_kwh` — surplus check, 409 `insufficient_surplus`); settlement is a TigerBeetle transfer (sale/export: `3001` energy clearing → `2001` operator revenue; purchase kinds reverse, funding clearing; deterministic transfer id, `tb_transfer_id` persisted). The clearing account is overdraft-protected: unfunded → 402 `insufficient_funds` with the draw-down compensated; rejected trades land in `failed` + `energy.trade.failed`, never cleared. Success → `executed` + `energy.trade.executed`. Replay with the same key returns the original trade (200). `quantity_kg` carries kWh for the ev-* kinds (column kept; kind names the unit). | `energy-trading` | JWT (operator) |
| POST | `/v1/energy/trades/{id}/cancel` — cancels a `proposed` trade (409 otherwise; `executed`/`failed` are terminal), publishes `energy.trade.cancelled` | `energy-trading` | JWT (operator) |
| GET  | `/v1/gov/kpis` — SQL rollups: 30d revenue/payments (= ridership estimate), CO2 avoided, credits, fleet counts + `fleet_active_ratio_pct`, station H2 inventory, open incidents (incl. `in_progress`). Honest degradation: failed rollups are null + named in `degraded`; `fleet_uptime_pct` is null until a time-based availability source exists | `gov-dashboard` | — |
| GET  | `/v1/ads/campaigns`, `/v1/ads/campaigns/{id}` | `advertising` | — |
| POST | `/v1/ads/campaigns` — validated (name 1-200 chars, `budget_minor >= 0`, `ends_at >= starts_at`); PATCH `/v1/ads/campaigns/{id}` — status lifecycle enforced (`ended` is terminal; illegal transitions → 409). Campaign responses include `committed_minor` (booked placement cost) and `remaining_budget_minor` | `advertising` | JWT |
| GET  | `/v1/ads/inventory?kind=&active=`, `/v1/ads/placements?campaign_id=&inventory_id=` | `advertising` | — |
| POST | `/v1/ads/inventory` — create a slot; `/v1/ads/placements` — book a slot (budget enforced: 409 `budget_exceeded` / `slot_already_booked`; ended campaigns rejected) | `advertising` | JWT (operator) |
| GET  | `/healthz` | — | — |

## Ledger (SPEC §3.4)

TigerBeetle double-entry ledger, ledger ID 1; accounts:
`RIDER_WALLET=1xxx` (persisted per rider in `commerce.rider_accounts`,
allocated sequentially from 1001; created with
`debits_must_not_exceed_credits` so unfunded wallets reject with 402 instead
of going negative), `OPERATOR_REVENUE=2001`, `RIDER_FUNDING=2002` (top-up
cash-in source), `ENERGY_TRADE=3001` (also overdraft-protected: trades
cannot settle against an unfunded clearing account — buyer-side funding
arrives via the SPEC §3.8 settlement workflow), `CARBON_FUND=4001`,
`CARBON_ISSUANCE=4002` (platform issuance source: the `carbon.credit.issued`
consumer posts `4002` → `4001` per issued period credit; issuance creates the
asset so the account has no balance cap). When `TIGERBEETLE_ADDR` is unset
startup **fails closed** — the money path must never silently run on a
fabricated ledger; the in-memory simulated ledger requires the explicit dev
opt-in `H2_SIMULATED_LEDGER=true` (SPEC §4 fallback).

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
| `TIGERBEETLE_ADDR` | — | e.g. `tigerbeetle:3000`; **required — startup fails closed when unset** |
| `H2_SIMULATED_LEDGER` | — | `true` opts into the in-memory dev ledger instead of TigerBeetle (DEV ONLY; never in production) |
| `WALLET_TOPUP_ENABLED` | `true` with the simulated ledger, `false` otherwise | gates `POST /v1/wallets/topup` (dev funding path; disable in production once real cash-in exists) |
| `MOJALOOP_ENDPOINT` | — | e.g. `http://mojaloop:4040`; `use_mojaloop` payments fail closed (`mojaloop_unavailable`) when unset |
| `H2_SIMULATED_MOJALOOP` | — | `true` returns clearly-labelled `ml-simulated-*` transfer ids when `MOJALOOP_ENDPOINT` is unset (DEV ONLY) |
| `FARE_DAILY_CAP_MINOR` | `800` | per-rider daily fare cap in minor units; `0` disables capping |

## Run

```sh
go run ./cmd/server
docker build -f services/go/commerce-api/Dockerfile -t h2fleet/commerce-api .   # context = repo root
```

## API contract

The HTTP API is specified in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0), hand-maintained from the actual route registrations.
