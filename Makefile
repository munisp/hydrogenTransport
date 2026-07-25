# H2Fleet — developer entrypoints
COMPOSE := docker compose --env-file .env -f infra/docker-compose.yml

.env:
	@test -f .env || cp .env.example .env


GO_SERVICES     := toggle-service fleet-api infra-api citizen-api commerce-api admin-api
RUST_SERVICES   := telemetry-ingest digital-twin
PYTHON_SERVICES := predictive-maintenance route-optimizer carbon-analytics lakehouse-etl telemetry-simulator ml-platform

.PHONY: up up-all down seed logs ps build-go build-rust build-python build-pwa \
        etl gateway-check migrate simulate backup observability smoke push-note help \
        ml graph ray onboard-note

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up: .env ## Start middleware only (default profile)
	$(COMPOSE) up -d
	@echo "Middleware up. APISIX :9080 | Keycloak :8180 | Temporal UI :8233 | OpenSearch Dashboards :5601 | MinIO console :9001"

up-all: .env ## Start middleware + all platform services (profile: apps)
	$(COMPOSE) --profile apps up -d --build
	@echo "Full stack up. Gateway: http://localhost:9080/api  PWA: http://localhost:3000"

down: .env ## Stop everything and remove containers (keeps volumes)
	$(COMPOSE) --profile all --profile apps --profile waf --profile etl --profile fluvio --profile ml --profile graph --profile ray down

ps: .env ## Show stack status
	$(COMPOSE) --profile all --profile apps ps

seed: ## Re-apply SQL seed (idempotent) against running Postgres
	docker exec -i h2-postgres psql -U h2 -d h2fleet -f /docker-entrypoint-initdb.d/002_seed.sql

logs: .env ## Tail logs (make logs S=kafka)
	$(COMPOSE) --profile apps logs -f $(S)

etl: ## Run the one-shot lakehouse ETL (Spark/Sedona -> Iceberg on MinIO)
	$(COMPOSE) --profile etl run --rm lakehouse-etl

gateway-check: ## Smoke-test gateway routes (requires up-all)
	@for p in toggles fleet infra citizen commerce ml optimize twin; do \
	  code=$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:9080/api/$$p/healthz); \
	  echo "/api/$$p/healthz -> $$code"; \
	done

migrate: .env ## Apply goose migrations (infra/sql/migrations) via the one-shot migrator
	$(COMPOSE) run --rm migrator

simulate: .env ## Build + (re)start the telemetry simulator (requires middleware up)
	$(COMPOSE) --profile apps up -d --build telemetry-simulator
	@echo "Simulator publishing to telemetry.raw every 5s. Watch:"
	@echo "  docker logs -f h2-telemetry-simulator"

ml: .env ## Start middleware + MLflow tracking server (profile: ml)
	$(COMPOSE) --profile ml up -d
	@echo "MLflow UI: http://127.0.0.1:5000"
	@echo "  backend store: postgres db 'mlflow' (auto-created) | artifacts: s3://lakehouse/mlflow (MinIO)"
	@echo "  ml-platform (profile apps) tracks to http://mlflow:5000 in-network."

graph: .env ## Start middleware + Neo4j graph DB (profile: graph)
	$(COMPOSE) --profile graph up -d
	@echo "Neo4j browser: http://127.0.0.1:7474  bolt: 127.0.0.1:7687  (user neo4j / NEO4J_PASSWORD from .env)"
	@echo "ml-platform uses NEO4J_URI=bolt://neo4j:7687 in-network."

ray: .env ## Start middleware + Ray head + worker (profile: ray)
	$(COMPOSE) --profile ray up -d
	@echo "Ray dashboard: http://127.0.0.1:8265  (GCS ray-head:6379, client server 127.0.0.1:10001)"

