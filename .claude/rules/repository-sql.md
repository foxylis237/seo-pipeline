---
paths:
  - "**/repository/**"
  - "migrations/**"
  - "internal/tasks/task1/repository/**"
---

# Репозиторий и схема PostgreSQL

## Схема и миграции — единый источник истины

`repository.ValidateSchema` сверяет фактические колонки, типы, nullable и каскадные FK перед
каждой командой. Новая миграция **обязана** сопровождаться правкой `expectedSchema` в
`schema.go` — иначе приложение перестанет запускаться целиком.

Миграции парные: `NNNNNN_name.up.sql` и `.down.sql`. Применяются docker-entrypoint'ом только
при первой инициализации volume; отдельного runner'а нет. Уже существующий volume повторно
не мигрируется — при изменении схемы нужен `make docker-down` + удаление volume.

Мёртвые колонки, которые `ValidateSchema` всё равно требует: `article_metadata.reading_time_minutes`,
`article_outputs.word_count`, `article_inputs.seo_title/profession_name/image_name/image_url`.
Прежде чем добавлять новую колонку — проверить, не повторяется ли этот сценарий.

## Транзакции

Паттерн для каждого метода сохранения:

```go
tx, err := r.pool.Begin(ctx)
defer func() { _ = tx.Rollback(ctx) }()   // безопасно после Commit
// ... Exec
if result.RowsAffected() != 1 { return fmt.Errorf("...") }
return tx.Commit(ctx)
```

`RowsAffected() != 1` проверять всегда: без этого исчезновение строки проходит молча.

Семь методов `Save*` повторяют этот блок почти дословно (`dupl` подтверждает). При добавлении
восьмого — не копировать, а выделить общий `saveStage`.

## Конкурентность

`ClaimNextIncomplete` использует `FOR UPDATE SKIP LOCKED`, чтобы параллельные процессы не
получили одну статью. Не заменять на `SELECT ... LIMIT 1` и не выносить выборку из транзакции.

## Атомарность с файловой системой

Состояние в БД сохраняется как `persist`-колбэк внутри `articleoutput.Commit(persist, pending...)`,
а не отдельным вызовом после публикации файла. Иначе возможен `result.md` на диске при
`status='processing'` — сегодня так работает batch-ветка `make task-1 result`.

## Идентификаторы

`external_id` — строка из Excel (колонка `id`), `articles.id` — внутренний BIGSERIAL.
В CLI пользователь всегда указывает `external_id`. Не путать в запросах и сообщениях.

## Тесты

Требуют `TEST_DATABASE_URL`, иначе **молча пропускаются** — сейчас так и происходит со всеми 15.
Каждый тест создаёт свою схему `repository_test_<ns>` и удаляет её в `Cleanup`.
Запуск: `TEST_DATABASE_URL='postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable' go test ./internal/tasks/task1/repository/...`
