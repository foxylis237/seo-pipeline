# SEO Pipeline

SEO Pipeline — CLI-приложение на Go для импорта заданий на статьи, сбора SEO-данных и генерации файлов.

Реализованы две задачи. **`task_1`** — рабочий сценарий в проде. **`pprof_1`** — новый пайплайн генерации, который разрабатывается параллельно и на `task_1` не влияет. Движок у них общий, различаются они конфигурацией и порядком стадий: см. «Задачи `task_1` и `pprof_1`».

Состояние статей и пути к артефактам хранятся в PostgreSQL. Keys.so и Arsenkin используются через Playwright, генерационные этапы — через настроенные LLM-провайдеры. Для безопасной локальной проверки предусмотрен изолированный dry-run без внешних запросов.

## Структура проекта

- `cmd/seo-pipeline/` — CLI, сборка зависимостей и запуск операций.
- `config/config.yaml` — маршрутизация LLM-стадий `task_1` и параметры моделей.
- `config/config.deepseek.yaml` — наложение для режима `LLM_MODE=deepseek` (`task_1`).
- `config/pprof_1.yaml` — самостоятельная схема стадий `pprof_1`, без наложения.
- `input/task_1/input.xlsx` — Excel импорта. Общий у обеих задач: путь задаётся полем профиля, а не выводится из имени задачи, поэтому развести их позже можно одной строкой.
- `internal/config/` — загрузка и проверка конфигурации.
- `internal/integrations/keysso/`, `internal/integrations/arsenkin/`, `internal/integrations/google/` — интеграции через Playwright.
- `internal/llm/` — маршрутизация стадий, таймауты и повторы; `gemini/` и `deepseekweb/` — клиенты провайдеров.
- `internal/storage/` — подключение к PostgreSQL.
- `internal/tasks/` — тип `Profile`: всё, чем одна задача отличается от другой;
  - `task1/profile.go`, `pprof1/profile.go` — конфигурация конкретной задачи;
  - `pprof1/flow.go`, `pprof1/chat.go` — поток генерации `pprof_1` и минимальный контракт чата.
- `internal/pipeline/` — общий движок обеих задач:
  - `article/` — модели статьи, этапа, результата;
  - `importer/` — чтение Excel и отчёт импорта;
  - `repository/` — доступ к PostgreSQL и проверка схемы;
  - `generation/` — pipeline генерационных стадий;
  - `keywords/` — резервный подбор исходных запросов моделью;
  - `output/` — атомарная публикация артефактов;
  - `diagnostics/` — снимки prepare, отчёт проверок, логи по статьям;
  - `demo/` — сборка каталога `DEMO` для ручного прогона;
  - `result/` — сборка `result.md`;
  - `validator/` — проверки без отдельной CLI-команды.
- `migrations/` — SQL-миграции PostgreSQL.
- `tasks/task_1/prompts/` — промпты стадий `task_1`; `deepseek/` — версии для одного диалога, `demo/` — объединённый промпт ручного чата.
- `tasks/pprof_1/prompts/` — промпты `pprof_1` плоским списком, порядок виден по номерам.
- `tasks/<задача>/templates/` — шаблон `result.md`.
- `tasks/<задача>/output/` — создаваемые артефакты статей.
- `output/task1/`, `output/pprof_1/` — отчёты импорта (`import-reports/`) и диагностика неудачных попыток Keys.so, Arsenkin, DeepSeek и Google (`debug/`). У каждой задачи свои: `reset` одной не трогает данные другой.
- `docs/` — единый язык, ADR, аудит; `.claude/` — правила, хуки и скиллы Claude Code.

## Задачи `task_1` и `pprof_1`

Задача — это отдельный пайплайн со своими каталогами, промптами, схемой стадий и схемой PostgreSQL. Набор команд у задач общий: `make <task> <операция>`, где `<task>` — `task-1` или `pprof-1`.

Общий у них движок (`internal/pipeline`): импорт, репозиторий, атомарная публикация артефактов, диагностика, сборка `result.md`, интеграции Keys.so, Arsenkin и Google. Различается конфигурация — она живёт в `internal/tasks/task1` и `internal/tasks/pprof1`.

| Что | `task_1` | `pprof_1` |
|---|---|---|
| Провайдеры | Gemini + DeepSeek Web, схема выбирается на статью | **только DeepSeek Web** |
| Схемы стадий | `config/config.yaml` + наложение `config.deepseek.yaml` | `config/pprof_1.yaml`, одна |
| Стадии | `structure`, `article`, `info`, `review`, `fix`, `html` | `structure`, `expert`, `seo_editor`, `info`, `review`, `html` |
| Промпты | `tasks/task_1/prompts/` | `tasks/pprof_1/prompts/` |
| Артефакты | `tasks/task_1/output/` | `tasks/pprof_1/output/` |
| Схема PostgreSQL | `public` | `pprof_1` |
| Excel | `input/task_1/input.xlsx` | тот же файл |

### Поток генерации `pprof_1`: три чата

Границы между чатами значимы: первое сообщение каждого просит провайдера начать новую беседу, поэтому истории не смешиваются.

```text
Чат 1   structure                                  → generated/structure.txt
Чат 2   1_expert → 2_seo_editor → info → 3_review
          ↓          ↓             ↓       ↓
        article.txt  review.txt    метаданные  fixed_article.txt
Чат 3   4_html                                     → article.html
                                                   → result.md
```

Внутри чата 2 каждое следующее сообщение опирается на историю: промпты так и написаны и статью повторно не передают. Отдельного шага `fix` у `pprof_1` нет — промпт `3_review` возвращает уже исправленную статью, а перелинковку делает `4_html`.

Чат 2 неделим. Браузерная беседа не переживает завершения процесса, поэтому продолжить её посередине нечем: все четыре артефакта публикуются одним commit, а команды `article`, `info`, `review` и `fix` прогоняют весь чат целиком.

### Промпты `pprof_1`

```text
tasks/pprof_1/prompts/
  article.txt        базовый промпт статьи — артефакт и документ Google, в модель НЕ уходит
  keywords.txt       резервный подбор запросов для prepare
  structure.txt      чат 1
  1_expert.txt       чат 2: статью пишет практикующий специалист по структуре
  2_seo_editor.txt   чат 2: SEO-редактура ключами и LSI
  info.txt           чат 2: метаданные по статье из этого же чата
  3_review.txt       чат 2: ревью, возвращающее исправленную статью
  4_html.txt         чат 3: разметка и перелинковка
```

**Базовый `article.txt` в модель не отправляется.** Он по-прежнему собирается целиком из входных данных и research, сохраняется в `prompts/article_prompt.txt` и выгружается в Google Docs — в тот же документ `Промт: <заголовок>`, что и раньше. Текст статьи пишет стадия `expert`.

## Требования и конфигурация

Нужны Go 1.25 и PostgreSQL с применёнными миграциями. Для `prepare` требуется Playwright/Chromium и доступ к Keys.so и Arsenkin; для генерационных команд — настроенные LLM-провайдеры.

По умолчанию приложение ищет `.env` на один каталог выше корня проекта. Путь можно переопределить через `ENV_FILE`; старое имя `SEO_PIPELINE_ENV` также поддерживается. Переменные окружения имеют приоритет над `.env`.

