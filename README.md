# SEO Pipeline

Локальный CLI-сервис на Go для импорта SEO-задач из Excel, сбора данных
конкурентов в Keys.so и Arsenkin, сохранения результатов в PostgreSQL и
сборки промпта и локального сохранения результата генератора статьи.

## Что уже реализовано

- импорт Excel в `articles` и `article_inputs`;
- идемпотентное обновление статьи по внешнему Excel ID;
- сбор запросов конкурента и удаление неявных дублей в Keys.so;
- получение частотностей Wordstat, LSI и структур конкурентов в Arsenkin;
- транзакционное сохранение research-данных по `article_id`;
- внешние prompt-шаблоны для всех пяти LLM-этапов;
- provider-neutral интерфейс `llm.Client`, общий router и единый
  OpenAI-compatible HTTP-клиент для OpenRouter и Groq;
- Gemini через официальный Go SDK;
- маршрутизация `structure → article → review → fix → html` через YAML;
- отдельный перезапуск `review`, `fix` или `html` по `external_id` без повторения
  уже завершённых LLM-этапов;
- общий timeout на stage, включающий все retry и backoff;
- классификация rate limit, исчерпанной квоты, кредитов и ошибок авторизации;
- безопасная запись prompts и ответов в `output/<excel-id>-<slug>/`;
- проверка схемы PostgreSQL до запуска внешних интеграций;
- текстовое и JSON-логирование без вывода секретов;
- очистка данных со сбросом PostgreSQL sequence.

Тесты используют fake-клиенты и `httptest.Server`; реальные LLM-запросы из тестов
не выполняются.

## Архитектура

```text
Excel (.xlsx)
    │
    ▼
importer ──► repository ──► PostgreSQL
                                  │
                                  ▼
                              articles
                                  │ article_id
                                  ▼
Keys.so ──► cleaned_keywords ──► article_research
                                  │ article_id
                                  ▼
Arsenkin ──► Wordstat + LSI + structures
                                  │
                                  ▼
                              LLM router
                                  │
                                  ▼
                    structure → article → review
                                  │
                                  ▼
                             fix → html
                                  │
                                  ▼
                    output/<external-id>-<slug>/
```

Все данные одной статьи связываются одним `articles.id`. Дочерние таблицы имеют
отношение 1:1 с `articles`, а `article_id` одновременно является PK и FK с
`ON DELETE CASCADE`.

### Структура кода

```text
cmd/seo-pipeline/          CLI: import, prepare, generate, review, fix, html
internal/article/         доменные Go-модели
internal/config/          загрузка .env и валидация конфига
internal/importer/        чтение и валидация Excel
internal/generation/      генерационные этапы и Gemini adapter
internal/llm/             общий LLM Client, routing и retry
internal/integrations/    Playwright-интеграции Keys.so и Arsenkin
internal/output/          безопасная запись файлов статьи
prompts/                  внешние шаблоны всех LLM-этапов
internal/repository/      SQL-запросы и проверка схемы
internal/storage/         подключение к PostgreSQL
migrations/               up/down SQL-миграции
data/                     локальные browser profiles (не в Git)
input/                    входные Excel-файлы (не в Git)
output/                   будущие выходные файлы (не в Git)
```

## Требования

- Go 1.25 или новее;
- Docker Desktop с Docker Compose;
- Chromium для Playwright;
- активные аккаунты Keys.so и Arsenkin;
- Excel-файл с исходными статьями.

## Первая установка

1. Скачать Go-зависимости:

   ```bash
   go mod download
   ```

2. Установить Chromium для версии Playwright из `go.mod`:

   ```bash
   go run github.com/mxschmitt/playwright-go/cmd/playwright@v0.6100.0 install chromium
   ```

3. Запустить PostgreSQL:

   ```bash
   docker compose up -d postgres
   docker compose ps
   ```

4. Применить все up-миграции по порядку:

   ```bash
   docker compose exec -T postgres psql -U seo -d seo < migrations/000001_create_articles.up.sql
   docker compose exec -T postgres psql -U seo -d seo < migrations/000002_add_articles_external_id.up.sql
   docker compose exec -T postgres psql -U seo -d seo < migrations/000003_add_wordstat_keywords.up.sql
   docker compose exec -T postgres psql -U seo -d seo < migrations/000004_add_article_research_updated_at.up.sql
   docker compose exec -T postgres psql -U seo -d seo < migrations/000005_add_article_review_stage.up.sql
   ```

5. Создать `.env` вне репозитория, например на уровень выше. Основу можно
   взять из `.env.example`, не копируя секреты в Git.