onboard-note: ## What the new admin-api / ml-platform services need from their env contract
	@echo "admin-api    (services/go/admin-api,    :8085, profile apps):"
	@echo "  shared x-h2-env (DATABASE_URL, KAFKA_BROKERS, REDIS_ADDR, TOGGLE_URL,"
	@echo "  KEYCLOAK_ISSUER, KEYCLOAK_AUDIENCE) + PORT + KEYCLOAK_ADMIN_URL"
	@echo "  (http://keycloak:8080) + KEYCLOAK_ADMIN_CLIENT_ID/SECRET (.env) +"
	@echo "  ALERTMANAGER_URL=http://alertmanager:9093. Host port 18085."
	@echo "  Requires a confidential Keycloak client 'h2fleet-admin-api' in the"
	@echo "  h2fleet realm (create via admin console until realm import ships it)."
	@echo "ml-platform  (services/python/ml-platform, :8095, profile apps):"
	@echo "  shared x-h2-env + PORT + MLFLOW_TRACKING_URI=http://mlflow:5000"
	@echo "  (needs 'make ml' or profile all) + MODEL_ARTIFACTS_DIR=/artifacts"
	@echo "  (volume ml_artifacts) + optional NEO4J_URI=bolt://neo4j:7687"
	@echo "  (needs 'make graph'). Host port 18095."

backup: .env ## Run a one-off backup now (pg_dump both PGs + TigerBeetle snapshot -> MinIO)
	$(COMPOSE) run --rm backup once
	@echo "Artifacts in s3://h2-backups/ (MinIO console http://127.0.0.1:9001)"

observability: ## Open Grafana (prometheus :9090, alertmanager :9093 also on loopback)
	@echo "Grafana:      http://localhost:3001  (admin/admin unless overridden in .env)"
	@echo "Prometheus:   http://127.0.0.1:9090"
	@echo "Alertmanager: http://127.0.0.1:9093"
	@(command -v xdg-open >/dev/null && xdg-open http://localhost:3001) || \
	 (command -v open >/dev/null && open http://localhost:3001) || true

smoke: ## End-to-end smoke suite (tests/e2e/smoke.sh — delivered by a follow-up workstream)
	@if [ -x tests/e2e/smoke.sh ]; then \
	  tests/e2e/smoke.sh; \
	elif [ -f tests/e2e/smoke.sh ]; then \
	  sh tests/e2e/smoke.sh; \
	else \
	  echo "tests/e2e/smoke.sh not present yet (follow-up workstream) — running gateway-check instead"; \
	  $(MAKE) gateway-check; \
	fi

build-go: ## go build + vet all Go services locally
	@for s in $(GO_SERVICES); do \
	  echo "== services/go/$$s"; \
	  (cd services/go/$$s && go build ./... && go vet ./...) || exit 1; \
	done

build-rust: ## cargo check all Rust services locally (--locked when Cargo.lock exists)
	@for s in $(RUST_SERVICES); do \
	  echo "== services/rust/$$s"; \
	  if [ -f services/rust/$$s/Cargo.lock ]; then \
	    (cd services/rust/$$s && cargo check --locked) || exit 1; \
	  else \
	    echo "   (no Cargo.lock committed yet — run 'cargo generate-lockfile' there)"; \
	    (cd services/rust/$$s && cargo check) || exit 1; \
	  fi; \
	done

build-python: ## pip install + compileall all Python services locally
	@for s in $(PYTHON_SERVICES); do \
	  echo "== services/python/$$s"; \
	  (cd services/python/$$s && pip install -r requirements.txt && python -m compileall -q .) || exit 1; \
	done

build-pwa: ## install + typecheck + build the PWA (npm ci if a lockfile exists)
	cd apps/pwa && (test -f package-lock.json && npm ci || npm install --no-audit --no-fund) \
	  && npx tsc --noEmit && npx vite build

push-note: ## How images get published
	@echo "CI builds every image (docker-build job, no push) to prove Dockerfiles work."
	@echo "CI does NOT push images yet. To publish to ghcr.io/munisp/h2fleet/<service>:<tag>:"
	@echo "  1. echo \$$GHCR_PAT | docker login ghcr.io -u <user> --password-stdin"
	@echo "  2. docker compose -f infra/docker-compose.yml --profile apps build"
	@echo "  3. docker tag/tag-push each service image, or extend infra/ci/workflow.yml"
	@echo "     with a packages:write token + 'docker/build-push-action push: true'."
	@echo "Local builds stay in the compose daemon; 'make up-all' builds from source."
