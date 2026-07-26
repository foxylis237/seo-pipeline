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

Импортировать статьи из Excel:

```bash
go run ./cmd/seo-pipeline import
```

Запустить пайплайн обработки:

```bash
go run ./cmd/seo-pipeline run
```

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
- 🚧 Получение следующей статьи из БД
- 🚧 Интеграция Keys.so
- ⏳ Остальные этапы пайплайна