6. Положить Excel-файл в `input/task_1/input.xlsx` или указать другой путь в
   `INPUT_FILE_PATH`.

## Конфигурация

Пример файла:

```dotenv
DATABASE_URL=postgres://seo:seo@localhost:5432/seo?sslmode=disable
INPUT_FILE_PATH=input/task_1/input.xlsx
OUTPUT_DIR=output

KEYS_SO_EMAIL=
KEYS_SO_PASSWORD=

ARSENKIN_EMAIL=
ARSENKIN_PASSWORD=
ARSENKIN_HEADLESS=true

GEMINI_API_KEY=
GEMINI_MODEL=gemini-2.5-flash

LOG_LEVEL=info
LOG_FORMAT=text
```

### Как выбирается `.env`

1. Если задан `ENV_FILE`, используется указанный путь.
2. Иначе, для обратной совместимости, проверяется `SEO_PIPELINE_ENV`.
3. Если обе переменные пусты, проект определяется по `go.mod`, а `.env` ищется
   рядом с каталогом проекта.

Относительный `ENV_FILE` вычисляется от текущего рабочего каталога. Используется
`godotenv.Load`: значения, уже заданные в системном окружении, имеют приоритет и не
перезаписываются файлом.

Пример запуска задачи из текущего расположения проекта:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 generate 37
```

### Переменные окружения

| Переменная | Команда | Обязательна | По умолчанию | Назначение |
|---|---|---:|---|---|
| `ENV_FILE` | все | нет | автопоиск | путь к `.env`; задаётся до запуска CLI |
| `DATABASE_URL` | все | да | — | PostgreSQL connection string |
| `INPUT_FILE_PATH` | `task_1 import` | нет | `input/task_1/input.xlsx` | путь к Excel |
| `OUTPUT_DIR` | `prepare`, `generate`, `review`, `fix`, `html` | нет | `output` | корень локальных результатов |
| `KEYS_SO_EMAIL` | `task_1 prepare` | да | — | login Keys.so |
| `KEYS_SO_PASSWORD` | `task_1 prepare` | да | — | пароль Keys.so |
| `ARSENKIN_EMAIL` | `task_1 prepare` | да | — | login Arsenkin |
| `ARSENKIN_PASSWORD` | `task_1 prepare` | да | — | пароль Arsenkin |
| `ARSENKIN_HEADLESS` | `task_1 prepare` | нет | `true` | запуск Chromium без UI |
| `GEMINI_API_KEY` | LLM-команды | да | — | ключ Gemini API; не логируется |
| `GEMINI_MODEL` | LLM-команды | да | — | модель Gemini для всех LLM-этапов |
| `LOG_LEVEL` | все | нет | `info` | `debug`, `info`, `warn` или `error` |
| `LOG_FORMAT` | все | нет | `text` | `text` или `json` |

Значения email и паролей не выводятся в логи. `.env`, browser profiles, входные Excel
и результаты игнорируются Git.

### Маршрутизация LLM

Провайдеры и настройки этапов находятся в `config/config.yaml`. YAML содержит
только имена переменных окружения, но не API-ключи.

| Этап | Провайдер | Модель |
|---|---|---|
| `structure` | Gemini | `GEMINI_MODEL` |
| `article` | Gemini | `GEMINI_MODEL` |
| `info` | Gemini | `GEMINI_MODEL` |
| `review` | Gemini | `GEMINI_MODEL` |
| `fix` | Gemini | `GEMINI_MODEL` |
| `html` | Gemini | `GEMINI_MODEL` |

Все этапы подключены через router и используют один экземпляр Gemini-клиента.
Фактическая модель берётся из `GEMINI_MODEL`; temperature, max tokens, timeout и
путь к prompt задаются в `config/config.yaml`.

Внешние шаблоны:

```text
prompts/structure.txt
prompts/article.txt
internal/prompts/article_info.txt
prompts/review.txt
prompts/fix_article.txt
prompts/html.txt
```

Один timeout stage ограничивает все его HTTP-попытки и backoff. Router выполняет
до трёх попыток для временного 429 и кодов 500/502/503/504. Исчерпанная квота,
кредиты, HTTP 402, 401 и 403 не повторяются. Перед каждой попыткой логируется
оставшееся время `remaining_ms`. Для долгих запросов каждые 30 секунд выводится
heartbeat `LLM request still running` с elapsed и оставшимся временем. Стадия
`fix` имеет увеличенный общий timeout 5 минут; timeout остальных стадий указан в
том же YAML и не изменён.

## CLI-задачи

Все команды выполняются из корня репозитория.

### Быстрый запуск — можно копировать

В примерах `37` — это `articles.external_id`. Если `.env` лежит рядом с
репозиторием и находится автоматически, уберите `ENV_FILE=../.env`.

Полный цикл подготовки и генерации:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 import
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 prepare 37
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 generate 37
```

