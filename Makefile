# ============================================================
# DataAtlas — root Makefile
# ============================================================

.DEFAULT_GOAL := help
SHELL := /bin/bash

.PHONY: help infra-up infra-down infra-observability dev web-dev control-plane-dev ingestion-dev test lint build migrate migrate-status clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

infra-up: ## Start core local infrastructure (PostgreSQL, Redis, NATS, MinIO)
	docker compose up -d

infra-observability: ## Start observability stack (OTel collector, Prometheus, Grafana)
	docker compose --profile observability up -d

infra-down: ## Stop local infrastructure
	docker compose --profile observability down

dev: ## Run all application services in development mode
	$(MAKE) -j3 web-dev control-plane-dev ingestion-dev

web-dev: ## Run Next.js dashboard in dev mode
	pnpm --filter @dataatlas/web dev

control-plane-dev: ## Run Go control plane in dev mode
	cd services/control-plane && go run ./cmd/server

ingestion-dev: ## Run Python ingestion engine in dev mode
	cd services/ingestion-engine && uv run uvicorn app.main:app --port 8090 --reload

test: ## Run all test suites (Go, Python, frontend)
	@bash scripts/test.sh

lint: ## Run all linters
	@bash scripts/lint.sh

build: ## Build all application components
	@bash scripts/build.sh

migrate: ## Run database migrations (apply all pending)
	@bash scripts/migrate.sh up

migrate-status: ## Show database migration status
	@bash scripts/migrate.sh status

clean: ## Remove disposable build artifacts
	rm -rf apps/web/.next apps/web/node_modules/.cache services/control-plane/bin .pytest_cache .ruff_cache