| Переменная | Назначение |
|---|---|
| `DATABASE_URL` | Подключение к основной PostgreSQL. Общее для задач: разводит их `search_path` из профиля, а не отдельный DSN. |
| `APP_ENV` | Окружение; dry-run разрешён только для `local` и `test`. |
| `DRY_RUN_DATABASE_URL` | Отдельная БД dry-run; по умолчанию `seo_dry_run` на `localhost:5433`. |
| `TEST_DATABASE_URL` | БД для тестов `internal/pipeline/repository`. Без неё 15 тестов молча пропускаются, а `make test` остаётся зелёным. |
| `INPUT_FILE_PATH` | Excel импорта `task_1`; по умолчанию `input/task_1/input.xlsx`. |
| `OUTPUT_DIR` | Каталог артефактов `task_1`; по умолчанию `tasks/task_1/output`. |
| `PPROF_1_*` | Точечное переопределение `pprof_1`: `PPROF_1_OUTPUT_DIR`, `PPROF_1_INPUT_FILE_PATH`, `PPROF_1_DATABASE_URL`, `PPROF_1_DRY_RUN_DATABASE_URL`. |
| `LLM_MODE` | Режим маршрутизации `task_1`: пусто или `gemini` — обычная схема, `deepseek` — все стадии через DeepSeek Web. На `pprof_1` не влияет: у него одна схема. |
| `GEMINI_API_KEY`, `GEMINI_MODEL` | Доступ и модель Gemini. |
| `OPENROUTER_API_KEY`, `OPENROUTER_MODEL` | Доступ и модель OpenRouter. Провайдер объявлен в `config/config.yaml`, но ни одной стадии по умолчанию не назначен. |
| `KEYS_SO_EMAIL`, `KEYS_SO_PASSWORD` | Доступ к Keys.so. |
| `ARSENKIN_EMAIL`, `ARSENKIN_PASSWORD` | Доступ к Arsenkin. |
| `ARSENKIN_HEADLESS` | Headless-режим Arsenkin, по умолчанию `true`. |
| `LOG_LEVEL` | `debug`, `info`, `warn` или `error`. |
| `LOG_FORMAT` | `auto` (по умолчанию), `pretty`, `text` или `json`. См. «Логи». |
| `ENV_FILE` | Явный путь к env-файлу. |

LLM-стадия может содержать упорядоченный fallback `targets`. Старые поля `provider` и `model` остаются допустимы и означают один target:

```yaml
targets:
  - provider: gemini
    model: ${GEMINI_MODEL}
  - provider: openrouter
    model: ${OPENROUTER_MODEL}
```

### Первый локальный запуск

Конфигурация по умолчанию ожидается рядом с каталогом проекта, поэтому из корня свежего клона выполните:

```bash
cp .env.example ../.env
```

Для импорта с локальной PostgreSQL достаточно оставленных в примере `DATABASE_URL`, `INPUT_FILE_PATH` и `OUTPUT_DIR`. Перед `prepare` и генерацией заполните соответствующие учётные данные и ключи. Затем:

```bash
make docker-up
make task-1 import
```

Для безопасной проверки всего pipeline без внешних сервисов и платных API используйте `make task-1 dry-run`.

**Перед первым запуском `pprof_1`** нужно один раз создать его схему PostgreSQL и применить в неё миграции — автоматического migration runner в проекте нет:

```bash
docker exec -i seo-postgres psql -U seo -d seo -c 'CREATE SCHEMA IF NOT EXISTS pprof_1'
for f in migrations/*.up.sql; do
  docker exec -i seo-postgres psql -U seo -d seo -c 'SET search_path TO pprof_1' -f - < "$f"
done
```

То же самое понадобится в базе dry-run на `localhost:5433`, если планируется `make pprof-1 dry-run`. Пока схемы нет, любая команда `pprof-1` останавливается на проверке схемы — это ожидаемо и безопасно.

## Makefile

Makefile — короткая оболочка над существующим CLI, командами Go и Docker Compose. Операции запускаются через namespace задачи: `make <task> <operation> [аргумент]`, где `<task>` — `task-1` или `pprof-1`. Рецепт у задач один, поэтому набор команд, разбор аргументов и тексты ошибок у них совпадают.

Вход в сервисы вынесен из задач и стал общим: `make login deepseek` и `make login google`. Прежние формы `make <task> deepseek-login` и `make <task> google-login` сохранены как совместимые алиасы. У Keys.so и Arsenkin ручного входа нет — они логинятся автоматически по `KEYS_SO_*` и `ARSENKIN_*` из `.env`, и `make login keysso` отвечает именно этим.

В `task_1` `review` и `fix` выполняются в одном LLM-чате: `fix` вторым сообщением опирается на историю — статью и ответ ревью — и повторно их не получает. Это уменьшает размер запроса и снижает вероятность обрыва генерации. При отдельном запуске `fix` история восстанавливается из сохранённых `article.txt` и `review.txt` без дополнительного обращения к модели.

Для `prepare`, `generate`, `article`, `info`, `review`, `fix`, `html` и `result` действует общее правило: без ID команда последовательно обрабатывает по внутреннему `articles.id` все статьи, которым по состоянию PostgreSQL нужен этот этап; с ID — только указанную статью. Нулевой, отрицательный и некорректный ID отклоняется до запуска. Ошибка одной статьи в batch фиксируется в её статусе и логах, остальные статьи продолжают обрабатываться, а итоговый код остаётся ненулевым.

### Шпаргалка: все команды целиком

Без плейсхолдеров, копируются как есть. `37` и `45` — примеры `external_id` из Excel.

Пометки: **$** — тратит деньги на LLM, **→** — ходит во внешний сервис (Keys.so, Arsenkin, Google), **!** — необратимо.

#### `task_1`

```bash
# Импорт и сверка
make task-1 import                  # весь Excel
make task-1 import 10               # только первые 10 новых строк
make task-1 import-check            # сверить импорт с Excel
make task-1 import-check 37         # сверить одну статью

# Сбор research                     →
make task-1 prepare                 # все статьи без research
make task-1 prepare 37              # одна статья

# Генерация                         $
make task-1 generate                # все статьи, которым нужна генерация
make task-1 generate 37             # одна статья
make task-1 article                 # article + info одной операцией
make task-1 article 37
make task-1 info                    # то же самое, второе имя
make task-1 info 37
make task-1 review                  # ревью готовой статьи
make task-1 review 37
make task-1 fix                     # правки по ревью, продолжает тот же чат
make task-1 fix 37
make task-1 html                    # HTML из исправленной статьи
make task-1 html 37

# Полный прогон                     $ →
make task-1 run                     # все незавершённые статьи, с возобновлением
make task-1 run 37                  # одна статья
make task-1 run plan                # где возобновится, ничего не запуская
make task-1 run plan 37
make task-1 retry                   # снять ошибку и прогнать
make task-1 retry 37
make task-1 regenerate 37           # пересоздать статью целиком, research сохраняется

# Сборка результата
make task-1 result                  # собрать result.md всем, кому нужно
make task-1 result 37               # одной статье

# DEMO для ручного прогона          $
make task-1 demo-generate           # пересобрать DEMO всем статьям
make task-1 demo-generate 37        # одной статье

# Диагностика
make task-1 errors                  # статьи с текущей сохранённой ошибкой
make task-1 errors 37               # по одной статье
make task-1 dry-run                 # офлайн-прогон без внешних сервисов и денег

# Google Docs                       →
make task-1 google-publish          # промпты всех статей, у которых он сохранён
make task-1 google-publish 45       # одной статьи

# Очистка                           !
make task-1 clear 37                # вернуть одну статью к состоянию после импорта
make task-1 reset                   # стереть всё состояние task_1
```

#### `pprof_1`

Перед первым запуском — один раз создать схему PostgreSQL (см. «Первый локальный запуск»).

