SHELL := /bin/bash

API_DIR := apps/api
WEB_DIR := apps/web
BIN_DIR := bin
GOLANGCI_LINT ?= golangci-lint

.DEFAULT_GOAL := help

.PHONY: help install dev api-dev api-build api-test api-integration api-race api-vet api-lint api-vuln api-tidy \
	web-install web-dev web-build web-typecheck web-test web-audit web-e2e api-contract-generate api-contract-check \
	fmt test check security-check perf-smoke perf-load build up down logs ps clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: ## Install Go modules and web dependencies
	cd $(API_DIR) && go mod download
	cd $(WEB_DIR) && bun install

dev: ## Run the API and Vite dev servers together (Ctrl-C stops both)
	@trap 'kill 0' INT TERM; \
	(cd $(API_DIR) && go run ./app) & \
	(cd $(WEB_DIR) && bun run dev) & \
	wait

api-dev: ## Run the API on :8080 with AUTH_MODE=dev
	cd $(API_DIR) && go run ./app

api-build: ## Build the API binary into bin/
	@mkdir -p $(BIN_DIR)
	cd $(API_DIR) && CGO_ENABLED=0 go build -trimpath -o ../../$(BIN_DIR)/metric-api ./app

api-test: ## Run Go tests (including integration tests)
	cd $(API_DIR) && go test ./...

api-integration: ## Run API integration, security, validation, and WebSocket tests
	cd $(API_DIR) && go test ./internal/server ./internal/ws -run '^(TestIntegration|TestHTTPAuthentication|TestHTTPIngest|TestHTTPProject|TestWebSocket)' -count=1

api-race: ## Run Go tests with the race detector
	cd $(API_DIR) && go test -race ./...

api-vet: ## Run go vet
	cd $(API_DIR) && go vet ./...

api-lint: ## Run golangci-lint on the entire Go module
	cd $(API_DIR) && $(GOLANGCI_LINT) run --config ../../.golangci.yml ./...

api-vuln: ## Check reachable Go vulnerabilities (requires network access)
	cd $(API_DIR) && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

api-tidy: ## Tidy Go modules
	cd $(API_DIR) && go mod tidy

web-install: ## Install web dependencies
	cd $(WEB_DIR) && bun install

web-dev: ## Run the Vite dev server on :5173
	cd $(WEB_DIR) && bun run dev

web-build: ## Typecheck and build the SPA
	cd $(WEB_DIR) && bun run build

web-typecheck: ## Typecheck the SPA
	cd $(WEB_DIR) && bun run typecheck

web-test: ## Run SPA unit tests
	cd $(WEB_DIR) && bun run test

web-audit: ## Audit production and development dependencies (requires network access)
	cd $(WEB_DIR) && bun audit

web-e2e: web-build ## Build and run Playwright browser tests (install Chromium first)
	cd $(WEB_DIR) && bun run test:e2e

api-contract-generate: ## Validate OpenAPI and regenerate React types and validators
	cd $(WEB_DIR) && bun run api:generate

api-contract-check: ## Check the OpenAPI spec and generated files for drift
	cd $(WEB_DIR) && bun run api:check

fmt: ## Format Go sources
	cd $(API_DIR) && gofmt -w .

test: api-test web-test ## Run all tests

check: api-test api-vet api-lint api-contract-check web-typecheck web-test ## Tests, lint, contract check, vet, and typecheck

security-check: api-vuln web-audit ## Run Go and web vulnerability scans

perf-smoke: ## Run the isolated k6 smoke profile
	METRIC_K6_PROFILE=smoke bash tests/k6/run.sh

perf-load: ## Run the isolated k6 load profile
	METRIC_K6_PROFILE=load bash tests/k6/run.sh

build: api-build web-build ## Build both apps

up: ## Build and start the production stack
	docker compose up -d --build

down: ## Stop the production stack
	docker compose down

logs: ## Follow stack logs
	docker compose logs -f --tail=100

ps: ## Show stack status
	docker compose ps

clean: ## Remove build artifacts and local databases
	rm -rf $(BIN_DIR) $(WEB_DIR)/dist $(WEB_DIR)/node_modules
	rm -f $(API_DIR)/traffic.db $(API_DIR)/traffic.db-wal $(API_DIR)/traffic.db-shm
	rm -f traffic.db traffic.db-wal traffic.db-shm
