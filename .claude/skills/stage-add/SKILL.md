---
name: stage-add
description: Добавить новую стадию в пайплайн генерации — провести правку через все согласованные места, ничего не пропустив.
disable-model-invocation: true
argument-hint: "[имя стадии и что она делает]"
---

# Добавление стадии пайплайна

Стадия сегодня добавляется правкой **двенадцати** мест. Пропуск любого даёт отказ на старте
(`ValidateSchema`), либо стадию, которую нельзя вызвать, либо статью, застрявшую на этапе.

Это ровно то нарушение OCP, о котором говорит [ADR-0006](../../../docs/adr/0006-architecture-principles.md):
добавление стадии требует трогать работающий код в десятке файлов. Скилл — обходной путь
до рефакторинга H1/H3, а не оправдание текущего дизайна.

## Прежде чем начать

Перед первой правкой пройти [grilling](../grilling/SKILL.md) — стадия меняет схему БД
и внешнее поведение, порог допроса пройден заведомо. Ответы, которые нужны до начала:

- Стадия добавляет **новый этап** (`current_step`) или встраивается в существующий?
- Какой этап идёт до неё и какой после? Чей `current_step` она снимает и чей ставит?
- Какие артефакты она пишет и **в какую колонку** сохраняется путь? См. ловушку ниже.
- Что делать при отказе — падать или пропускать статью?

## Ловушка: колонки путей названы не тем, что хранят

Проверено 2026-08-05. Фактическое соответствие в `article_outputs`:

| Колонка | Что в ней лежит |
|---|---|
| `structure_path` | `generated/structure.txt` |
| `article_path` | `generated/article.txt` |
| `review_path` | `generated/review.txt` |
| `fixed_article_path` | `generated/fixed_article.txt` |
| `html_path` | `article.html` |

Путь `result.md` **нигде не сохраняется** — `CompleteGeneration` ставит только
`status='completed'`. Восстанавливается из `external_id` + `slug`.

Важно: `GetPendingForOperation` использует NULL-ность этих колонок как **маркер прогресса**:

```
review  ждёт → review_path        IS NULL
fix     ждёт → review_path        IS NOT NULL AND fixed_article_path IS NULL
html    ждёт → fixed_article_path IS NOT NULL AND html_path          IS NULL
```

Значит колонка — не просто хранилище пути, а часть машины состояний. Занять чужую нельзя,
переиспользовать «свободную на вид» — тоже. Новой стадии нужна **своя новая колонка**,
и её добавление тянет за собой миграцию и `expectedSchema`.

## Двенадцать точек правки

Идти по порядку — каждая следующая опирается на предыдущую.

### 1. Промпт

`tasks/task_1/prompts/<stage>.txt`. Шаблон `text/template`, плейсхолдеры — как в соседних
файлах. Разбор существующих — [prompt-tune](../prompt-tune/SKILL.md).

### 2. Конфигурация стадии

`config/config.yaml` → `llm.stages.<stage>`: `targets` (обычно `*default_targets`),
`prompt`, `temperature`, `max_tokens`, `timeout`.

Единственный источник истины для маршрутизации — `targets`. Легаси-поля `provider`/`model`
на уровне стадии не заполнять ([ADR-0005](../../../docs/adr/0005-target-fallback.md), H5).

### 3. Список обязательных стадий

`internal/config/llm.go` → `requiredLLMStages`. Без этого стадия не валидируется и молча
отсутствует в рантайме.

### 4. Миграция: новое значение `current_step`

Значения `current_step` ограничены `CHECK`-констрейнтом. Эталонный образец —
`articles_current_step_check` в `migrations/000001_init_schema.up.sql`: констрейнт
**дропается и пересоздаётся** новой миграцией целиком
целиком со всем списком.

```sql
ALTER TABLE articles DROP CONSTRAINT articles_current_step_check;
ALTER TABLE articles ADD CONSTRAINT articles_current_step_check
    CHECK (current_step IS NULL OR current_step IN ( ...весь список плюс новое... ));
```

Парный `.down.sql` обязателен. Если стадии нужна своя колонка пути (см. ловушку) —
`ALTER TABLE article_outputs ADD COLUMN <stage>_path TEXT` в этой же миграции.

**Миграции применяются только при первой инициализации volume.** Чтобы проверить — `make
docker-down`, удалить volume, `make docker-up`.