```bash
# Импорт и сверка
make pprof-1 import                 # весь Excel (файл общий с task_1)
make pprof-1 import 10              # только первые 10 новых строк
make pprof-1 import-check           # сверить импорт с Excel
make pprof-1 import-check 37        # сверить одну статью

# Сбор research                     →
make pprof-1 prepare                # все статьи без research
make pprof-1 prepare 37             # одна статья

# Генерация                         $
make pprof-1 generate               # полный прогон: у pprof_1 это то же, что run
make pprof-1 generate 37
make pprof-1 article                # чат 2 целиком: expert → seo_editor → info → review
make pprof-1 article 37
make pprof-1 info                   # то же самое, чат 2 целиком
make pprof-1 info 37
make pprof-1 review                 # то же самое, чат 2 целиком
make pprof-1 review 37
make pprof-1 fix                    # то же самое, чат 2 целиком
make pprof-1 fix 37
make pprof-1 html                   # чат 3: разметка и перелинковка
make pprof-1 html 37

# Полный прогон                     $ →
make pprof-1 run                    # все незавершённые статьи, с возобновлением
make pprof-1 run 37                 # одна статья
make pprof-1 run plan               # где возобновится, ничего не запуская
make pprof-1 run plan 37
make pprof-1 retry                  # снять ошибку и прогнать
make pprof-1 retry 37
make pprof-1 regenerate 37          # пересоздать статью целиком, research сохраняется

# Сборка результата
make pprof-1 result                 # собрать result.md всем, кому нужно
make pprof-1 result 37              # одной статье

# Диагностика
make pprof-1 errors                 # статьи с текущей сохранённой ошибкой
make pprof-1 errors 37              # по одной статье
make pprof-1 dry-run                # офлайн-прогон; требует схемы pprof_1 на 5433

# Google Docs                       →
make pprof-1 google-publish         # промпты всех статей, у которых он сохранён
make pprof-1 google-publish 45      # одной статьи

# Очистка                           !
make pprof-1 clear 37               # вернуть одну статью к состоянию после импорта
make pprof-1 reset                  # стереть состояние только pprof_1

# Не поддерживается
make pprof-1 demo-generate          # ошибка: DEMO собран вокруг ручного чата task_1
```

`article`, `info`, `review` и `fix` у `pprof_1` — четыре имени одного действия: каждая прогоняет чат 2 целиком, то есть четыре обращения к модели. Гранулярности меньше чата нет.

#### Общие команды

```bash
# Вход в сервисы                    →
make login deepseek                 # ручной вход в DeepSeek, откроет браузер
make login google                   # ручной вход в Google, откроет браузер
make task-1 deepseek-login          # алиас make login deepseek
make task-1 google-login            # алиас make login google
make pprof-1 deepseek-login         # тот же алиас
make pprof-1 google-login           # тот же алиас

# PostgreSQL
make docker-up                      # поднять оба сервиса и дождаться
make docker-start                   # то же самое
make docker-stop                    # остановить контейнеры
make docker-restart                 # перезапустить
make docker-ps                      # состояние сервисов
make docker-logs                    # следить за логами
make docker-down                    # удалить контейнеры и сеть, volumes сохраняются

# Проверки и сборка
make test                           # go test ./...
make test-race                      # go test -race ./...
make fmt                            # go fmt ./...
make vet                            # go vet ./...
make lint                           # golangci-lint
make lint-fix                       # golangci-lint с автоправками
make build                          # bin/seo-pipeline
make help                           # короткий список команд
```

### Команды приложения

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `help` | Список целей: команда слева, краткое описание справа. Ширина 58 символов, длинные описания перенесены внутри своей колонки. | Нет. | `make help` | Список формирует сам Makefile. |
| `task-1 import` | Импортирует все заполненные строки Excel. | Необязательный положительный лимит строк данных. | `make task-1 import` или `make task-1 import 10` | `go run ./cmd/seo-pipeline task-1 import [limit]` |
| `task-1 import-check` | Проверяет корректность импорта Excel перед запуском генерации: полноту переноса, уникальность `external_id`, совпадение полей и связь `articles ↔ article_inputs`. Данные не изменяет, внешние сервисы не вызывает. | Необязательный `external_id` из Excel. | `make task-1 import-check` или `make task-1 import-check 37` | `go run ./cmd/seo-pipeline task-1 import-check [external_id]` |
| `task-1 errors` | Показывает статьи с текущей сохранённой ошибкой. | Необязательный `external_id` из Excel. | `make task-1 errors` или `make task-1 errors 57` | `go run ./cmd/seo-pipeline task-1 errors [external_id]` |
| `task-1 run` | Полный pipeline `prepare → structure → article/info → review → fix → html → result` с возобновлением: готовые этапы пропускаются. Без ID берёт все статьи, кроме `completed`. | Необязательный ID; `plan` показывает точку возобновления, ничего не выполняя. | `make task-1 run`, `make task-1 run 37`, `make task-1 run plan` | `go run ./cmd/seo-pipeline task-1 run [--plan] [external_id]` |
| `task-1 retry` | Снимает сохранённую ошибку и проводит статью тем же полным раннером, что и `run`. | Необязательный `external_id` из Excel. | `make task-1 retry` или `make task-1 retry 57` | `go run ./cmd/seo-pipeline task-1 retry [external_id]` |
| `task-1 regenerate` | Сбрасывает результаты генерации одной статьи и проводит её заново полным pipeline. Research сохраняется. | **Обязательный** `external_id`. | `make task-1 regenerate 37` | `go run ./cmd/seo-pipeline task-1 regenerate <external_id>` |
| `task-1 clear` | Возвращает одну статью к состоянию сразу после импорта: удаляет research, metadata, outputs, историю ошибок и каталог статьи целиком. Строка в БД, её `id` и `external_id` сохраняются. | **Обязательный** `external_id`. | `make task-1 clear 23` | `go run ./cmd/seo-pipeline task-1 clear <external_id> [--yes]` |
| `task-1 reset` | Приводит `task_1` к состоянию «импорта ещё не было»: пустые таблицы, сброшенные счётчики, пустые каталоги вывода. | Нет. | `make task-1 reset` | `go run ./cmd/seo-pipeline task-1 reset [--yes]` |
| `task-1 dry-run` | Поднимает тестовую БД, запускает тесты, vet и полный локальный pipeline на stub-данных. | Нет. | `make task-1 dry-run` | См. раздел «Dry-run». |
| `task-1 prepare` | Собирает отсутствующий research Keys.so и Arsenkin. | Необязательный ID. | `make task-1 prepare` или `make task-1 prepare 37` | `go run ./cmd/seo-pipeline task-1 prepare [external_id]` |
| `task-1 generate` | Запускает требуемый полный flow `structure → article/info → review → fix → html → result`. | Необязательный ID. | `make task-1 generate` или `make task-1 generate 37` | `go run ./cmd/seo-pipeline task-1 generate [external_id]` |
| `task-1 demo-generate` | Пересобирает каталог `DEMO` из уже сохранённого состояния статьи. Без ID — для всех статей, включая `completed` и `failed`. | Необязательный ID. | `make task-1 demo-generate` или `make task-1 demo-generate 37` | `go run ./cmd/seo-pipeline task-1 demo-generate [external_id]` |
| `task-1 article` | Генерирует article и metadata `info` двумя отдельными вызовами. | Необязательный ID. | `make task-1 article` или `make task-1 article 37` | `go run ./cmd/seo-pipeline task-1 article [external_id]` |
| `task-1 info` | Выполняет ту же объединённую операцию `article + info`. | Необязательный ID. | `make task-1 info` или `make task-1 info 37` | `go run ./cmd/seo-pipeline task-1 info [external_id]` |
| `task-1 review` | Проверяет статьи с готовыми article и metadata без review. Открывает чат, который продолжит `fix`. | Необязательный ID. | `make task-1 review` или `make task-1 review 37` | `go run ./cmd/seo-pipeline task-1 review [external_id]` |
| `task-1 fix` | Исправляет статьи с готовым review без fixed article, продолжая чат ревью. | Необязательный ID. | `make task-1 fix` или `make task-1 fix 37` | `go run ./cmd/seo-pipeline task-1 fix [external_id]` |
| `task-1 html` | Создаёт отсутствующий HTML из готовой fixed article. | Необязательный ID. | `make task-1 html` или `make task-1 html 37` | `go run ./cmd/seo-pipeline task-1 html [external_id]` |
| `task-1 result` | Собирает отсутствующий `result.md` и завершает статью. | Необязательный ID. | `make task-1 result` или `make task-1 result 37` | `go run ./cmd/seo-pipeline task-1 result [external_id]` |
| `login google` | Удаляет сохранённый профиль, открывает видимый Chromium для ручного входа в Google и сохраняет новый persistent profile. Задаче не принадлежит. | Нет. | `make login google` | `go run ./cmd/seo-pipeline login google` |
| `task-1 google-publish` | Публикует уже сохранённый промпт статьи в Google Docs и запоминает адрес документа для `result.md`. LLM, Keys.so и Arsenkin не вызываются. Без ID обрабатывает все статьи, у которых промпт сохранён. | Необязательный `external_id`. | `make task-1 google-publish 45` или `make task-1 google-publish` | `go run ./cmd/seo-pipeline task-1 google-publish [external_id]` |
| `login deepseek` | Удаляет сохранённый профиль, открывает Chromium для ручного входа в DeepSeek и сохраняет новый persistent profile. Задаче не принадлежит. | Нет. | `make login deepseek` | `go run ./cmd/seo-pipeline login deepseek` |

