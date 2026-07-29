# SEO Pipeline

SEO Pipeline — CLI-приложение на Go для импорта заданий на статьи, сбора SEO-данных и генерации файлов. Сейчас реализован только сценарий `task_1`.

Состояние статей и пути к артефактам хранятся в PostgreSQL. Keys.so и Arsenkin используются через Playwright, генерационные этапы — через настроенные LLM-провайдеры. Для безопасной локальной проверки предусмотрен изолированный dry-run без внешних запросов.

## Структура проекта

- `cmd/seo-pipeline/` — CLI, сборка зависимостей и запуск операций.
- `config/config.yaml` — маршрутизация LLM-этапов и параметры моделей.
- `input/task_1/input.xlsx` — единственный Excel по умолчанию для импорта `task_1`.
- `internal/config/` — загрузка и проверка конфигурации.
- `internal/integrations/` — интеграции Keys.so и Arsenkin.
- `internal/llm/` — LLM-клиенты, маршрутизация, таймауты и повторы.
- `internal/storage/` — подключение к PostgreSQL.
- `internal/tasks/task1/` — импортёр, repository, pipeline, файловые операции, сборка результата и валидатор.
- `migrations/` — SQL-миграции PostgreSQL.
- `tasks/task_1/prompts/` — промпты генерационных этапов.
- `tasks/task_1/templates/` — шаблон `result.md`.
- `tasks/task_1/output/` — создаваемые артефакты статей.

## Требования и конфигурация

Нужны Go 1.25 и PostgreSQL с применёнными миграциями. Для `prepare` требуется Playwright/Chromium и доступ к Keys.so и Arsenkin; для генерационных команд — настроенные LLM-провайдеры.

По умолчанию приложение ищет `.env` на один каталог выше корня проекта. Путь можно переопределить через `ENV_FILE`; старое имя `SEO_PIPELINE_ENV` также поддерживается. Переменные окружения имеют приоритет над `.env`.

| Переменная | Назначение |
|---|---|
| `DATABASE_URL` | Подключение к основной PostgreSQL. |
| `APP_ENV` | Окружение; dry-run разрешён только для `local` и `test`. |
| `DRY_RUN_DATABASE_URL` | Отдельная БД dry-run; по умолчанию `seo_dry_run` на `localhost:5433`. |
| `INPUT_FILE_PATH` | Excel импорта; по умолчанию `input/task_1/input.xlsx`. |
| `OUTPUT_DIR` | Каталог артефактов; по умолчанию `tasks/task_1/output`. |
| `GEMINI_API_KEY`, `GEMINI_MODEL` | Доступ и модель Gemini. |
| `KEYS_SO_EMAIL`, `KEYS_SO_PASSWORD` | Доступ к Keys.so. |
| `ARSENKIN_EMAIL`, `ARSENKIN_PASSWORD` | Доступ к Arsenkin. |
| `ARSENKIN_HEADLESS` | Headless-режим Arsenkin, по умолчанию `true`. |
| `LOG_LEVEL` | `debug`, `info`, `warn` или `error`. |
| `LOG_FORMAT` | `text` или `json`. |
| `ENV_FILE` | Явный путь к env-файлу. |

## Makefile

Makefile — короткая оболочка над существующим CLI, командами Go и Docker Compose. Все операции со статьями вызываются через namespace `task-1`: `make task-1 <operation> [argument]`. Например, `make task-1 generate 37`. Команды с обязательным ID завершаются понятной ошибкой, если ID отсутствует или не является положительным целым числом.

### Команды приложения

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `help` | Показывает все цели и описания. | Нет. | `make help` | `make -qp` не требуется; список формирует сам Makefile. |
| `task-1 import` | Импортирует первые две статьи из Excel по умолчанию. | Необязательный путь к другому Excel. | `make task-1 import` или `make task-1 import path/to/input.xlsx` | `go run ./cmd/seo-pipeline task-1 import [excel_path]` |
| `task-1 run` | Без ID атомарно обрабатывает все доступные `pending`-статьи demo-flow; с ID запускает demo-flow явно. | Необязательный ID. | `make task-1 run` или `make task-1 run 37` | `go run ./cmd/seo-pipeline task-1 run [external_id]` |
| `task-1 dry-run` | Поднимает тестовую БД, запускает тесты, vet и полный локальный pipeline на stub-данных. | Нет. | `make task-1 dry-run` | См. раздел «Dry-run». |
| `task-1 prepare` | Собирает Keys.so и Arsenkin research для статьи. | Обязательный ID. | `make task-1 prepare 37` | `go run ./cmd/seo-pipeline task-1 prepare 37` |
| `task-1 generate` | Запускает полный flow `structure → article/info → review → fix → html → result`. | Обязательный ID. | `make task-1 generate 37` | `go run ./cmd/seo-pipeline task-1 generate 37` |
| `task-1 demo-generate` | Запускает demo-flow `article → info → result`. | Обязательный ID. | `make task-1 demo-generate 37` | `go run ./cmd/seo-pipeline task-1 demo-generate 37` |
| `task-1 article` | Генерирует статью, затем metadata `info` в общем чате. | Обязательный ID. | `make task-1 article 37` | `go run ./cmd/seo-pipeline task-1 article 37` |
| `task-1 info` | Выполняет ту же объединённую операцию `article + info`. | Обязательный ID. | `make task-1 info 37` | `go run ./cmd/seo-pipeline task-1 info 37` |
| `task-1 review` | Проверяет уже сгенерированную статью. | Обязательный ID. | `make task-1 review 37` | `go run ./cmd/seo-pipeline task-1 review 37` |
| `task-1 fix` | Исправляет статью с использованием сохранённого review. | Обязательный ID. | `make task-1 fix 37` | `go run ./cmd/seo-pipeline task-1 fix 37` |
| `task-1 html` | Создаёт и публикует HTML статьи. | Обязательный ID. | `make task-1 html 37` | `go run ./cmd/seo-pipeline task-1 html 37` |
| `task-1 result` | Собирает `result.md` и после успешной публикации завершает статью. | Обязательный ID. | `make task-1 result 37` | `go run ./cmd/seo-pipeline task-1 result 37` |

