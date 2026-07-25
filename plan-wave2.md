# Plan — Stakeholder Onboarding, UX Polish, Admin Console, Real AI/ML Stack, Middleware Assessment

## Wave 1 (4 parallel agents)
- **AI stack agent** (services/python/ml-platform + upgrade predictive-maintenance):
  Real PyTorch models w/ trained weights shipped: (a) maintenance time-to-failure (LSTM), (b) ridership/demand forecast, (c) leak/anomaly autoencoder, (d) pure-PyTorch GCN (GNN) on route/station graph, (e) carbon forecast. Realistic synthetic data generator seeded from platform schema; training + fine-tuning scripts; Ray (optional, local fallback); MLflow registry; drift monitoring (PSI/KS); A/B champion-challenger in inference; continuous training loop fed by platform data (Postgres/lakehouse); CPU inference.
- **Backend agent** (services/go/admin-api :8085 + onboarding):
  Stakeholder onboarding for all personas (citizen self-serve, driver/operator/station-staff/advertiser invite+approval, partner API keys, gov/investor viewers); user management via Keycloak Admin API; admin KPI aggregation; NOC/SOC health+alerts feed; APISIX /api/admin/* route.
- **Infra agent**: compose for admin-api, ml-platform, mlflow, neo4j (profile), ray (profile), Makefile, k8s notes.
- **Auditor agent**: evidence-based robustness/integration scorecard for 11 middleware components + AI/neo4j → docs/MIDDLEWARE.md (answers user Q5/Q6).

## Wave 2 (after Backend delivers API contracts)
- **UX agent**: polished PWA design system (loading/empty/error states, transitions), onboarding flows UI per persona, unified admin console (KPI dashboards, user mgmt, all-20 toggles, NOC/SOC wallboard), mobile onboarding + polish.

## Wave 3
- Integration fix pass → full verification → push to GitHub → final report incl. Q4/Q5/Q6 answers.