Отдельной CLI-операции `structure` нет: структура создаётся внутри `generate`.

### Docker

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `docker-up` | Запускает оба PostgreSQL-сервиса и ждёт готовности. | Нет. | `make docker-up` | `docker compose up -d --wait` |
| `docker-start` | Запускает сервисы и ждёт готовности PostgreSQL. | Нет. | `make docker-start` | `docker compose up -d --wait` |
| `docker-stop` | Останавливает контейнеры, не удаляя их. | Нет. | `make docker-stop` | `docker compose stop` |
| `docker-down` | Останавливает и удаляет контейнеры сети проекта, сохраняя volumes. | Нет. | `make docker-down` | `docker compose down` |
| `docker-restart` | Перезапускает сервисы и ждёт готовности. | Нет. | `make docker-restart` | `docker compose restart`, затем `docker compose up -d --wait` |
| `docker-logs` | Показывает последние 100 строк и продолжает следить за логами. | Нет. | `make docker-logs` | `docker compose logs --tail=100 --follow` |
| `docker-ps` | Показывает состояние сервисов. | Нет. | `make docker-ps` | `docker compose ps` |

Для обычной ежедневной паузы используйте `make docker-stop`, а для продолжения — `make docker-start`: контейнеры при этом сохраняются. `make docker-down` удаляет контейнеры и сеть, но не именованные volumes с данными; следующий `make docker-up` создаст контейнеры заново.

`postgres` доступен на `localhost:5432`, `postgres-dry-run` — на `localhost:5433`. Оба контейнера применяют `migrations/*.up.sql` при первой инициализации своих volumes. Отдельного migration runner и Makefile-цели миграции в проекте нет; уже существующий volume автоматически повторно не мигрируется.

### Проверки и сборка

| Цель | Что делает | Параметр | Пример | Эквивалент без Makefile |
|---|---|---|---|---|
| `test` | Запускает все Go-тесты. | Нет. | `make test` | `go test ./...` |
| `test-race` | Запускает все тесты с race detector. | Нет. | `make test-race` | `go test -race ./...` |
| `fmt` | Форматирует все Go-пакеты. | Нет. | `make fmt` | `go fmt ./...` |
| `vet` | Запускает статический анализ Go. | Нет. | `make vet` | `go vet ./...` |
| `lint` | Запускает golangci-lint по `.golangci.yml`. | Нет. | `make lint` | `golangci-lint run ./...` |
| `lint-fix` | Запускает golangci-lint и применяет безопасные автоправки. | Нет. | `make lint-fix` | `golangci-lint run --fix ./...` |
| `build` | Собирает `bin/seo-pipeline`. | Нет. | `make build` | `mkdir -p bin` и `go build -o bin/seo-pipeline ./cmd/seo-pipeline` |

`make test` зелёный и без `TEST_DATABASE_URL`, но тесты репозитория при этом пропускаются. Перед сдачей проверяйте, что они действительно выполнились.

## CLI без Makefile

CLI принимает имя задачи первым аргументом: `task-1` или `pprof-1`; подчёркнутые формы `task_1` и `pprof_1` тоже поддерживаются. `<external_id>` — положительное значение колонки `id` из Excel.

Вход в сервисы живёт вне задач:

```bash
go run ./cmd/seo-pipeline login deepseek
go run ./cmd/seo-pipeline login google
```

```bash
go run ./cmd/seo-pipeline task-1 import
go run ./cmd/seo-pipeline task-1 import 10
go run ./cmd/seo-pipeline task-1 import-check
go run ./cmd/seo-pipeline task-1 run
go run ./cmd/seo-pipeline task-1 run <external_id>
go run ./cmd/seo-pipeline task-1 run --plan
go run ./cmd/seo-pipeline task-1 regenerate <external_id>
go run ./cmd/seo-pipeline task-1 retry
go run ./cmd/seo-pipeline task-1 prepare
go run ./cmd/seo-pipeline task-1 generate
go run ./cmd/seo-pipeline task-1 demo-generate
go run ./cmd/seo-pipeline task-1 article <external_id>
go run ./cmd/seo-pipeline task-1 info <external_id>
go run ./cmd/seo-pipeline task-1 review <external_id>
go run ./cmd/seo-pipeline task-1 fix <external_id>
go run ./cmd/seo-pipeline task-1 html <external_id>
go run ./cmd/seo-pipeline task-1 result <external_id>
go run ./cmd/seo-pipeline task-1 clear <external_id>
go run ./cmd/seo-pipeline task-1 reset --yes
go run ./cmd/seo-pipeline task-1 google-login
go run ./cmd/seo-pipeline task-1 google-publish <external_id>
go run ./cmd/seo-pipeline task-1 deepseek-login
```

Флаг `--yes` поддерживается только для `reset` и `clear`.

При `Ctrl+C`, `SIGINT` или `SIGTERM` общий контекст отменяется, текущие операции прекращаются, а созданные ресурсы закрываются.

## Импорт

По умолчанию импорт читается из `input/task_1/input.xlsx`. Используется лист `Лист1`, а если его нет — первый лист книги. Обязательны колонки `id`, `article_name`, `image_slug` и `reference_url`. Поддерживаются `header`, `meta_description`, `key_word`, `category`, `authors`, `links`, `professions` и старая опечатка `referense_url`.

```bash
make task-1 import
```

Импортирует все заполненные строки Excel.

```bash
make task-1 import 10
```

Импортирует первые 10 новых валидных статей. Уже существующие `external_id`, пустые и некорректные строки в лимит не входят. Если новых статей меньше, импорт завершается в конце файла. Нулевой, отрицательный или некорректный лимит отклоняется до начала импорта.

Excel всегда просматривается сверху вниз, а источником истины остаётся PostgreSQL. Новый `external_id` создаётся атомарно через `INSERT ... ON CONFLICT DO NOTHING`; уже существующая статья пропускается без обновления её полей, статуса или результатов обработки.

Для прохождения полного task1 обязательны непустые `id`, `article_name`, `image_slug` и `reference_url`. Значение `NULL` без учёта регистра также считается пустым. Ошибки отдельных строк не останавливают остальные строки; дубликаты `external_id` после первой корректной строки Excel отмечаются как ошибки файла.

