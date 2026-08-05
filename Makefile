SHELL := /bin/sh

.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
DOCKER_COMPOSE ?= docker compose
TASK := task-1
CLI := $(GO) run ./cmd/seo-pipeline $(TASK)
BINARY := bin/seo-pipeline
DRY_RUN_DATABASE_URL ?= postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable

# Namespaced task_1 syntax: make task-1 <operation> [article_id|limit].
TASK_OPERATION := $(word 2,$(MAKECMDGOALS))
TASK_ARG := $(word 3,$(MAKECMDGOALS))
TASK_EXTRA_ARGS := $(wordlist 4,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
TASK_OPERATIONS := import errors retry run dry-run prepare generate demo-generate article info review fix html result deepseek-login
OPTIONAL_ARGUMENT_OPERATIONS := errors retry run prepare generate demo-generate article info review fix html result

.PHONY: help task-1 docker-up docker-start docker-stop docker-down docker-restart docker-logs docker-ps
.PHONY: test test-race fmt vet lint lint-fix build

# ----------------------------------------------------
# Help
# ----------------------------------------------------

help: ## Show all available commands
	@printf 'SEO Pipeline commands\n\n'
	@printf 'task_1 operations:\n'
	@printf '  %-32s %s\n' 'make task-1 import [limit]' 'Import articles from Excel'
	@printf '  %-32s %s\n' 'make task-1 errors [EXTERNAL_ID]' 'Show articles with recorded errors'
	@printf '  %-32s %s\n' 'make task-1 retry [EXTERNAL_ID]' 'Retry failed articles through the demo flow'
	@printf '  %-32s %s\n' 'make task-1 run [ID]' 'Run pending articles or one article'
	@printf '  %-32s %s\n' 'make task-1 dry-run' 'Run the isolated local pipeline'
	@printf '  %-32s %s\n' 'make task-1 prepare [ID]' 'Collect pending research or process one article'
	@printf '  %-32s %s\n' 'make task-1 generate [ID]' 'Run pending full flows or process one article'
	@printf '  %-32s %s\n' 'make task-1 demo-generate [ID]' 'Resume the demo generation flow'
	@printf '  %-32s %s\n' 'make task-1 article [ID]' 'Generate pending article text/metadata or one article'
	@printf '  %-32s %s\n' 'make task-1 info [ID]' 'Generate pending article text/metadata or one article'
	@printf '  %-32s %s\n' 'make task-1 review [ID]' 'Review pending generated articles or one article'
	@printf '  %-32s %s\n' 'make task-1 fix [ID]' 'Fix pending reviewed articles or one article'
	@printf '  %-32s %s\n' 'make task-1 html [ID]' 'Generate pending HTML or process one article'
	@printf '  %-32s %s\n' 'make task-1 result [ID]' 'Build pending results or process one article'
	@printf '  %-32s %s\n' 'make task-1 run plan [ID]' 'Show where the pipeline would resume, without running it'
	@printf '  %-32s %s\n' 'make task-1 deepseek-login' 'Open Chromium for manual DeepSeek login'
	@printf '\nProject commands:\n'
	@awk 'BEGIN {FS = ":.*## "} /^(docker|test|fmt|vet|lint|build)[a-zA-Z0-9_-]*:.*## / {printf "  make %-27s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----------------------------------------------------
# task_1
# ----------------------------------------------------

task-1: ## Run a task_1 operation
	@if [ -z "$(TASK_OPERATION)" ]; then \
		printf 'Operation is required.\n\nExample:\n\nmake task-1 generate 37\n\nRun make help to see all task_1 operations.\n'; \
		exit 1; \
	fi
	@if ! printf ' %s ' "$(TASK_OPERATIONS)" | grep -Fq " $(TASK_OPERATION) "; then \
		printf 'Unknown task_1 operation: %s\n\nRun make help to see all task_1 operations.\n' "$(TASK_OPERATION)"; \
		exit 1; \
	fi
	@if [ -n "$(strip $(TASK_EXTRA_ARGS))" ] && \
		! { [ "$(TASK_OPERATION)" = 'run' ] && [ "$(TASK_ARG)" = 'plan' ] && [ $(words $(TASK_EXTRA_ARGS)) -eq 1 ]; }; then \
		printf 'Too many arguments.\n\nExample:\n\nmake task-1 $(TASK_OPERATION) $(TASK_ARG)\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'import' ] && [ -n "$(TASK_ARG)" ] && \
		! printf '%s' "$(TASK_ARG)" | grep -Eq '^[1-9][0-9]*$$'; then \
		printf 'Import limit must be a positive integer.\n\nExample:\n\nmake task-1 import 10\n'; \
		exit 1; \
	fi
	@if printf ' %s ' "$(OPTIONAL_ARGUMENT_OPERATIONS)" | grep -Fq " $(TASK_OPERATION) " && \
		[ -n "$(TASK_ARG)" ] && [ "$(TASK_ARG)" != 'plan' ] && \
		! printf '%s' "$(TASK_ARG)" | grep -Eq '^[1-9][0-9]*$$'; then \
		printf 'ID must be a positive integer.\n\nExample:\n\nmake task-1 $(TASK_OPERATION) 37\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'dry-run' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'dry-run does not accept arguments.\n\nExample:\n\nmake task-1 dry-run\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'deepseek-login' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'deepseek-login does not accept arguments.\n\nExample:\n\nmake task-1 deepseek-login\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'dry-run' ]; then \
		$(DOCKER_COMPOSE) up -d --wait postgres-dry-run && \
		$(GO) test ./... && \
		$(GO) vet ./... && \
		APP_ENV=test DRY_RUN_DATABASE_URL='$(DRY_RUN_DATABASE_URL)' $(CLI) run --dry-run; \
	elif [ "$(TASK_OPERATION)" = 'run' ] && [ "$(TASK_ARG)" = 'plan' ]; then \
		$(CLI) run --plan $(TASK_EXTRA_ARGS); \
	elif [ -n "$(TASK_ARG)" ]; then \
		$(CLI) $(TASK_OPERATION) "$(TASK_ARG)"; \
	else \
		$(CLI) $(TASK_OPERATION); \
	fi

# ----------------------------------------------------
# Docker
# ----------------------------------------------------

docker-up: ## Create, start, and wait for PostgreSQL
	$(DOCKER_COMPOSE) up -d --wait

docker-start: ## Start and wait for PostgreSQL services
	$(DOCKER_COMPOSE) up -d --wait

docker-stop: ## Stop PostgreSQL containers without removing them
	$(DOCKER_COMPOSE) stop

docker-down: ## Stop and remove PostgreSQL containers and network
	$(DOCKER_COMPOSE) down

docker-restart: ## Restart and wait for PostgreSQL services
	$(DOCKER_COMPOSE) restart
	$(DOCKER_COMPOSE) up -d --wait

docker-logs: ## Follow PostgreSQL service logs
	$(DOCKER_COMPOSE) logs --tail=100 --follow

docker-ps: ## Show PostgreSQL service status
	$(DOCKER_COMPOSE) ps

# ----------------------------------------------------
# Tests and code quality
# ----------------------------------------------------

test: ## Run all Go tests
	$(GO) test ./...

test-race: ## Run task_1 tests with the race detector
	$(GO) test -race ./internal/tasks/task1/...

fmt: ## Format all Go packages
	$(GO) fmt ./...

vet: ## Run Go static analysis
	$(GO) vet ./...

lint: ## Run golangci-lint using .golangci.yml
	$(GOLANGCI_LINT) run ./...

lint-fix: ## Run golangci-lint and apply the fixes it can make safely
	$(GOLANGCI_LINT) run --fix ./...

# ----------------------------------------------------
# Build
# ----------------------------------------------------

build: ## Build the CLI binary
	mkdir -p bin
	$(GO) build -o $(BINARY) ./cmd/seo-pipeline

# Treat operation and argument words as task-1 parameters, not separate targets.
%:
	@if [ "$(firstword $(MAKECMDGOALS))" = 'task-1' ] && \
		{ [ "$@" = "$(TASK_OPERATION)" ] || [ "$@" = "$(TASK_ARG)" ] || \
			{ [ "$(TASK_ARG)" = 'plan' ] && [ "$@" = "$(strip $(TASK_EXTRA_ARGS))" ]; }; }; then \
		:; \
	else \
		printf 'Unknown command: %s\n\nRun make help to see available commands.\n' "$@"; \
		exit 1; \
	fi