Запустить LLM-этапы отдельно, без автоматического запуска соседних стадий:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline article 37
ENV_FILE=../.env go run ./cmd/seo-pipeline info 37
ENV_FILE=../.env go run ./cmd/seo-pipeline review 37
ENV_FILE=../.env go run ./cmd/seo-pipeline fix 37
ENV_FILE=../.env go run ./cmd/seo-pipeline html 37
```

Те же команды без явного `ENV_FILE`:

```bash
go run ./cmd/seo-pipeline task_1 import
go run ./cmd/seo-pipeline task_1 prepare 37
go run ./cmd/seo-pipeline task_1 generate 37
go run ./cmd/seo-pipeline article 37
go run ./cmd/seo-pipeline info 37
go run ./cmd/seo-pipeline review 37
go run ./cmd/seo-pipeline fix 37
go run ./cmd/seo-pipeline html 37
```

### Импорт статей

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 import
```

Или с явным путём к Excel:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 import input/task_1/input.xlsx
```

Задача импортирует статьи из Excel в PostgreSQL и не запускает внешние сервисы
или Gemini. Если путь не передан, используется `INPUT_FILE_PATH`.

### Подготовка статьи

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 prepare 37
```

`37` — `articles.external_id` из Excel. Задача очищает прежние производные
результаты статьи, собирает данные через Keys.so и Арсенкин и сохраняет research
в PostgreSQL. Gemini и генерация статьи не запускаются.

Точный поток:

```text
статья по external_id → Keys.so → Arsenкин → сохранение research → завершение
```

### Генерация статьи

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 generate 37
```

Задача использует сохранённый research и выполняет полный LLM-поток. Keys.so,
Арсенкин и Playwright не запускаются.

Точный поток:

```text
research из PostgreSQL → structure → article → review → fix → html
→ проверка HTML Go-кодом → сохранение article.html → status=completed
```

Каждый этап stateless: история чатов между этапами не передаётся. Сохраняются
отрендеренные prompts, ответы, модель, `external_id` и относительные пути.
Контекст article записывается в `generated/generation_context.json`.

### Только article

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline article 37
```

Команда читает уже сохранённый `generated/structure.txt`, использует research из
PostgreSQL, вызывает только stage `article` и сохраняет `generated/article.txt`.
Structure, info, review, fix и HTML автоматически не запускаются.

### Только info

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline info 37
```

Команда читает `generated/structure.txt` и `generated/article.txt`, вызывает
только Gemini stage `info`, сохраняет фактически отправленный prompt в
`prompts/article_info_prompt.txt`, а название, три метки, TL;DR и FAQ — в
`generated/article_info.txt`. Тот же результат сохраняется в существующем поле
`article_metadata.metadata_text`; `current_step` обновляется, а
`error_message` очищается.

### Только review

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline review 37
```

Команда читает уже сохранённый `generated/article.txt`, вызывает только stage
`review`, сохраняет `generated/review.txt` и завершает работу. `structure`,
`article` и `fix` не запускаются.

### Только fix

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline fix 37
```

Команда читает `generated/article.txt` и `generated/review.txt`, вызывает только
stage `fix`, сохраняет `generated/fixed_article.txt` и записывает его путь в
`article_outputs.final_path`. HTML автоматически не запускается.

### Только HTML

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline html 37
```

Команда читает `generated/fixed_article.txt`, вызывает только stage `html`,
проверяет ответ Go-кодом и сохраняет `article.html`. Предыдущие LLM-этапы не
повторяются.

Если обязательного входного файла или сохранённого пути нет, команда завершится
до LLM-вызова с ошибкой, содержащей `article_id`, `external_id` и название
отсутствующего результата.

Каждая отдельная LLM-команда завершает работу после своего этапа:

| Команда | Обязательные входы | Выходы |
|---|---|---|
| `article` | research в PostgreSQL, `generated/structure.txt` | `prompts/article_prompt.txt`, `generated/article.txt`, `generated/generation_context.json` |
| `info` | `generated/structure.txt`, `generated/article.txt` | `prompts/article_info_prompt.txt`, `generated/article_info.txt` |
| `review` | `generated/article.txt` | `prompts/article_review_prompt.txt`, `generated/review.txt` |
| `fix` | `generated/article.txt`, `generated/review.txt` | `prompts/fix_article_prompt.txt`, `generated/fixed_article.txt` |
| `html` | `generated/fixed_article.txt` | `prompts/article_html_prompt.txt`, `article.html` |