После каждого запуска создаются два JSON-отчёта:

- `output/task1/import-reports/import-<timestamp>.json` — исторический отчёт запуска;
- `output/task1/import-reports/latest.json` — полный отчёт последнего запуска.

Отчёт содержит время запуска, входной файл, лимит, количество просмотренных, импортированных, существующих, некорректных и пустых строк, признак достижения лимита, построчные ошибки и фатальную инфраструктурную ошибку при её наличии. Оба файла публикуются атомарно.

`make task-1 import-check` сверяет результат импорта с Excel, ничего не меняя: полноту переноса, уникальность `external_id`, совпадение полей и связь `articles ↔ article_inputs`.

## Pipeline

### Источник исходных запросов

Исходные запросы для статьи берутся из первого доступного источника, в таком порядке:

1. **Ручное заполнение.** Если в `article_research.cleaned_keywords` статьи уже лежат запросы, этап Keys.so пропускается целиком. В логах и в `prepare/keysso.json` источник помечается как `manual`.
2. **Keys.so.** Обычный сбор по `reference_url` конкурента с последующей очисткой дубликатов.
3. **Резервный подбор моделью.** Срабатывает только тогда, когда Keys.so отработал штатно, но не дал ни одного исходного запроса. За это отвечает стадия `keywords` в `config/config.yaml` — та же маршрутизация и тот же Router, что у генерационных стадий. Результат проходит через ту же очистку Keys.so, что и обычный сбор, поэтому `cleaned_keywords` остаётся результатом Keys.so независимо от источника.

Таймаут, отказ авторизации и сломанная навигация Keys.so **не** переключают источник: их повтор осмыслен, а подмена источника скрыла бы поломку интеграции.

Резервный подбор отдаёт не более 49 запросов и оставляет только фразы из букв, цифр и пробелов. Ограничение не косметическое: операторные символы (`/`, `-`) роняют задачу Wordstat молча — форма принимает список, а задача не создаётся вовсе.

Частотностей у этого источника нет и быть не должно. Единственный источник частотностей в пайплайне — Wordstat на этапе Arsenkin, и колонка `wordstat_keywords` заполняется только оттуда.

### Подготовка research

`prepare [external_id]` переводит выбранную статью в `processing`, не удаляя прежний успешный research и артефакты. Без ID выбираются только незавершённые статьи на research-этапе без полного `competitor_structure`. Затем полностью собираются данные Keys.so и Arsenkin в памяти. Только после успеха всех обязательных внешних операций research атомарно заменяется через PostgreSQL upsert, старые metadata/output очищаются и `current_step` становится `structure_generation`. При ошибке интеграции прежние research, metadata и output сохраняются.

Arsenkin выполняет Wordstat и Copywriters в одном browser context. В форму Wordstat уходит не более 49 запросов, лишние отбрасываются с записью в лог. Возвращённые фразы проверяются на принадлежность отправленному списку — результат проверки попадает в `prepare/prepare-report.json` как `wordstat_membership`.

При ошибках Playwright интеграция Keys.so различает `no_data`, `maintenance`, `navigation_error`, `timeout` и `unexpected_page`. Текущая причина хранится в `articles.error_message`, история — в `article_errors`. Диагностика каждой неудачной попытки сохраняется в `output/task1/debug/keysso/article-<id>/<timestamp>-attempt-<n>/`: полноэкранный `screenshot.png`, текущий DOM `page.html` и безопасный `info.json` без cookies и заголовков авторизации. Аналогичная диагностика Arsenkin — в `output/task1/debug/arsenkin/`. `no_data` является окончательным ответом и не повторяется; технические и навигационные ошибки используют ограниченный retry.

### Полная генерация

`generate [external_id]` выполняет:

1. `structure` — структура статьи.
2. `article` и `info` — текст и metadata в общей логической беседе выбранного LLM-провайдера.
3. `review` — проверка статьи.
4. `fix` — исправленная версия.
5. `html` — нормализованный HTML.
6. `result` — сборка `result.md` Go-шаблоном без отдельного LLM-вызова.

После HTML статья остаётся `processing` на `final_file_assembly`. Статус `completed` устанавливается только после успешной публикации `result.md`.

### DeepSeek Web

Провайдер `deepseek_web` использует бесплатный веб-интерфейс `chat.deepseek.com` через уже подключённый Playwright. API DeepSeek, пароль и автоматизация авторизации не используются. Профиль Chromium хранится локально в `data/browser/deepseek/` и исключён из Git.

Первый вход выполняется вручную:

```bash
make task-1 deepseek-login
```

Команда всегда начинает с чистого состояния: сохранённый профиль удаляется до запуска браузера, поэтому вход выполняется заново, а не переиспользует прежние cookies. Если профилем в этот момент пользуется другой процесс, команда откажется работать вместо порчи чужой сессии.

Дальше открывается видимый Chromium со страницей входа. В нём пользователь самостоятельно проходит логин, CAPTCHA, подтверждение почты и другие проверки. Когда DeepSeek Chat становится доступен, браузер корректно закрывается, а новый persistent profile сохраняется.

Вместе с профилем удаляется и маркер паузы после блокировки (`.seo-pipeline.blocked`): ручной вход — это и есть вмешательство, после которого пауза больше не нужна.

### Проверка Cloudflare и капча

Проверка приходит и посреди работы: сообщение уходит, а вместо ответа страница показывает заглушку, причём поле ввода остаётся на месте. Это **не** блокировка аккаунта и не истёкшая авторизация.

В таком случае прогон не считает аккаунт заблокированным, не пишет паузу и не трогает профиль. Вместо этого открывается **видимое окно Chromium с тем же профилем** — в нём нужно пройти проверку руками. После этого окно закрывается само, стадия повторяется на уже проверенном профиле, и прогон продолжается без перезапуска команды.

Если проверку не прошли за отведённое время или окно закрыли, операция останавливается с ошибкой `DeepSeek requires manual captcha verification`. Автоматических повторов против капчи не делается.

### Два режима LLM (`task_1`)

Провайдеры внутри одной статьи не смешиваются. Режим выбирается переменной `LLM_MODE` при запуске:

| `LLM_MODE` | Конфигурация | Схема |
|---|---|---|
| не задан или `gemini` | автоматический выбор на каждую статью | `structure`, `info`, `html` → DeepSeek; `article`, `review`, `fix` → только Gemini, без резерва |
| `deepseek` | `config/config.yaml` + наложенный `config/config.deepseek.yaml` | все шесть стадий → DeepSeek, **одна беседа на статью** |

**Режим выбирается один раз на статью и внутри неё не меняется.** Пробных запросов к Gemini не делается: доступность выясняется только по реальному ответу модели.

Если Gemini возвращает исчерпание квоты или оплаты, он выключается на **24 часа**, а текущая статья **переделывается целиком** через DeepSeek-only — смешанного режима внутри одной статьи не возникает. Все следующие статьи сразу идут через DeepSeek. По истечении суток отметка снимается, и очередная статья снова пробует Gemini.

Состояние хранится в `data/llm/gemini-unavailable` — это состояние провайдера, а не статьи, поэтому его нет в PostgreSQL. Чтобы вернуть Gemini досрочно, файл достаточно удалить. Прочие ошибки Gemini (5xx, отказ авторизации, ошибки разбора) провайдера не выключают.

```bash
LLM_MODE=deepseek make task-1 run
```

