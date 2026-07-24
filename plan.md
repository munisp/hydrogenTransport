# Hydrogen Bus Unified Platform — Implementation Plan

## Goal
Implement the unified, toggleable hydrogen-bus fleet platform end to end and push to https://github.com/munisp/hydrogenTransport

## Architecture Recap (from prior session)
- **20 ideas grouped into 4 domains** (each domain = independently toggleable app/module):
  1. **Fleet Operations & Telematics** — real-time telemetry, predictive maintenance, digital twin, H2 fuel monitoring, route/energy optimization
  2. **Infrastructure & Safety** — refueling station mgmt, leak detection/safety, workforce/dispatch, compliance & reporting
  3. **Citizen & Revenue** — passenger app (PWA + mobile), demand-responsive transit, carbon credits, open data portal
  4. **Commerce & Finance** — fare payments (mojaloop/tigerbeetle), advertising/marketplace, energy trading, gov dashboards
- **Middleware**: Kafka, Dapr, Fluvio, Temporal, Postgres, Keycloak, Permify, Redis, Mojaloop, OpenSearch, OpenAppSec, APISIX, TigerBeetle, Apache Sedona, GeoLibre, Lakehouse
- **Languages**: Go (core services), Rust (ingestion/twin hot paths), Python (ML/analytics), TypeScript (PWA/mobile)

## Stage 1 — Repo scaffolding & docs
- Monorepo layout: `/services`, `/apps`, `/infra`, `/packages`, `/docs`
- README, ARCHITECTURE.md, module-toggle design (feature flags per module via config + Dapr building blocks)

## Stage 2 — Backend services (subagents in parallel)
- A: Go services (fleet-api, dispatch, commerce, gateway wiring, feature-toggle service)
- B: Rust services (telemetry-ingest, digital-twin engine) + Python services (predictive-maintenance ML, route optimizer, carbon analytics)
- C: Shared packages: proto/events schemas, toggle SDK, lakehouse/ETL jobs (Sedona/Spark), seed data

## Stage 3 — Frontend (subagent)
- PWA (React + TypeScript): dashboards for all 4 domains with module toggles
- Native mobile shell (React Native/Expo skeleton) + citizen app screens

## Stage 4 — Infra & DevOps
- docker-compose with full middleware stack, k8s/helm notes, CI (GitHub Actions), Makefile

## Stage 5 — Push to GitHub
- Push all files to munisp/hydrogenTransport via GitHub plugin (batched push_files)

## Stage 6 — Validate
- Verify tree on GitHub, final summary
