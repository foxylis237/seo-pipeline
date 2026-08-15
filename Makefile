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
TASK_OPERATIONS := import import-check errors retry run regenerate dry-run prepare generate demo-generate article info review fix html result clear reset deepseek-login
OPTIONAL_ARGUMENT_OPERATIONS := import-check errors retry run regenerate clear prepare generate demo-generate article info review fix html result

.PHONY: help task-1 docker-up docker-start docker-stop docker-down docker-restart docker-logs docker-ps
.PHONY: test test-race fmt vet lint lint-fix build

# ----------------------------------------------------
# Help
# ----------------------------------------------------

# help укладывается в 58 символов: команда слева, очень краткое описание справа. Список
# целиком должен быть виден в узком окне, иначе им не пользуются.
#
# Формат printf с двумя %s повторяется по списку аргументов, поэтому пары идут подряд. Длинные
# описания разбиты на строки прямо здесь, а не переносятся на ходу: системный awk на macOS
# считает длину в байтах, и кириллицу он переносил бы вдвое раньше нужного. Продолжение — это
# пара с пустой командой; выравнивание держит %-26s, ширина взята по самой длинной команде
# (task-1 demo-generate [ID], 25 символов).
help: ## этот список
	@printf 'SEO Pipeline\n\n'
	@printf 'task_1 — make <команда>\n'
	@printf '  %-26s%s\n' \
		'task-1 import [limit]'     'импорт статей из Excel' \
		'task-1 import-check [ID]'  'сверка импорта с Excel' \
		'task-1 errors [ID]'        'статьи с текущей ошибкой' \
		'task-1 run [ID]'           'полный прогон, возобновляемый' \
		'task-1 run plan [ID]'      'где возобновится, без запуска' \
		'task-1 retry [ID]'         'снять ошибку и прогнать' \
		'task-1 regenerate ID'      'пересоздать статью целиком' \
		'task-1 prepare [ID]'       'research Keys.so и Arsenkin' \
		'task-1 generate [ID]'      'генерация после prepare' \
		'task-1 article [ID]'       'текст статьи и метаданные' \
		'task-1 info [ID]'          'то же, что article' \
		'task-1 review [ID]'        'проверка готовой статьи' \
		'task-1 fix [ID]'           'правки по итогам review' \
		'task-1 html [ID]'          'HTML из исправленной статьи' \
		'task-1 result [ID]'        'собрать result.md' \
		'task-1 demo-generate [ID]' 'пересобрать каталог DEMO' \
		'task-1 clear ID'           'вернуть статью к состоянию' \
		''                          'сразу после импорта' \
		'task-1 reset'              'стереть всё состояние task_1' \
		'task-1 dry-run'            'офлайн-прогон без сервисов' \
		'task-1 deepseek-login'     'ручной вход в DeepSeek'
	@printf '\nПроект — make <цель>\n'
	@awk 'BEGIN {FS = ":.*## "} \
		/^(docker|test|fmt|vet|lint|build)[a-zA-Z0-9_-]*:.*## / {printf "  %-26s%s\n", $$1, $$2}' \
		$(MAKEFILE_LIST)
	@printf '\nПодробности по каждой операции — README.md\n'

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
	@if [ "$(TASK_OPERATION)" = 'reset' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'reset does not accept arguments.\n\nExample:\n\nmake task-1 reset\n'; \
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

docker-up: ## поднять PostgreSQL и дождаться
	$(DOCKER_COMPOSE) up -d --wait

docker-start: ## запустить и дождаться
	$(DOCKER_COMPOSE) up -d --wait

docker-stop: ## остановить контейнеры
	$(DOCKER_COMPOSE) stop

docker-down: ## удалить контейнеры и сеть
	$(DOCKER_COMPOSE) down

docker-restart: ## перезапустить и дождаться
	$(DOCKER_COMPOSE) restart
	$(DOCKER_COMPOSE) up -d --wait

docker-logs: ## следить за логами
	$(DOCKER_COMPOSE) logs --tail=100 --follow

docker-ps: ## состояние сервисов
	$(DOCKER_COMPOSE) ps

# ----------------------------------------------------
# Tests and code quality
# ----------------------------------------------------

test: ## все тесты
	$(GO) test ./...

test-race: ## тесты с race detector
	$(GO) test -race ./...

fmt: ## форматирование
	$(GO) fmt ./...

vet: ## статический анализ
	$(GO) vet ./...

lint: ## golangci-lint по .golangci.yml
	$(GOLANGCI_LINT) run ./...

lint-fix: ## golangci-lint с автоправками
	$(GOLANGCI_LINT) run --fix ./...

# ----------------------------------------------------
# Build
# ----------------------------------------------------

build: ## бинарник в bin/seo-pipeline
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