DeepSeek-only режим включается, когда у Gemini заканчиваются токены. Все стадии статьи идут одним чатом: `structure → article → info → review → fix → html`. Новая беседа открывается только при смене статьи, поэтому промпты в `tasks/task_1/prompts/deepseek/` опираются на историю диалога и не пересылают результат предыдущего этапа повторно. За это отвечает флаг провайдера `single_chat_per_article`.

Резервный подбор запросов из стадии `keywords` в эту беседу не входит: он идёт с нулевым `article_id`, чтобы не занять первым ходом диалог, на историю которого опираются стадии генерации.

`config/config.deepseek.yaml` — файл отличий, а не вторая копия конфигурации: в нём только провайдер стадии, путь к промпту и сам флаг. Таймауты, температуры и `max_tokens` живут в одном месте, в базовом файле.

Чтобы выбрать веб-провайдер вручную, укажите его в `targets` нужных стадий (значение `model` используется как метка в логах и результатах; моделью управляет сам веб-интерфейс):

```yaml
targets:
  - provider: deepseek_web
    model: deepseek-chat
```

После этого обычная команда `make task-1 generate [ID]` использует тот же pipeline и те же промпты. Клиент открывает сохранённый persistent profile, создаёт новый DeepSeek Chat, отправляет промпт и ждёт новый непустой ответ. Завершение определяется без `sleep`: видимый индикатор генерации должен исчезнуть, а текст последнего ответа — перестать изменяться. Общий LLM-router применяет настроенный stage-timeout, до трёх попыток для временных браузерных ошибок и `slog`-логирование. При истёкшей сессии возвращается ошибка `DeepSeek session expired. Run deepseek-login.` без повторных попыток.

### Публикация промпта в Google Docs

