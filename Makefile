BINARY=bench
GO=go
BUN=bun
FRONTEND_DIR=frontend

.DEFAULT_GOAL := help

.PHONY: help build test dev serve display-log frontend-build backend-build

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: frontend-build backend-build ## Build the binary

test: ## Run all tests
	$(GO) test ./...

dev: ## Start development server (auto-reloads, logs to dev.log)
	@./scripts/shoreman.sh

serve: backend-build ## Run the web server
	./$(BINARY) serve --db ./bench.db --port 8080

display-log: ## Display the last 100 lines of dev.log
	@tail -100 ./dev.log | perl -pe 's/\e\[[0-9;]*m(?:\e\[K)?//g'

frontend-build: ## Build frontend assets for embedding
	cd $(FRONTEND_DIR) && $(BUN) install && $(BUN) run build

backend-build: ## Build the backend binary
	$(GO) build -o $(BINARY) ./cmd/bench

fmt: ## Format frontend code with oxfmt
	cd $(FRONTEND_DIR) && $(BUN) run fmt

fmt-check: ## Check frontend code formatting
	cd $(FRONTEND_DIR) && $(BUN) run fmt:check

lint: ## Lint frontend code with oxlint
	cd $(FRONTEND_DIR) && $(BUN) run lint

lint-fix: ## Lint and fix frontend code
	cd $(FRONTEND_DIR) && $(BUN) run lint:fix
