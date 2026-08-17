SHELL := /bin/sh

.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
DOCKER_COMPOSE ?= docker compose
BINARY := bin/seo-pipeline
DRY_RUN_DATABASE_URL ?= postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable

# Namespaced syntax: make <task> <operation> [article_id|limit].
#
# Задачи различаются только именем: набор операций, разбор аргументов и рецепт у них общие,
# а пути, схему стадий и схему PostgreSQL выбирает профиль внутри CLI.
TASK_NAMES := task-1 pprof-1
TASK_NAME := $(firstword $(MAKECMDGOALS))
CLI = $(GO) run ./cmd/seo-pipeline $(TASK_NAME)

# Глобальный вход в сервисы: make login deepseek. Задаче не принадлежит.
LOGIN_SERVICE := $(word 2,$(MAKECMDGOALS))

TASK_OPERATION := $(word 2,$(MAKECMDGOALS))
TASK_ARG := $(word 3,$(MAKECMDGOALS))
TASK_EXTRA_ARGS := $(wordlist 4,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
TASK_OPERATIONS := import import-check errors retry run regenerate dry-run prepare generate demo-generate article info review fix html result clear reset google-login google-publish deepseek-login
OPTIONAL_ARGUMENT_OPERATIONS := import-check errors retry run regenerate clear google-publish prepare generate demo-generate article info review fix html result

.PHONY: help task-1 pprof-1 login docker-up docker-start docker-stop docker-down docker-restart docker-logs docker-ps
.PHONY: test test-race fmt vet lint lint-fix build

# ----------------------------------------------------
# Help
# ----------------------------------------------------

# help печатает команды обеих задач явно, а не через плейсхолдер задачи: из шаблона строку
# всё равно приходится собирать руками, и это первое, обо что спотыкаются.
#
# Выравнивает только левая колонка, и она вся латиницей. Это важно: printf на macOS считает
# ширину в байтах, поэтому кириллица в выравниваемом поле съезжает вдвое раньше нужного.
# Правая колонка идёт последней и не выравнивается вовсе.
help: ## этот список
	@printf 'SEO Pipeline. [ID] — external_id из Excel, необязателен: без него берутся все\n'
	@printf 'подходящие статьи. ID без скобок — обязателен.\n\n'
	@printf 'task_1\n'
	@printf '  %-34s%s\n' \
		'make task-1 import [limit]'      'импорт статей из Excel' \
		'make task-1 import-check [ID]'   'сверка импорта с Excel' \
		'make task-1 errors [ID]'         'статьи с текущей ошибкой' \
		'make task-1 prepare [ID]'        'research Keys.so и Arsenkin' \
		'make task-1 generate [ID]'       'генерация после prepare' \
		'make task-1 article [ID]'        'текст статьи и метаданные' \
		'make task-1 info [ID]'           'то же, что article' \
		'make task-1 review [ID]'         'проверка готовой статьи' \
		'make task-1 fix [ID]'            'правки по итогам review' \
		'make task-1 html [ID]'           'HTML из исправленной статьи' \
		'make task-1 run [ID]'            'полный прогон, возобновляемый' \
		'make task-1 run plan [ID]'       'где возобновится, без запуска' \
		'make task-1 retry [ID]'          'снять ошибку и прогнать' \
		'make task-1 regenerate ID'       'пересоздать статью целиком' \
		'make task-1 result [ID]'         'собрать result.md' \
		'make task-1 demo-generate [ID]'  'пересобрать каталог DEMO' \
		'make task-1 google-publish [ID]' 'промпты в Google Docs' \
		'make task-1 dry-run'             'офлайн-прогон без сервисов' \
		'make task-1 clear ID'            'статью к состоянию импорта' \
		'make task-1 reset'               'стереть состояние task_1'
	@printf '\npprof_1 — DeepSeek-only, три чата\n'
	@printf '  %-34s%s\n' \
		'make pprof-1 import [limit]'      'импорт статей из Excel' \
		'make pprof-1 import-check [ID]'   'сверка импорта с Excel' \
		'make pprof-1 errors [ID]'         'статьи с текущей ошибкой' \
		'make pprof-1 prepare [ID]'        'research Keys.so и Arsenkin' \
		'make pprof-1 generate [ID]'       'полный прогон, он же run' \
		'make pprof-1 article [ID]'        'чат 2 целиком' \
		'make pprof-1 info [ID]'           'чат 2 целиком, то же' \
		'make pprof-1 review [ID]'         'чат 2 целиком, то же' \
		'make pprof-1 fix [ID]'            'чат 2 целиком, то же' \
		'make pprof-1 html [ID]'           'чат 3: HTML и перелинковка' \
		'make pprof-1 run [ID]'            'полный прогон, возобновляемый' \
		'make pprof-1 run plan [ID]'       'где возобновится, без запуска' \
		'make pprof-1 retry [ID]'          'снять ошибку и прогнать' \
		'make pprof-1 regenerate ID'       'пересоздать статью целиком' \
		'make pprof-1 result [ID]'         'собрать result.md' \
		'make pprof-1 google-publish [ID]' 'промпты в Google Docs' \
		'make pprof-1 dry-run'             'офлайн-прогон без сервисов' \
		'make pprof-1 clear ID'            'статью к состоянию импорта' \
		'make pprof-1 reset'               'стереть состояние pprof_1'
	@printf '\nВход в сервисы — общий для задач\n'
	@printf '  %-34s%s\n' \
		'make login deepseek'              'ручной вход в DeepSeek' \
		'make login google'                'ручной вход в Google' \
		'make task-1 deepseek-login'       'алиас make login deepseek' \
		'make task-1 google-login'         'алиас make login google'
	@printf '\nПроект\n'
	@awk 'BEGIN {FS = ":.*## "} \
		/^(docker|test|fmt|vet|lint|build)[a-zA-Z0-9_-]*:.*## / {printf "  make %-29s%s\n", $$1, $$2}' \
		$(MAKEFILE_LIST)
	@printf '\nТратят деньги и ходят наружу: prepare, generate, article, info, review, fix,\n'
	@printf 'html, run, retry, regenerate, demo-generate, google-publish. Необратимы:\n'
	@printf 'clear и reset. У pprof_1 article, info, review и fix — одно действие: чат 2\n'
	@printf 'целиком; demo-generate не поддерживается.\n'
	@printf 'Перед первым запуском pprof-1 создать схему PostgreSQL — см. README.md\n'

# ----------------------------------------------------
# Задачи
# ----------------------------------------------------

# Рецепт один на все задачи: имя задачи подставляется в сообщения и в вызов CLI, а какие у
# неё каталоги, схема стадий и схема PostgreSQL — решает профиль внутри приложения. Новая
# задача добавляется именем в TASK_NAMES, целью-однострочником и профилем в internal/tasks.
define run_task_operation
	@if [ -z "$(TASK_OPERATION)" ]; then \
		printf 'Operation is required.\n\nExample:\n\nmake $(TASK_NAME) generate 37\n\nRun make help to see all $(TASK_NAME) operations.\n'; \
		exit 1; \
	fi
	@if ! printf ' %s ' "$(TASK_OPERATIONS)" | grep -Fq " $(TASK_OPERATION) "; then \
		printf 'Unknown $(TASK_NAME) operation: %s\n\nRun make help to see all $(TASK_NAME) operations.\n' "$(TASK_OPERATION)"; \
		exit 1; \
	fi
	@if [ -n "$(strip $(TASK_EXTRA_ARGS))" ] && \
		! { [ "$(TASK_OPERATION)" = 'run' ] && [ "$(TASK_ARG)" = 'plan' ] && [ $(words $(TASK_EXTRA_ARGS)) -eq 1 ]; }; then \
		printf 'Too many arguments.\n\nExample:\n\nmake $(TASK_NAME) $(TASK_OPERATION) $(TASK_ARG)\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'import' ] && [ -n "$(TASK_ARG)" ] && \
		! printf '%s' "$(TASK_ARG)" | grep -Eq '^[1-9][0-9]*$$'; then \
		printf 'Import limit must be a positive integer.\n\nExample:\n\nmake $(TASK_NAME) import 10\n'; \
		exit 1; \
	fi
	@if printf ' %s ' "$(OPTIONAL_ARGUMENT_OPERATIONS)" | grep -Fq " $(TASK_OPERATION) " && \
		[ -n "$(TASK_ARG)" ] && [ "$(TASK_ARG)" != 'plan' ] && \
		! printf '%s' "$(TASK_ARG)" | grep -Eq '^[1-9][0-9]*$$'; then \
		printf 'ID must be a positive integer.\n\nExample:\n\nmake $(TASK_NAME) $(TASK_OPERATION) 37\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'dry-run' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'dry-run does not accept arguments.\n\nExample:\n\nmake $(TASK_NAME) dry-run\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'deepseek-login' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'deepseek-login does not accept arguments.\n\nExample:\n\nmake $(TASK_NAME) deepseek-login\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'google-login' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'google-login does not accept arguments.\n\nExample:\n\nmake $(TASK_NAME) google-login\n'; \
		exit 1; \
	fi
	@if [ "$(TASK_OPERATION)" = 'reset' ] && [ -n "$(TASK_ARG)" ]; then \
		printf 'reset does not accept arguments.\n\nExample:\n\nmake $(TASK_NAME) reset\n'; \
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
endef

task-1: ## Run a task_1 operation
	$(run_task_operation)

pprof-1: ## Run a pprof_1 operation
	$(run_task_operation)

# ----------------------------------------------------
# Вход в сервисы (общий для всех задач)
# ----------------------------------------------------

login: ## ручной вход: make login deepseek
	@if [ -z "$(LOGIN_SERVICE)" ]; then \
		printf 'Service is required.\n\nExample:\n\nmake login deepseek\n'; \
		exit 1; \
	fi
	@$(GO) run ./cmd/seo-pipeline login "$(LOGIN_SERVICE)"

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

# Слова операции и аргумента — параметры задачи, а не отдельные цели. То же для сервиса у
# глобальной команды входа.
%:
	@if printf ' %s ' "$(TASK_NAMES)" | grep -Fq " $(TASK_NAME) " && \
		{ [ "$@" = "$(TASK_OPERATION)" ] || [ "$@" = "$(TASK_ARG)" ] || \
			{ [ "$(TASK_ARG)" = 'plan' ] && [ "$@" = "$(strip $(TASK_EXTRA_ARGS))" ]; }; }; then \
		:; \
	elif [ "$(TASK_NAME)" = 'login' ] && [ "$@" = "$(LOGIN_SERVICE)" ]; then \
		:; \
	else \
		printf 'Unknown command: %s\n\nRun make help to see available commands.\n' "$@"; \
		exit 1; \
	fi