### 5. `expectedSchema`

`internal/tasks/task1/repository/schema.go`. Добавлена колонка — добавить строку
`{"article_outputs", "<stage>_path", "text", true}`. Пропуск = приложение не стартует
целиком ([ADR-0003](../../../docs/adr/0003-validate-schema.md)).

### 6. Метод сохранения в репозитории

`internal/tasks/task1/repository/article_repository.go`. Скопировать форму `SaveReviewPath`:
транзакция → `UPDATE article_outputs SET <stage>_path` → `UPDATE articles SET current_step` →
проверка `RowsAffected() != 1` → `Commit`.

Семь таких методов уже почти идентичны, `dupl` их видит (H3, кластер 1). Восьмой копировать
не надо — выделить общий `saveStage`. Если решено копировать, сказать об этом явно в отчёте.

### 7. Ветки выбора статей

Там же, три `switch`, все обязательны:

- `GetPendingForOperation` (~строка 643) — предикат «какие статьи ждут эту операцию»,
  через NULL-ность колонок;
- `BeginGenerationStage` (~строка 320) — сопоставление имени стадии и `current_step`;
- `classifyErrorOperation` (~строка 1248) — сопоставление `current_step` и имени операции
  для `article_errors`.

### 8. Пути артефактов

`internal/tasks/task1/output/writer.go` → `articlePaths()`: поля `<Stage>PromptPath`
(в `prompts/`) и `<Stage>Path` (в `generated/`) в структуре `ArticlePaths`.

### 9. Метод `Stage*` во writer

Там же. Форма — как `StageReview`. Шесть таких методов уже дублируются (H3, кластер 2).

Публиковать файл только через `Commit(persist, ...)`, не отдельно
([ADR-0001](../../../docs/adr/0001-staged-commit.md)).

### 10. Пайплайн

`internal/tasks/task1/generation/pipeline.go`:

- метод в интерфейсе `PipelineRepository` (сейчас 13 методов — М7, узкий интерфейс лучше);
- метод в интерфейсе `PipelineWriter`;
- приватный `run<Stage>` по образцу `runReview`/`runFix`/`runHTML` — они идентичны
  на ~33 строки (H3, кластер 5);
- публичный `Run<Stage>ByExternalID`;
- вызов в `Pipeline.Run` в нужном месте цепочки.

### 11. Точка входа CLI

`cmd/seo-pipeline/main.go`:

- строка `available` в `parseTaskCommand` (~408) — список операций в тексте ошибки;
- `switch task` в `parseTaskCommand` (~413) — разбор аргументов;
- `case` в списке команд, требующих LLM (~141);
- ветка маршрутизации batch / одиночной статьи (~240) — шесть таких веток дословно
  одинаковы (H3, кластер 4).

`Makefile`: `TASK_OPERATIONS` и `OPTIONAL_ARGUMENT_OPERATIONS` (строки 17-18) плюс строка
в `help`. Операция, отсутствующая в `TASK_OPERATIONS`, отсекается ещё до запуска бинарника.

### 12. Документация и тесты

- [docs/CONTEXT.md](../../../docs/CONTEXT.md) — таблица стадий и, если добавился этап,
  список этапов;
- тест стадии в `pipeline_test.go` (фейк обязан реализовать весь интерфейс — отсюда его размер);
- тест метода репозитория (выполнится только с `TEST_DATABASE_URL`).

## Проверка

По порядку, каждый шаг обязателен:

1. `go build ./...` — компилируется.
2. Пересоздать volume и применить миграцию, иначе `ValidateSchema` не проверится.
3. `make task-1 dry-run` — сквозной прогон. **Сейчас dry-run сломан** (C1/C2); если он
   всё ещё падает на подмене модели — это известная проблема, а не ваша регрессия,
   и сказать об этом надо прямо.
4. [/ship](../ship/SKILL.md) — полный гейт.

## Завершение

Отчёт: какие из двенадцати точек затронуты, какие сознательно пропущены и почему,
какая колонка выбрана под путь и почему именно она, и прошла ли проверка dry-run.

Если по ходу выяснилось, что стадия вписывается без правки половины списка — сказать это:
значит граница в этом месте лучше, чем считалось, и заметку стоит занести
в [бэклог](../../../docs/AUDIT-2026-08.md).