Отдельной CLI-операции `structure` сейчас нет: структура создаётся внутри `generate`. Ограничение количества строк для `import` также отсутствует — второй аргумент этой команды является путём, а не числом.

### Docker

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `docker-up` | Запускает оба PostgreSQL-сервиса. | Нет. | `make docker-up` | `docker compose up -d` |
| `docker-down` | Останавливает и удаляет контейнеры сети проекта, сохраняя volumes. | Нет. | `make docker-down` | `docker compose down` |
| `docker-restart` | Перезапускает сервисы. | Нет. | `make docker-restart` | `docker compose restart` |
| `docker-logs` | Показывает последние 100 строк и продолжает следить за логами. | Нет. | `make docker-logs` | `docker compose logs --tail=100 --follow` |
| `docker-ps` | Показывает состояние сервисов. | Нет. | `make docker-ps` | `docker compose ps` |

`postgres` доступен на `localhost:5432`, `postgres-dry-run` — на `localhost:5433`. Dry-run-контейнер применяет `migrations/*.up.sql` при первичной инициализации своего volume. Отдельного migration runner и Makefile-цели миграции в проекте нет.

### Проверки и сборка

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `test` | Запускает все Go-тесты. | Нет. | `make test` | `go test ./...` |
| `test-race` | Запускает task1-тесты с race detector. | Нет. | `make test-race` | `go test -race ./internal/tasks/task1/...` |
| `fmt` | Форматирует все Go-пакеты. | Нет. | `make fmt` | `go fmt ./...` |
| `vet` | Запускает статический анализ Go. | Нет. | `make vet` | `go vet ./...` |
| `build` | Собирает `bin/seo-pipeline`. | Нет. | `make build` | `mkdir -p bin` и `go build -o bin/seo-pipeline ./cmd/seo-pipeline` |

## CLI без Makefile

CLI принимает группу `task-1`; совместимая форма `task_1` также поддерживается. `<external_id>` — положительное значение колонки `id` из Excel.

```bash
go run ./cmd/seo-pipeline task-1 import
go run ./cmd/seo-pipeline task-1 import path/to/input.xlsx
go run ./cmd/seo-pipeline task-1 run
go run ./cmd/seo-pipeline task-1 run <external_id>
go run ./cmd/seo-pipeline task-1 prepare <external_id>
go run ./cmd/seo-pipeline task-1 generate <external_id>
go run ./cmd/seo-pipeline task-1 demo-generate <external_id>
go run ./cmd/seo-pipeline task-1 article <external_id>
go run ./cmd/seo-pipeline task-1 info <external_id>
go run ./cmd/seo-pipeline task-1 review <external_id>
go run ./cmd/seo-pipeline task-1 fix <external_id>
go run ./cmd/seo-pipeline task-1 html <external_id>
go run ./cmd/seo-pipeline task-1 result <external_id>
```

При `Ctrl+C`, `SIGINT` или `SIGTERM` общий контекст отменяется, текущие операции прекращаются, а созданные ресурсы закрываются.

## Импорт

По умолчанию импорт читается из `input/task_1/input.xlsx`. Используется лист `Лист1`, а если его нет — первый лист книги. Обязательны колонки `id` и `article_name`. Поддерживаются `header`, `image_slug`, `meta_description`, `key_word`, `reference_url`, `category`, `authors`, `links`, `professions` и старая опечатка `referense_url`.

Текущий MVP читает первые две непустые строки. Новый `external_id` создаётся транзакционно; уже существующий ID пропускается без обновления.

## Pipeline

### Подготовка research