Во время разработки writer переиспользует существующий каталог статьи и атомарно
перезаписывает только файлы текущих этапов. Повторные `generate`, `article`,
`info`, `review`, `fix` и `html` не падают из-за уже существующих файлов.
Структура каталогов и имена артефактов не меняются.

Перед началом `prepare` база удаляет прежние `article_research`,
`article_metadata` и `article_outputs` и сбрасывает состояние статьи. Каталог
`output/<external-id>-<slug>/` при этом не удаляется. Импортированная строка
задания и `article_inputs` остаются источником для нового запуска.

При ошибке она записывается в `articles.error_message`, а задача завершается с
ненулевым exit code.

Файлы результата:

```text
output/<external-id>-<slug>/
├── prompts/
│   ├── structure_prompt.txt
│   ├── article_prompt.txt
│   ├── article_info_prompt.txt
│   ├── article_review_prompt.txt
│   ├── fix_article_prompt.txt
│   └── article_html_prompt.txt
├── generated/
│   ├── structure.txt
│   ├── article.txt
│   ├── article_info.txt
│   ├── review.txt
│   ├── fixed_article.txt
│   └── generation_context.json
└── article.html
```

Проверить HTML:

```bash
test -s output/37-<slug>/article.html && grep -E '<(h[1-6]|p)[ >]' output/37-<slug>/article.html
```

Проверить состояние статьи в PostgreSQL:

```sql
SELECT external_id, status, current_step, error_message
FROM articles
WHERE external_id = '37';
```

После успешного HTML ожидаются `status = 'completed'`, `current_step IS NULL` и
пустой `error_message`. Во время выполнения этапы последовательно принимают
значения `article_generation`, `article_review`, `html_generation`.

| Задача | Что делает | Внешний браузер | LLM |
|---|---|---:|---:|
| `import` | Excel → PostgreSQL | нет | нет |
| `prepare` | сбор и подготовка research | да | нет |
| `generate` | полный поток structure → article → review → fix → html | нет | да |
| `review` | только review сохранённой статьи | нет | да |
| `fix` | только исправление по сохранённому review | нет | да |
| `html` | только HTML из исправленной статьи | нет | да |

## База данных

```text
articles
├── article_inputs
├── article_research
├── article_metadata
└── article_outputs
```

| Таблица | Назначение | Когда заполняется |
|---|---|---|
| `articles` | ID, Excel ID, title, status, current step, error, timestamps | все команды статьи |
| `article_inputs` | исходные поля Excel | `task_1 import` |
| `article_research` | Keys.so, Wordstat, LSI, структуры, timestamps | `task_1 prepare` |
| `article_metadata` | metadata-текст и время чтения | зарезервировано |
| `article_outputs` | пути к structure/article/review/fixed article/HTML | `generate`, `review`, `fix`, `html` |

`article_research` содержит:

- `cleaned_keywords JSONB` — очищенные Keys.so-запросы;
- `wordstat_keywords JSONB` — пары query/frequency;
- `lsi_words JSONB` — LSI-слова;
- `competitor_structure TEXT` — структуры страниц;
- `collected_at` — время сбора;
- `updated_at` — время последнего обновления research-строки.

При каждом запуске CLI проверяет:

- наличие всех пяти таблиц и ожидаемых колонок;
- SQL-типы и nullable-ограничения;
- отсутствие неожиданных колонок;
- FK к `articles` с `ON DELETE CASCADE`.

При расхождении CLI завершается до Keys.so/Arsenkin и перечисляет проблемы.

### Полезные SQL-команды

```bash
docker compose exec postgres psql -U seo -d seo
```

```sql
\dt
\d articles
\d article_research

SELECT id, external_id, title, status, current_step, error_message
FROM articles
ORDER BY id;

SELECT article_id,
       jsonb_array_length(cleaned_keywords) AS cleaned_count,
       jsonb_array_length(wordstat_keywords) AS wordstat_count,
       jsonb_array_length(lsi_words) AS lsi_count,
       collected_at,
       updated_at
FROM article_research
ORDER BY article_id;

\q
```

## Миграции

| Номер | Назначение |
|---|---|
| `000001` | пять основных таблиц, constraints и индексы |
| `000002` | `articles.external_id` и unique index |
| `000003` | `article_research.wordstat_keywords` |
| `000004` | `article_research.updated_at` для обновления существующих БД |
| `000005` | этап `article_review` в допустимых значениях `current_step` |

Приложение проверяет схему, но не применяет миграции автоматически. Их нужно
запускать по возрастанию номера. Down-миграции могут удалять данные; перед откатом
нужна резервная копия.

