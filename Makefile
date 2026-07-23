# H2Fleet — developer entrypoints
COMPOSE := docker compose -f infra/docker-compose.yml

GO_SERVICES     := toggle-service fleet-api infra-api citizen-api commerce-api
RUST_SERVICES   := telemetry-ingest digital-twin
PYTHON_SERVICES := predictive-maintenance route-optimizer carbon-analytics lakehouse-etl

.PHONY: up up-all down seed logs ps build-go build-rust build-python build-pwa \
        etl gateway-check push-note help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up: ## Start middleware only (default profile)
	$(COMPOSE) up -d
	@echo "Middleware up. APISIX :9080 | Keycloak :8180 | Temporal UI :8233 | OpenSearch Dashboards :5601 | MinIO console :9001"

up-all: ## Start middleware + all platform services (profile: apps)
	$(COMPOSE) --profile apps up -d --build
	@echo "Full stack up. Gateway: http://localhost:9080/api  PWA: http://localhost:3000"

down: ## Stop everything and remove containers (keeps volumes)
	$(COMPOSE) --profile all --profile apps --profile waf --profile etl --profile fluvio down

ps: ## Show stack status
	$(COMPOSE) --profile all --profile apps ps

seed: ## Re-apply SQL seed (idempotent) against running Postgres
	docker exec -i h2-postgres psql -U h2 -d h2fleet -f /docker-entrypoint-initdb.d/002_seed.sql

logs: ## Tail logs (make logs S=kafka)
	$(COMPOSE) --profile apps logs -f $(S)

etl: ## Run the one-shot lakehouse ETL (Spark/Sedona -> Iceberg on MinIO)
	$(COMPOSE) --profile etl run --rm lakehouse-etl

gateway-check: ## Smoke-test gateway routes (requires up-all)
	@for p in toggles fleet infra citizen commerce ml optimize twin; do \
	  code=$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:9080/api/$$p/healthz); \
	  echo "/api/$$p/healthz -> $$code"; \
	done

build-go: ## go build + vet all Go services locally
	@for s in $(GO_SERVICES); do \
	  echo "== services/go/$$s"; \
	  (cd services/go/$$s && go build ./... && go vet ./...) || exit 1; \
	done

build-rust: ## cargo check all Rust services locally
	@for s in $(RUST_SERVICES); do \
	  echo "== services/rust/$$s"; \
	  (cd services/rust/$$s && cargo check) || exit 1; \
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
	@echo "Images are built by CI and pushed to ghcr.io/munisp/h2fleet/<service>:<tag>."
	@echo "Local builds stay in the compose daemon; use 'make up-all' which builds from source."
