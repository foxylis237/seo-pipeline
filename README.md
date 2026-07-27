# SEO Pipeline

Локальный CLI-сервис автоматизации SEO-пайплайна.

## Стек

- Go
- PostgreSQL
- Docker
- slog

---

## Структура

```
cmd/seo-pipeline/        // CLI
internal/
    article/             // модели
    config/              // загрузка .env
    importer/            // импорт Excel
    integrations/        // внешние сервисы
    repository/          // работа с БД
    storage/             // PostgreSQL
migrations/              // SQL-миграции
input/                   // входной Excel
output/                  // результаты
logs/
```

---

## Запуск

Запустить PostgreSQL:

```bash
docker compose up -d postgres
```

Применить миграции по порядку:

```bash
docker compose exec -T postgres psql -U seo -d seo < migrations/000001_create_articles.up.sql
docker compose exec -T postgres psql -U seo -d seo < migrations/000002_add_articles_external_id.up.sql
```

Импортировать статьи из Excel (по умолчанию `input/input.xlsx`):

```bash
export DATABASE_URL='postgres://seo:seo@localhost:5432/seo?sslmode=disable'
go run ./cmd/seo-pipeline import
```

Другой путь можно передать через `INPUT_FILE_PATH`.

Запустить пайплайн обработки:

```bash
export DATABASE_URL='postgres://seo:seo@localhost:5432/seo?sslmode=disable'
go run ./cmd/seo-pipeline run
```

`run` атомарно получает одну статью со статусом `pending`, переводит её в
`processing` и завершает работу. Интеграция Keys.so пока не запускается.

### Переменные окружения

Для `import`:

- `DATABASE_URL` — обязательно;
- `INPUT_FILE_PATH` — необязательно, по умолчанию `input/input.xlsx`.

Для текущей реализации `run`:

- `DATABASE_URL` — обязательно.

`KEYS_SO_EMAIL` и `KEYS_SO_PASSWORD` понадобятся только после подключения
и ручной проверки браузерного сценария Keys.so. Текущие селекторы являются
неподтверждёнными, поэтому интеграция не считается рабочей.

---

## Импорт

Источник:

```
input/input.xlsx
```

Импортирует статьи в PostgreSQL.

На текущем этапе читаются первые 2 строки (MVP).

---

## Таблицы

### articles

Основная информация о статье.

| Поле | Описание |
|------|----------|
| id | ID статьи |
| external_id | Уникальный ID строки исходного Excel |
| title | Название статьи |
| status | Статус обработки |
| current_step | Текущий этап пайплайна |
| error_message | Текст ошибки |
| created_at | Дата создания |
| updated_at | Дата обновления |

---

### article_inputs

Исходные данные из Excel.

| Поле | Описание |
|------|----------|
| article_id | ID статьи |
| category | Рубрика |
| header | H1 |
| image_slug | URL изображения |
| meta_description | Meta Description |
| key_word | Основной ключ |
| reference_url | URL конкурента |
| author | Автор |
| links | Внутренние ссылки |
| professions | Связанные профессии |

---

## Статусы

```
pending
processing
completed
failed
```

---

## Этапы пайплайна

```
arsenkin_collection
arsenkin_cleanup
structure_generation
article_generation
metadata_generation
reading_time_calculation
html_generation
final_file_assembly
```

---

## Текущее состояние MVP

- ✅ Импорт Excel
- ✅ PostgreSQL
- ✅ Логирование
- ✅ CLI (`import`, `run`)
- ✅ Атомарное получение следующей статьи из БД
- 🚧 Интеграция Keys.so
- ⏳ Остальные этапы пайплайна