`prepare <external_id>` переводит статью в `processing`, не удаляя прежний успешный research и артефакты. Затем полностью собирает данные Keys.so и Arsenkin в памяти. Только после успеха всех обязательных внешних операций research атомарно заменяется через PostgreSQL upsert, старые metadata/output очищаются и `current_step` становится `structure_generation`. При ошибке интеграции прежние research, metadata и output сохраняются.

### Полная генерация

`generate <external_id>` выполняет:

1. `structure` — структура статьи.
2. `article` и `info` — текст и metadata в одном Gemini-чате.
3. `review` — проверка статьи.
4. `fix` — исправленная версия.
5. `html` — нормализованный HTML.
6. `result` — сборка `result.md` Go-шаблоном без отдельного LLM-вызова.

После HTML статья остаётся `processing` на `final_file_assembly`. Статус `completed` устанавливается только после успешной публикации `result.md`.

### Run и demo-flow

`run` без ID атомарно резервирует по возрастанию внутреннего ID только статьи со статусом `pending`, используя PostgreSQL `FOR UPDATE SKIP LOCKED`. Конкурентные процессы не получают одну статью. `failed`, `processing` и `completed` автоматически не выбираются. На ошибке текущей статьи запуск останавливается.

`run <external_id>` и `demo-generate <external_id>` явно запускают сокращённый flow `article → info → result` для выбранной статьи. Это остаётся способом точечного повторного запуска.

## Dry-run

Рекомендуемый безопасный запуск:

```bash
make task-1 dry-run
```

Точный эквивалент без Makefile:

```bash
docker compose up -d --wait postgres-dry-run
go test ./...
go vet ./...
APP_ENV=test DRY_RUN_DATABASE_URL='postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable' go run ./cmd/seo-pipeline task-1 run --dry-run
```

Dry-run разрешён только при `APP_ENV=local` или `APP_ENV=test` и только для БД, имя которой содержит `test`, `dry_run` или `dry-run`. Он читает настроенный Excel, использует локальные research-данные и детерминированные LLM-stub ответы, не вызывает Gemini, Keys.so, Arsenkin или Playwright. Перед запуском очищаются отдельная dry-run БД и `<OUTPUT_DIR>/dry-run`, поэтому данные повторных запусков не смешиваются.

## Статусы и этапы

Статусы `articles`:

- `pending` — импортирована и доступна обычному `run`;
- `processing` — зарезервирована или проходит pipeline;
- `completed` — `result.md` успешно опубликован;
- `failed` — текущая операция завершилась ошибкой.

Разрешённые значения `current_step`:

- `arsenkin_collection`;
- `arsenkin_cleanup`;
- `structure_generation`;
- `article_generation`;
- `article_review`;
- `metadata_generation`;
- `reading_time_calculation`;
- `html_generation`;
- `final_file_assembly`.

При `completed` этап равен `NULL`. При ошибке сохраняются текущий этап и `error_message`.

## Артефакты

Для статьи `external_id=37` и `image_slug=primer`:

```text
tasks/task_1/output/37-primer/
  prompts/
    structure_prompt.txt
    article_prompt.txt
    article_info_prompt.txt
    article_review_prompt.txt
    fix_article_prompt.txt
    article_html_prompt.txt
  generated/
    structure.txt
    article.txt
    article_info.txt
    review.txt
    fixed_article.txt
    generation_context.json
  article.html
  result.md
```

Файлы сначала полностью записываются во временные файлы в целевом каталоге, затем публикуются атомарным rename вместе с сохранением состояния stage. При ошибке сохраняется предыдущая опубликованная версия. Пути в PostgreSQL относительны к `OUTPUT_DIR`.

## PostgreSQL и миграции

Схема состоит из таблиц `articles`, `article_inputs`, `article_research`, `article_metadata` и `article_outputs`. Приложение проверяет ожидаемую схему перед выполнением команд, но не запускает миграции основной БД автоматически.

Миграции применяются по номеру:

1. `000001_create_articles`;
2. `000002_add_articles_external_id`;
3. `000003_add_wordstat_keywords`;
4. `000004_add_article_research_updated_at`;
5. `000005_add_article_review_stage`;
6. `000006_add_structured_article_metadata`;
7. `000007_add_result_input_fields`.

## Текущие ограничения

- Реализован только `task_1`.
- Импорт ограничен первыми двумя статьями и не обновляет существующие ID.
- `article` и `info` являются двумя именами одной объединённой операции.
- Отдельной CLI-команды только для `structure` нет.
- Автоматического retry для `failed` нет; повторный запуск выполняется явно.
- Повторный генерационный запуск снова обращается к LLM.
- Общей транзакции между всей файловой системой и всем pipeline нет; согласованность обеспечивается отдельно для каждого сохраняемого stage.
- Валидатор существует как внутренний пакет без отдельной CLI-команды.

## Проверка проекта без Makefile

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./internal/tasks/task1/...
go build -o bin/seo-pipeline ./cmd/seo-pipeline
```
