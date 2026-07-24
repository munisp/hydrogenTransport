# Plan — Production Readiness Audit + Gap Closure (H2Fleet)

## Stage 1 — Audit (3 parallel reviewers, read-only)
- A: Code & Build — compile all Go/Rust/Python/TS, run existing tests, error handling, missing tests, dead config
- B: Security & Operations — authn/authz coverage, secrets handling, TLS/WAF/rate-limits, backups/DR, observability, probes, resource limits, k8s completeness
- C: Data & Integration — migration versioning, seed integrity, event/API contract coverage, E2E flows, CI/CD completeness, docs accuracy
Output per reviewer: dimension scores (0–10) with evidence + ranked gap list.

## Stage 2 — Scorecard synthesis (orchestrator)
- Weighted production-readiness score, gap register grouped into workstreams.

## Stage 3 — Gap implementation (parallel coders)
- W1: Test suite (unit + integration) for all services + PWA
- W2: Security & ops hardening (secrets template, TLS notes, rate limiting, backup jobs, dashboards)
- W3: Observability + CI/CD (Prometheus/Grafana, metrics endpoints, full CI incl. tests, lockfiles)
- W4: Data/migrations + k8s completeness + docs (RUNBOOK, SLOs, OpenAPI)

## Stage 4 — Validate & push
- Re-run builds/tests, verify tree, push deltas to GitHub (pusher subagents), re-score.