Как только пайплайн собрал промпт статьи и сохранил его в `prompts/article_prompt.txt`, тот же текст выгружается в Google Docs — в папку [Статьи ДПО ПРОФ](https://drive.google.com/drive/folders/1N-NRlswacwqKWUOEiA1OS3tKT_V_yLiS). Имя документа:

```text
Промт: <название статьи>
```

Публикуется **уже готовый промпт** — та самая строка, которую роутер отправил модели и writer записал в файл. Промпт не собирается второй раз, дополнительных обращений к LLM не делается.

Документ с таким именем ищется до создания: если он есть — содержимое заменяется целиком, если нет — создаётся новый. Версий, копий `(1)` и истории не остаётся, после публикации существует ровно один документ с актуальным текстом.

**Первый вход выполняется вручную:**

```bash
make task-1 google-login
```

Команда удаляет сохранённый профиль, открывает видимый Chromium на папке Drive и ждёт, пока вы войдёте. Логин, пароль, CAPTCHA и 2FA не автоматизируются и не обходятся. После входа обычные прогоны используют сохранённую сессию.

Профиль лежит в `data/browser/google/` рядом с профилями Arsenkin и DeepSeek, исключён из Git и защищён `flock`: две одновременные публикации не испортят его LevelDB.

Вход открывается в **установленном Chrome**, а не в связанном с Playwright Chromium, и с флагом `--disable-blink-features=AutomationControlled`. Иначе Google отказывает во входе с сообщением «Возможно, этот браузер или приложение небезопасны»: он видит признак автоматизации ещё до формы логина. Если Chrome не установлен, запуск откатывается на Chromium сам — но вход из него проходит хуже.

**Повторная публикация без генерации:**

```bash
make task-1 google-publish 45
```

Берёт `prompts/article_prompt.txt` статьи и создаёт или перезаписывает документ. Ни LLM, ни Keys.so, ни Arsenkin не вызываются.

**Что происходит при отсутствии или истечении сессии.** Публикация останавливается сразу, без повторов: следующая попытка упёрлась бы в ту же страницу входа. В логе появляется `needs_manual_login=true` и подсказка `make task-1 google-login`. То же самое при CAPTCHA и 2FA — обходить их приложение не пытается. Локальный `article_prompt.txt` при любой ошибке Google остаётся нетронутым.

**Retry.** Временные отказы Google и Playwright повторяются до 3 раз с растущей паузой (2 с, 4 с, дальше не больше 15 с). Каждая попытка поднимает браузер заново — половина отказов Playwright лечится только новым контекстом. Не повторяются: истёкшая сессия, отсутствие входа, CAPTCHA, 2FA, занятый профиль и отмена команды. Каждая попытка пишется в `slog` с `article_id`, `external_id`, `stage`, `attempt`, `duration_ms`, `retryable` и причиной. Диагностика неудач — `output/task1/debug/google/article-<id>/`: `screenshot.png`, `page.html` с вычищенными адресами почты и `info.json` без cookies.

**Ссылка в result.md.** После успешной публикации адрес документа сохраняется в `article_outputs.google_doc_url`, и `result.md` печатает его последним разделом:

````markdown
## Гугл Док

```text
https://docs.google.com/document/d/AbC123/edit
```
````

Раздел выводится всегда. Если публикация не проходила или не удалась, он остаётся на месте с пустым значением — состав разделов `result.md` не зависит от того, дошла ли публикация. Перед сборкой `result.md` пайплайн дожидается очереди публикаций: генерация к этому моменту закончена, задерживать нечего. Если ссылка появилась позже, вернуть её в файл можно повторной сборкой — `make task-1 result <external_id>`.

**В `pprof_1` публикуется тот же базовый промпт статьи.** Он собирается из входных данных и research, сохраняется в `prompts/article_prompt.txt` и уходит в тот же документ `Промт: <заголовок>`, но в модель не отправляется: текст статьи пишет стадия `expert`. Очередь публикации у задач общая — за profile-каталогом Chromium держится `flock`, и вторая очередь конфликтовала бы с первой.

**Публикация не задерживает генерацию.** Она уходит в отдельную goroutine сразу после сохранения промпта, а стадия `info` продолжается параллельно. Задания выполняются по одному — за профилем держится `flock`. Перед выходом команда дожидается очереди, поэтому фоновых Chromium после завершения не остаётся; отмена по `Ctrl+C` доходит и до публикации. Ошибка публикации не роняет генерацию: за неё уже заплачено, и в статус статьи она не попадает.

### Run, retry и regenerate

`run`, `retry` и `regenerate` используют один и тот же раннер полного пайплайна с возобновлением: `prepare → structure → article/info → review → fix → html → result`. Готовые этапы пропускаются — признаком служит сохранённый артефакт, поэтому существующие research и файлы Structure/Article/Review/Fix/HTML не удаляются и не переделываются. Статья получает `completed` только после `html` и сборки `result.md`.

`run` без ID берёт все статьи со `status <> 'completed'` по возрастанию внутреннего `articles.id`. Ошибка одной статьи не прекращает обход: она записывается в её статус и логи, batch продолжается, а процесс завершается ненулевым кодом. Отмена по сигналу останавливает обход сразу.

`run <external_id>` проводит тем же полным пайплайном одну указанную статью.

`make task-1 run plan [ID]` показывает, с какого этапа пайплайн возобновится, ничего не выполняя.

`retry` снимает сохранённую ошибку и повторяет статью. Записи без текущей ошибки и уже `completed` пропускаются. Ошибка очищается непосредственно перед повтором конкретной статьи; при новом сбое новая ошибка снова сохраняется штатной логикой пайплайна.

`regenerate <external_id>` требует ID и пересоздаёт одну статью целиком: сбрасывает состояние генерации в БД, удаляет сгенерированные файлы и проводит статью заново. Импортированные данные и research Keys.so/Arsenkin сохраняются, поэтому `prepare` будет пропущен как готовый. После прогона результат проверяется: статус `completed`, непустой `html_path` и существующий `result.md`; иначе команда завершается ошибкой с перечнем несоответствий.

```bash
make task-1 errors        # все статьи с сохранёнными ошибками
make task-1 errors 57     # ошибка конкретной статьи
make task-1 retry         # повторить все неуспешные
make task-1 retry 57      # повторить одну
make task-1 regenerate 57 # пересоздать одну с нуля
```

Число — это `external_id` из Excel.

### Каталог DEMO

`make task-1 demo-generate [ID]` собирает каталог `DEMO` внутри каталога статьи из уже сохранённого состояния. Команда **не двигает статью по пайплайну**: она не меняет статус, `current_step` и `error_message`. Без ID DEMO пересобирается для всех статей, включая `completed` и `failed`; ошибка одной статьи не прекращает обход, но возвращается наружу.

Недостающие артефакты не блокируют сборку: чего нет в БД, то генерируется на месте, а чего не удалось получить — пропускается с предупреждением в логе. Раскладка подпапок повторяет боевой каталог статьи.

В корне `DEMO` лежит ровно то, что открывают руками при ручном прогоне: готовый `result.md` и два промпта ручного чата — `article_prompt.txt` и `fix_links_html_prompt.txt`. Второй собирается из `tasks/task_1/prompts/demo/fix_links_html.txt` — это объединённый промпт второго сообщения, существующий только для DEMO; боевые промпты он не подменяет.

### Очистка одной статьи

`make task-1 clear <external_id>` возвращает одну статью к состоянию сразу после импорта — как будто она ни разу не генерировалась. Число — это `external_id` из Excel, а не внутренний `articles.id`; отчёт печатает оба номера рядом, чтобы очистка не ушла в соседнюю строку.

Удаляется всё, что появилось после импорта:

- строки `article_research`, `article_metadata`, `article_outputs` и `article_errors` этой статьи;
- статус возвращается в `pending`, `current_step` и `error_message` — в `NULL`;
- каталог статьи целиком, вместе с `prepare/`, `logs/`, `prompts/`, `generated/`, `DEMO/`, `article.html` и `result.md`.

Логи прошлых прогонов удаляются вместе с остальным намеренно. У ни разу не запускавшейся статьи каталога нет вовсе, а уцелевший `logs/demo-generate.log` со строкой `status=completed` описывал бы состояние, которого после очистки уже нет. Если история прогонов нужнее — это одна строка в `clearKeepsSubdirectories` (`internal/pipeline/output/clear.go`), логика не меняется.

Сохраняется место статьи в базе: строка `articles` с прежними `id` и `external_id` и её `article_inputs`. Повторный импорт после очистки не нужен — статью сразу берёт `make task-1 run <external_id>`.

Как и `reset`, команда печатает отчёт с фактическим масштабом удаления и требует ввести слово `clear`. Без терминала нужен `--yes`. Порядок тот же: сначала коммит в БД, потом файлы, поэтому команда идемпотентна и сбой на файлах чинится повторным запуском.

Отличие от соседних команд: `regenerate` сохраняет research и сразу запускает пайплайн заново, `reset` чистит всю базу целиком, а `clear` трогает одну статью и ничего не запускает.

### Сброс состояния

`make task-1 reset` приводит `task_1` к состоянию «импорта ещё не было»: очищает таблицы, сбрасывает счётчики идентификаторов и опустошает каталоги вывода — `OUTPUT_DIR`, `output/task1/import-reports` и `output/task1/debug`.

До удаления печатается отчёт с фактическим масштабом: имя базы без учётных данных, количество записей по таблицам и число элементов в каждом каталоге. Для подтверждения нужно ввести слово `reset` — одной буквы для необратимой операции мало. Без терминала команда откажется работать, если не передан `--yes`.

Порядок намеренный: сначала коммит в БД, потом файлы. Команда идемпотентна, поэтому сбой на файлах чинится повторным запуском.

## Dry-run

Рекомендуемый безопасный запуск:

```bash
make task-1 dry-run
```

Для второй задачи — `make pprof-1 dry-run`. Он идёт в схему `pprof_1` базы на `localhost:5433`, поэтому её нужно там создать заранее так же, как в основной базе.

Точный эквивалент без Makefile:

```bash
docker compose up -d --wait postgres-dry-run
go test ./...
go vet ./...
APP_ENV=test DRY_RUN_DATABASE_URL='postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable' go run ./cmd/seo-pipeline task-1 run --dry-run
```

Dry-run делает две вещи.

**Показывает разрешённую маршрутизацию** — какой режим будет выбран, доступен ли каждый провайдер и почему, какой провайдер и какой таймаут у каждой стадии. Схему выбирает тот же резолвер, что и боевой прогон, поэтому отчёт отвечает не на вопрос «как настроено», а на вопрос «как пойдёт». Доступность определяется по локальным признакам — переменным окружения и маркерам выключения; ни сетевых запросов, ни запуска браузера здесь нет.

```
Режим: gemini — Gemini доступен

Провайдеры:
  deepseek_web   доступен     профиль data/browser/deepseek готов
  gemini         доступен     GEMINI_API_KEY задан, модель gemini-2.5-flash

Стадии:
  structure  deepseek_web / deepseek-web        2m0s
  article    gemini / gemini-2.5-flash          3m0s
  info       deepseek_web / deepseek-web        3m0s
  review     gemini / gemini-2.5-flash          2m0s
  fix        gemini / gemini-2.5-flash          5m0s  (продолжает чат стадии review)
  html       deepseek_web / deepseek-web        3m0s
```

**Прогоняет эту же схему целиком** на детерминированных локальных ответах: импорт, все шесть стадий, сборка `result.md` и проверка, что каждый артефакт на месте.

Разрешён только при `APP_ENV=local` или `APP_ENV=test` и только для БД, имя которой содержит `test`, `dry_run` или `dry-run`. Не вызывает Gemini, Keys.so, Arsenkin или Playwright. Перед запуском очищаются отдельная dry-run БД и `<OUTPUT_DIR>/dry-run`, поэтому данные повторных запусков не смешиваются.

## Статусы и этапы

Статусы `articles`:

- `pending` — импортирована и ещё не обрабатывалась;
- `processing` — зарезервирована или проходит pipeline;
- `completed` — `result.md` успешно опубликован;
- `failed` — текущая операция завершилась ошибкой.

Разрешённые значения `current_step`:

- `arsenkin_collection`;
- `structure_generation`;
- `article_generation`;
- `metadata_generation`;
- `article_review`;
- `html_generation`;
- `final_file_assembly`.

Оба списка закреплены `CHECK`-ограничениями в `migrations/000001_init_schema.up.sql`. Новый этап требует новой миграции и правки `expectedSchema` в `internal/pipeline/repository/schema.go`.

При `completed` этап равен `NULL` — это тоже ограничение уровня БД. При ошибке сохраняются текущий этап и `error_message`.

## Логи

Формат консоли выбирается переменной `LOG_FORMAT`:

| Значение | Что делает |
|---|---|
| `auto` (по умолчанию) | В терминале — человекочитаемый `pretty`, при перенаправлении в файл или пайп — `text`. Поэтому `make task-1 run \| grep` и запуск из CI работают как раньше. |
| `pretty` | Человекочитаемый формат всегда, даже в пайпе. |
| `text` | Прежний `key=value` даже в терминале. Ставьте, когда вывод разбирают инструментами. |
| `json` | Structured JSON для отправки в Loki, ELK и подобное. |

`pretty` отличается от `text` только подачей, набор событий тот же:

```
task_1 · import

11:58:45  ✓  [37] новая статья импортирована · Как стать логопедом: обуче…
11:58:45  ✓  импорт статей завершён
             imported_count=2  viewed_count=2  limit_reached=true
             report_path=output/task1/import…5845.579268000Z.json

готово · 58 мс
```

Что он делает с записью: `task` и `operation` уходят в шапку вместо повтора в каждой строке, `level` становится значком (`✓` `!` `✗` `·`), дата исчезает и остаётся время, `external_id` выносится в начало строки как `[37]`, длительности переводятся в человеческий вид (`25,4 с` вместо `duration_ms=25400`). Строки обрезаются по ширине окна из `COLUMNS`, а длинные пути усекаются по середине, чтобы имя файла осталось видно.

Две вещи не обрезаются никогда: текст ошибки и строки уровня `warn` и `error` — усечённая диагностика уводит в неверную сторону. Цвет выключается переменной `NO_COLOR`.

**Логи статей в `<external_id>-<slug>/logs/<операция>.log` формат консоли не затрагивает.** Они всегда пишутся в `text` или `json` со всеми полями, включая `article_id` и полные пути: их читают инструментами, и обрезанное значение там недопустимо.

## Артефакты и диагностика

Для статьи `external_id=37` и `image_slug=primer`:

```text
tasks/task_1/output/37-primer/
  prepare/
    input.json              исходные данные статьи
    keysso.json             запросы и их источник: manual, keysso или модель
    arsenkin.json           частотности Wordstat, LSI, структура конкурентов
    prepare-report.json     результат каждой проверки prepare
  logs/
    prepare.log             лог операции; по файлу на операцию
  prompts/
    structure_prompt.txt
    article_prompt.txt
    article_review_prompt.txt
    fix_article_prompt.txt
    article_html_prompt.txt
  generated/
    structure.txt
    article.txt
    review.txt
    fixed_article.txt
    generation_context.json
  article.html
  result.md
  DEMO/
    result.md
    article_prompt.txt
    fix_links_html_prompt.txt
    prompts/
      structure_prompt.txt
      article_info_prompt.txt
    generated/
      structure.txt
      article.txt
      article_info.txt
    prepare/                копия диагностики prepare
```

`prepare-report.json` пишется и при успехе, и при отказе: отказ — как раз тот случай, когда отчёт нужен.

Файлы сначала полностью записываются во временные файлы в целевом каталоге, затем публикуются атомарным rename вместе с сохранением состояния stage. При ошибке сохраняется предыдущая опубликованная версия. Пути в PostgreSQL относительны к `OUTPUT_DIR`.

## История ошибок обработки

Ошибки Prepare и генерационных этапов сохраняются в PostgreSQL: последняя ошибка остаётся в `articles.error_message`, а неизменяемая история — в `article_errors`. Успешный повторный запуск очищает актуальную ошибку по существующим правилам, но историю не удаляет.

```bash
make task-1 errors
make task-1 errors 124
```

Обе команды показывают **текущую** сохранённую ошибку, а не историю: первая — по всем статьям, у которых она есть, вторая — по одной. Если у статьи текущей ошибки нет, команда так и сообщает. Полная история читается из `article_errors` запросами ниже.

```sql
SELECT
    ae.created_at,
    ae.external_id,
    a.title,
    ae.step,
    ae.operation,
    ae.retryable,
    ae.error_message
FROM article_errors ae
JOIN articles a ON a.id = ae.article_id
ORDER BY ae.created_at DESC;
```

```sql
SELECT
    created_at,
    step,
    operation,
    retryable,
    error_message
FROM article_errors
WHERE external_id = '124'
ORDER BY created_at DESC;
```

Ошибки строк Excel по-прежнему находятся отдельно в `output/task1/import-reports/latest.json` и timestamped-отчётах; в `article_errors` они не переносятся.

## PostgreSQL и миграции

Схема состоит из таблиц `articles`, `article_inputs`, `article_research`, `article_metadata`, `article_outputs` и `article_errors`. Приложение проверяет ожидаемую схему перед выполнением команд, но не запускает миграции основной БД автоматически.

Схема описана baseline-миграцией `000001_init_schema` и последующими. Сейчас их две: `000002_add_google_doc_url` добавляет `article_outputs.google_doc_url` для ссылки на документ с промптом.

Существующий volume автоматически не мигрируется, поэтому новую миграцию нужно применить к работающей базе руками:

```bash
docker exec -i seo-postgres psql -U seo -d seo < migrations/000002_add_google_doc_url.up.sql
```

Свежий клон получает обе миграции сам при первой инициализации volume.

Прежняя цепочка `000001_create_articles` … `000008_add_article_errors` свёрнута в baseline. Цепочка `000001_create_articles` … `000008_add_article_errors` свёрнута в неё; промежуточных состояний схемы больше не существует, обновление старого volume не поддерживается — только пересоздание.

Чистая база с нуля:

```bash
make docker-down                 # или: docker compose down -v
docker compose down -v           # удалить volumes, иначе миграции не применятся повторно
make docker-up
go run ./cmd/seo-pipeline task-1 import
```

Если нужно очистить данные, не трогая контейнеры и схему, используйте `make <task> reset`, а для одной статьи — `make <task> clear <external_id>`. Обе команды работают только в схеме своей задачи и данные другой не трогают.

### Схема на задачу

Изоляция задач в PostgreSQL держится на `search_path`: `task_1` работает в `public`, `pprof_1` — в схеме `pprof_1`. Профиль задачи дописывает `search_path` в `DATABASE_URL`, поэтому отдельный DSN не нужен, а `public` возвращается нетронутым — подключение `task_1` не изменилось. Имя схемы проверяется до подстановки в строку подключения и берётся только из профиля.

Таблицы, миграции и `expectedSchema` у задач одни и те же — различается только схема, в которой они лежат. Поэтому новая задача не требует ни миграции, ни правки кода схемы: достаточно создать схему и применить в неё существующие `migrations/*.up.sql` (см. «Первый локальный запуск»).

## Текущие ограничения

- `pprof_1` работает только через DeepSeek Web: Gemini и другие провайдеры ему не настроены.
- В `pprof_1` `article`, `info`, `review` и `fix` — четыре имени одного действия: чат 2 неделим и прогоняется целиком, поэтому отдельно переделать только ревью нельзя.
- `demo-generate` в `pprof_1` не поддерживается.
- Слотов артефактов пять, а текстов у `pprof_1` шесть: `review.txt` занят статьёй после SEO-редактуры, а не списком замечаний. Схема БД ради этого не менялась.
- Схема PostgreSQL новой задачи создаётся руками, как и миграции.
- Импорт возобновляемый: существующие ID пропускаются без обновления, а необязательный лимит считается только по новым валидным статьям.
- `article` и `info` являются двумя именами одной объединённой операции.
- Отдельной CLI-команды только для `structure` нет.
- Автоматического retry для `failed` нет; повторный запуск выполняется явно через `retry` или `regenerate`.
- Повторный генерационный запуск снова обращается к LLM и тратит квоту.
- Общей транзакции между всей файловой системой и всем pipeline нет; согласованность обеспечивается отдельно для каждого сохраняемого stage.
- Валидатор существует как внутренний пакет без отдельной CLI-команды.
- В форму Wordstat уходит не более 49 запросов: лишние отбрасываются.

## Документация проекта

- `docs/CONTEXT.md` — единый язык: термины предметной области, используемые в коде и тестах.
- `docs/adr/` — архитектурные решения с обоснованием.
- `docs/AUDIT-2026-08.md` — отслеживаемый бэклог находок аудита.
- `docs/WORKING-WITH-CLAUDE.md` — скиллы, правила и хуки Claude Code в этом репозитории.
- `CLAUDE.md` — инварианты, ловушки и конвенции, которые нельзя ломать без обсуждения.

## Проверка проекта без Makefile

```bash
go fmt ./...
go vet ./...
golangci-lint run ./...
go test ./...
go test -race ./...
go build -o bin/seo-pipeline ./cmd/seo-pipeline
```
