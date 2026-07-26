# Wave 4 — Business Rules 10/10 + No-Mock Re-Verification + GitHub Reconciliation

User asks (2026-07-25):
1. Improve/implement/fix business rules from 5.4/10 to 10/10.
2. Confirm all prior implementation claims are 100% NOT mock/stub/placeholder/partial — audit with a strict method (scan → raw line numbers → read exact lines → fix one by one), re-verify, production readiness scores.
3. Push all code to GitHub; merge all PRs to main; ensure all branches merged and functional.

## Stage A (parallel, strict file-ownership split)

### A1 — business-rules completer (coder)
- Input: docs/BUSINESS_LOGIC_AUDIT.md (per-feature scores, avg 5.4), Wave-B fixes already landed.
- Method per user: for each of the 20 features, re-run the audit's scoring rubric; READ the exact code lines behind every deduction; verify Wave-B fixes really landed; fix every remaining gap (no feature keeps a deduction without a documented, intentional reason).
- Owns: feature business logic in all services (handlers, domain rules, workflows).
- Output: updated docs/BUSINESS_LOGIC_AUDIT.md with RE-VERIFIED per-feature scores (target 10/10 each; any residual must be an env-bound item, named honestly); gates green for every touched module.

### A2 — no-mock auditor/fixer (coder)
- Method per user: grep the ENTIRE repo for mock/stub/placeholder patterns (mock, stub, fake, simulated, placeholder, TODO, FIXME, XXX, not.?implemented, REPLACE_ME, hardcoded, dummy, sample data, fallback) producing a line-number inventory; then READ each hit's exact lines in context; classify REAL / DELIBERATE-DEV-FALLBACK (env-gated) / MOCK-TO-FIX; fix one by one.
- Known deliberate fallbacks to harden/classify: simulated TigerBeetle ledger, simulated Mojaloop path, Keycloak simulated admin fallback, ML synth data generator, telemetry simulator, seed data. Rule: production path must be the default; simulated paths opt-in via explicit env (fail-closed where money/identity is involved) OR clearly documented dev-only. No silent fabrication anywhere.
- Owns: wiring/config/default/fallback code + docs/NO_MOCK_AUDIT.md (new) with per-area production scores.
- Boundary: does NOT redesign feature business rules (A1's scope); if a mock sits inside a business handler, A2 converts the mechanism (default/env-gate) and flags semantics to the lead.

## Stage B — integration gate + GitHub reconciliation
- B1 (coder): full compile gate re-run (Go/Rust/Python/TS/validators) after A1+A2; fix any cross-edit breakage.
- B2 (coder): compute delta vs GitHub main, push sequentially (≤10 files/commit); check open PRs (merge all to main) + branches (ensure everything merged; delete stale merged branches); verify remote == local byte-for-byte on the delta.
- Lead (me): check list_pull_requests/list_branches via MCP upfront to scope B2.

## Stage C — final report
- Re-verified production readiness scores (per-dimension + composite), no-mock confirmation summary, GitHub state (HEAD, branches, PRs).
