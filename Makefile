SHELL := /bin/bash

API_DIR := apps/api
WEB_DIR := apps/web
BIN_DIR := bin

.DEFAULT_GOAL := help

.PHONY: help install dev api-dev api-build api-test api-race api-vet api-tidy \
	web-install web-dev web-build web-typecheck web-test \
	fmt test check build up down logs ps clean

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

api-test: ## Run Go tests
	cd $(API_DIR) && go test ./...

api-race: ## Run Go tests with the race detector
	cd $(API_DIR) && go test -race ./...

api-vet: ## Run go vet
	cd $(API_DIR) && go vet ./...

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

fmt: ## Format Go sources
	cd $(API_DIR) && gofmt -w .

test: api-test web-test ## Run all tests

check: api-test api-vet web-typecheck web-test ## Tests, vet, and typecheck

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
