# Wave 6 — Exhaustive Gap Audit + Fix Everything

User ask (2026-07-26): deep audit of the CODEBASE (ground truth, not aspirational docs) → exhaustively enumerate use cases/scenarios the platform does NOT handle → fix all recommendations, findings, gaps.

## Ground rules for auditors
- Evidence = actual code reads (file:line). Docs (BUSINESS_LOGIC_AUDIT, NO_MOCK_AUDIT, SCENARIOS, PRODUCTION_SCORECARD) are context for what's ALREADY covered — do NOT re-report those as gaps; verify claims against code where load-bearing.
- Every gap entry: scenario (concrete stakeholder story) | why unhandled | evidence file:line | recommended fix | size (S/M/L).
- Be exhaustive but REAL — no fantasy features; each gap must be something a city fleet operator would genuinely hit.

## Stage A — 4 parallel auditors (explore, read-only)
- **A1 Operational edge cases**: driver mid-shift failures, offline/degraded ops, concurrency races not yet covered, day-boundary bugs (fare cap at midnight, carbon period boundaries), partial failures in multi-service flows (payment ok but event lost), queue starvation, telemetry gaps/dedup edge cases, clock skew, bus swaps mid-route, split journeys.
- **A2 Business-domain use cases**: fare products (period passes, subscriptions, concessions/students/seniors, group/family, corporate accounts, tourist bundles), refund disputes/chargebacks, paratransit/accessibility (wheelchair priority), school transport, charter/event services, airport express, driver payroll/attendance integration, agency/contractor settlement (multi-operator), advertising self-service billing, B2B data products billing.
- **A3 Platform/technical**: true multi-tenancy (multiple cities one deployment), tenant isolation (Permify model, DB), API versioning/deprecation policy, outbound webhooks/notifications to external systems, bulk data export (GDPR DSAR), right-to-erasure vs audit-log immutability, retention/legal hold, DR restore proof (backup exists — restore untested?), secrets rotation procedure, third-party map/transit data licensing, offline-first PWA behavior in dead zones.
- **A4 Safety/security/regulatory ops**: crash/PRD-vent/evacuation emergency workflows (vs routine leak), driver impairment/ fatigue signals, SOS/panic button flow, cyber incident response runbook execution path, insurance-claim data packs, regulator audit access (read-only auditor role), chain-of-custody for telemetry evidence, H2 station emergency (vs bus), credential compromise revocation drill, DPIA artifacts.

## Stage B — synthesis (lead)
Dedupe + rank; produce docs/GAP_AUDIT.md consolidated catalog. Everything classified: FIX-NOW (code) / CONTRACT (honest external-boundary contract implementation) / DOCUMENTED-DECISION (won't fix + why).

## Stage C — fix workstreams (parallel coders, strict ownership)
Group FIX-NOW items by service; implement with tests; contracts for external-boundary items; docs updated.

## Stage D — gate + push + scorecard update
Full compile gate, validators, push to main, update PRODUCTION_SCORECARD with gap-audit results.