## Browser profiles и авторизация

- Keys.so: `data/keysso-browser-profile/`;
- Arsenkin: `data/arsenkin-browser-profile/`;
- каждая интеграция имеет свой persistent Chromium profile;
- сессия повторно используется между запусками;
- при завершившейся сессии CLI выполняет вход из `.env`;
- `storageState` не используется;
- один profile нельзя одновременно использовать в двух процессах;
- Arsenkin дополнительно защищает profile lock-файлом;
- browser profiles содержат session data, поэтому их нельзя коммитить или
  передавать другим людям.

Для наблюдения за работой Arsenkin можно временно указать `ARSENKIN_HEADLESS=false` в
системном окружении или `.env`.

## Логирование

Примеры:

```bash
LOG_LEVEL=debug LOG_FORMAT=text ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 prepare 37
LOG_LEVEL=info LOG_FORMAT=json ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 generate 37
```

Логи LLM содержат `article_id`, `stage`, `provider`, `model`, номер попытки,
duration, usage и оставшееся время `remaining_ms`. Для ошибок дополнительно
выводятся `status_code`, `error_type`, безопасное короткое сообщение и
`retryable`.

Поддерживаемые типы LLM-ошибок: `rate_limit`, `quota_exhausted`,
`credits_exhausted`, `unauthorized`, `provider_error`. Prompt, полный ответ,
полное тело HTTP-ошибки, API-ключи и Authorization не логируются. Email и пароли
внешних интеграций также не логируются.

## Тесты и проверки

Все unit-тесты:

```bash
go test ./...
```

Repository-тесты с реальной PostgreSQL запускаются при наличии `TEST_DATABASE_URL`. Они
создают и удаляют изолированную тестовую схему:

```bash
TEST_DATABASE_URL='postgres://seo:seo@localhost:5432/seo?sslmode=disable' go test ./...
```

До коммита:

```bash
go fmt ./...
go test ./...
git diff --check
```

## Диагностика

### `.env` не найден

Запускать команду из корня проекта и явно указать файл:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline task_1 generate 37
```

### PostgreSQL недоступен

```bash
docker compose ps
docker compose logs postgres
```

Проверить `DATABASE_URL`, порт `5432` и статус health/container.

### Схема не согласована с кодом

Применить все недостающие up-миграции. Для ошибки
`article_research.updated_at does not exist` нужна `000004`:

```bash
docker compose exec -T postgres psql -U seo -d seo < migrations/000004_add_article_research_updated_at.up.sql
```

### Browser profile already used

Завершить другую задачу `prepare`, которая использует тот же profile. Не удалять
lock-файл или profile, пока процесс ещё работает.

### Повторный запуск после ошибки

Если полный `generate` остановился после уже успешного этапа, устранить причину и
запустить только нужный оставшийся этап:

```bash
# повторить article без structure
ENV_FILE=../.env go run ./cmd/seo-pipeline article 37

# повторить info без других LLM-этапов
ENV_FILE=../.env go run ./cmd/seo-pipeline info 37

# повторить review без structure/article
ENV_FILE=../.env go run ./cmd/seo-pipeline review 37

# повторить fix без structure/article/review
ENV_FILE=../.env go run ./cmd/seo-pipeline fix 37

# повторить html без предыдущих LLM-запросов
ENV_FILE=../.env go run ./cmd/seo-pipeline html 37
```

Для `article` должна существовать structure, для `info` — structure и article,
для `review` — article, для `fix` — article и review, для `html` — fixed article.

### LLM rate limit, квота или кредиты

- обычный временный HTTP 429 повторяется в пределах общего timeout stage;
- `insufficient_quota` и явное исчерпание бесплатной квоты не повторяются;
- HTTP 402 и исчерпанные кредиты не повторяются;
- 401/403 не повторяются;
- 500/502/503/504 повторяются по текущей retry-политике.

Остаток бесплатных токенов заранее не проверяется: причина определяется по
фактическому ответу API.

## Текущие ограничения

- импорт ограничен двумя статьями;
- каждая задача обрабатывает одну статью;
- текущую программную валидацию статьи нужно дополнительно проверить и уточнить
  перед использованием как окончательный редакционный контроль;
- селекторы внешних сайтов могут потребовать адаптации после изменения UI Keys.so или
  Arsenkin.

## Будущие задачи

- Перевести LLM-клиенты на streaming, чтобы видеть фактическое поступление частей
  ответа и при необходимости сохранять частичный результат. До реализации
  streaming heartbeat означает только то, что HTTP-запрос ещё выполняется, а не
  то, что модель уже генерирует текст.
