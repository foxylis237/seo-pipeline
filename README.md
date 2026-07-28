# SEO Pipeline

Локальный CLI-сервис на Go для импорта SEO-задач из Excel, сбора данных
конкурентов в Keys.so и Arsenkin, сохранения результатов в PostgreSQL и
сборки промпта для генерации статьи.

## Что уже реализовано

- импорт Excel в `articles` и `article_inputs`;
- идемпотентное обновление статьи по внешнему Excel ID;
- сбор запросов конкурента и удаление неявных дублей в Keys.so;
- получение частотностей Wordstat, LSI и структур конкурентов в Arsenkin;
- транзакционное сохранение research-данных по `article_id`;
- сборка отдельного промпта для каждой статьи;
- проверка схемы PostgreSQL до запуска внешних интеграций;
- текстовое и JSON-логирование без вывода секретов;
- очистка данных со сбросом PostgreSQL sequence.

Этапы генерации текста, metadata, HTML и итоговых файлов пока не
реализованы. Таблицы и этапы для них зарезервированы в схеме.

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
                         article prompt (stdout)
```

Все данные одной статьи связываются одним `articles.id`. Дочерние таблицы имеют
отношение 1:1 с `articles`, а `article_id` одновременно является PK и FK с
`ON DELETE CASCADE`.

### Структура кода

```text
cmd/seo-pipeline/          CLI: import, run, reset
internal/article/         доменные Go-модели
internal/config/          загрузка .env и валидация конфига
internal/importer/        чтение и валидация Excel
internal/integrations/    Playwright-интеграции Keys.so и Arsenkin
internal/prompts/         встроенные текстовые шаблоны
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
   ```

5. Создать `.env` вне репозитория, например на уровень выше. Основу можно
   взять из `.env.example`, не копируя секреты в Git.

6. Положить Excel-файл в `input/input.xlsx` или указать другой путь в
   `INPUT_FILE_PATH`.

## Конфигурация

Пример файла:

```dotenv
DATABASE_URL=postgres://seo:seo@localhost:5432/seo?sslmode=disable
INPUT_FILE_PATH=input/input.xlsx

KEYS_SO_EMAIL=
KEYS_SO_PASSWORD=

ARSENKIN_EMAIL=
ARSENKIN_PASSWORD=
ARSENKIN_HEADLESS=true

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

Для текущего расположения проекта:

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline run
```

### Переменные окружения

| Переменная | Команда | Обязательна | По умолчанию | Назначение |
|---|---|---:|---|---|
| `ENV_FILE` | все | нет | автопоиск | путь к `.env`; задаётся до запуска CLI |
| `DATABASE_URL` | все | да | — | PostgreSQL connection string |
| `INPUT_FILE_PATH` | `import` | нет | `input/input.xlsx` | путь к Excel |
| `KEYS_SO_EMAIL` | `run` | да | — | login Keys.so |
| `KEYS_SO_PASSWORD` | `run` | да | — | пароль Keys.so |
| `ARSENKIN_EMAIL` | `run` | да | — | login Arsenkin |
| `ARSENKIN_PASSWORD` | `run` | да | — | пароль Arsenkin |
| `ARSENKIN_HEADLESS` | `run` | нет | `true` | запуск Chromium без UI |
| `LOG_LEVEL` | все | нет | `info` | `debug`, `info`, `warn` или `error` |
| `LOG_FORMAT` | все | нет | `text` | `text` или `json` |

Значения email и паролей не выводятся в логи. `.env`, browser profiles, входные Excel
и результаты игнорируются Git.

## Команды

Все команды выполняются из корня репозитория.

### Импорт Excel

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline import
```

Импорт:

- ищет лист `Лист1`, а при его отсутствии берёт первый лист;
- требует колонки `id` и `article_name`;
- поддерживает необязательные `header`, `image_slug`, `meta_description`,
  `key_word`, `reference_url`, `category`, `authors`, `links`, `professions`;
- поддерживает старую опечатку `referense_url`;
- пропускает пустые строки;
- отклоняет пустые и повторяющиеся ID;
- в текущей MVP-версии импортирует не более двух непустых строк.

Повторный `import` с тем же Excel ID обновляет `articles` и `article_inputs`, а не
создаёт дубликат.

### Запуск pipeline

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline run
```

`run` читает статьи по `articles.id` и последовательно для каждой:

1. проверяет `reference_url`;
2. получает запросы конкурента в Keys.so;
3. удаляет неявные дубли и записывает `cleaned_keywords`;
4. получает частотности Wordstat в Arsenkin;
5. передаёт Top-50 запросов в Copywriters;
6. сохраняет `wordstat_keywords`, `lsi_words` и `competitor_structure`;
7. собирает промпт и выводит его в stdout.

При ошибке она записывается в `articles.error_message`, а команда завершается с
ненулевым exit code. Текущая реализация останавливает весь запуск на первой
ошибочной статье. Успешный повторный запуск перезаписывает research-данные той
же статьи и очищает её ошибку.

### Очистка базы

```bash
ENV_FILE=../.env go run ./cmd/seo-pipeline reset
```

Команда в одной транзакции очищает пять таблиц и сбрасывает sequence. Схема,
миграции и browser profiles не удаляются. Операция удаляет все данные статей.

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
| `articles` | ID, Excel ID, title, status, current step, error, timestamps | `import`, `run` |
| `article_inputs` | исходные поля Excel | `import` |
| `article_research` | Keys.so, Wordstat, LSI, структуры, timestamps | `run` |
| `article_metadata` | metadata-текст и время чтения | зарезервировано |
| `article_outputs` | пути к structure/article/metadata/HTML/final и word count | зарезервировано |

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
LOG_LEVEL=debug LOG_FORMAT=text ENV_FILE=../.env go run ./cmd/seo-pipeline run
LOG_LEVEL=info LOG_FORMAT=json ENV_FILE=../.env go run ./cmd/seo-pipeline run
```

Логи этапов содержат `article_id`, integration, stage, duration, current URL и счётчики
результатов. Email и пароли не логируются. Ошибка парсинга `.env` также не включает
строку файла, в которой может быть секрет.

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
ENV_FILE=../.env go run ./cmd/seo-pipeline run
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

Завершить другой процесс pipeline, который использует тот же profile. Не удалять
lock-файл или profile, пока процесс ещё работает.

### Повторный запуск после ошибки

Устранить причину и повторить `run`. Upsert не создаст вторую research-строку для
того же `article_id`.

## Текущие ограничения

- импорт ограничен двумя статьями;
- pipeline обрабатывает статьи последовательно;
- первая ошибка останавливает весь `run`;
- `articles.status` пока не ведёт полный lifecycle `processing/completed/failed`;
- `article_metadata` и `article_outputs` пока не заполняются;
- промпт выводится в stdout, но не сохраняется в файл;
- селекторы внешних сайтов могут потребовать адаптации после изменения UI Keys.so или
  Arsenkin.
