# Единый язык проекта

Термины из этого файла используются в именах типов, функций, файлов, тестов, сообщений об
ошибках и в разговоре. Если для одного понятия в коде встречается два имени — это дефект,
а не стилистика.

Единственная тактическая практика DDD, принятая в проекте. Агрегаты, bounded contexts и
domain events не вводим — см. [.claude/rules/architecture.md](../.claude/rules/architecture.md).

---

## Три значения слова «article» — главная ловушка

Слово `article` в проекте означает три разные вещи. Всегда уточняем, какую:

| Что имеется в виду | Как называть | Где живёт |
|---|---|---|
| Сущность-статья целиком | **статья**, `Article` | таблица `articles`, тип `article.Article` |
| Стадия LLM, генерирующая текст | **стадия `article`** | `config.yaml` → `stages.article` |
| Файл с результатом этой стадии | **артефакт `article.txt`** | `generated/article.txt` |

В коде: тип — `Article`, стадия — строковый литерал `"article"`, файл — `ArticlePath`.
В разговоре и комментариях — «статья», «стадия article», «артефакт article.txt».

---

## Статья

**Статья** (`article.Article`) — единица работы пайплайна. Импортируется из Excel, проходит
этапы, завершается файлом `result.md`.

**`external_id`** — строковый идентификатор из Excel (колонка `id`). Это то, что пользователь
указывает в CLI: `make task-1 generate 37`.

**`articles.id`** — внутренний `BIGSERIAL`. В CLI никогда не фигурирует.

Путать их нельзя: в сообщениях об ошибках и логах всегда явно `article_id` или `external_id`.

**`slug`** — URL-совместимое имя статьи. Вместе с `external_id` образует имя каталога
артефактов: `<external_id>-<slug>/`.

---

## Статус

**Статус** (`articles.status`) — состояние статьи в целом. Ровно четыре значения, ограничены
`CHECK`-констрейнтом:

| Значение | Смысл |
|---|---|
| `pending` | импортирована, работа не начиналась |
| `processing` | работа идёт или прервалась посередине |
| `completed` | `result.md` собран, `current_step` обязан быть `NULL` |
| `failed` | завершилась ошибкой |

Инвариант на уровне схемы: `status <> 'completed' OR current_step IS NULL`.

---

## Этап

**Этап** (`articles.current_step`) — где именно внутри пайплайна находится статья. Семь
значений, ограничены `CHECK`:

`arsenkin_collection` → `structure_generation` → `article_generation` →
`metadata_generation` → `article_review` → `html_generation` → `final_file_assembly`

Этап — это **состояние в БД**, точка возобновления после сбоя. Не путать со стадией.

---

## Стадия

**Стадия** (`stage`) — вызов LLM с собственным промптом и настройками. Ровно шесть,
список зафиксирован в `requiredLLMStages`:

| Стадия | Промпт | Что делает |
|---|---|---|
| `structure` | `structure.txt` | план статьи из research |
| `article` | `article.txt` | текст статьи по плану |
| `info` | `article_info.txt` | метаданные: TL;DR, FAQ, теги |
| `review` | `review.txt` | замечания к тексту |
| `fix` | `fix_article.txt` | текст с учётом замечаний |
| `html` | `html.txt` | HTML-разметка |

Стадия — это **единица конфигурации** (`config.yaml` → `stages.<name>`). Этап — состояние
в БД. Соответствие между ними не однозначное: стадии `article` и `info` вместе покрывают
этапы `article_generation` и `metadata_generation` и выполняются в одном чате.

---

## Операция

**Операция** — то, что пользователь пишет в CLI: `make task-1 <operation> [id]`.
Список — в `TASK_OPERATIONS` в Makefile.

Операция ≠ стадия ≠ этап. Операция `generate` запускает все шесть стадий; операция `fix` —
одну. Операции **`article` и `info` — два имени одной объединённой операции**.

Без `id` обрабатываются все подходящие статьи (batch-режим), с `id` — одна.

---

## Research

**Research** (`article_research`) — SEO-данные, собранные до генерации. Три составляющие:

- **`competitor_structure`** — структура статьи конкурента (Arsenkin Copywriters)
- **`wordstat_keywords`** — запросы с частотностью (`article.KeywordFrequency`), Arsenkin Wordstat
- **`lsi_words`** — семантически связанные слова (Arsenkin Copywriters)

Собирается операцией `prepare` из Keys.so и Arsenkin. Пишется одним атомарным upsert'ом
после того, как собрано всё — частичный research не сохраняется.

---

## Артефакт

**Артефакт** — файл на диске, произведённый пайплайном. Живёт под `OUTPUT_DIR`
(по умолчанию `tasks/task_1/output`), в каталоге `<external_id>-<slug>/`.

```
<external_id>-<slug>/
  prompts/            ← отправленные промпты (для разбора инцидентов)
    structure_prompt.txt, article_prompt.txt, article_info_prompt.txt,
    article_review_prompt.txt, fix_article_prompt.txt, article_html_prompt.txt
  generated/          ← ответы LLM
    structure.txt, article.txt, article_info.txt, review.txt,
    fixed_article.txt, generation_context.json
  article.html        ← результат стадии html
  result.md           ← итоговый файл
```

**Пути в БД относительны `OUTPUT_DIR`**, не абсолютны. Абсолютный путь в колонке — дефект.

---

## Staged commit

Механизм атомарности «файлы + БД». Три термина:

- **stage** (глагол) — записать артефакт во временный файл, ещё не публикуя. Методы `Stage*`
  у `output.Writer`.
- **persist** — колбэк, сохраняющий состояние в БД. Передаётся в `Commit` первым аргументом.
- **commit** — `articleoutput.Commit(persist, pending...)`: публикует файлы через `rename`,
  затем выполняет `persist`. При сбое `persist` файлы откатываются на предыдущую версию.

Публиковать файл отдельно от статуса нельзя. Подробнее — [ADR-0001](adr/0001-staged-commit.md).

---

## LLM: провайдер, target, fallback

- **Провайдер** (`provider`) — способ обращения к модели: `gemini`, `openai_compatible`,
  `deepseek_web`. Объявляется в `config.yaml` → `providers`.
- **Target** — пара «провайдер + модель» внутри стадии. У стадии их список.
- **Fallback** — переход к следующему target'у, когда предыдущий исчерпал попытки.
  Логируется как `LLM fallback selected`.

Единственный источник истины для маршрутизации — **`stage.Targets`**. Поля `stage.Provider` /
`stage.Model` — легаси, подлежат удалению (см. H5 в [бэклоге аудита](AUDIT-2026-08.md)).

---

## Claim

**Claim** — резервирование статьи для обработки. `ClaimNextIncomplete` выбирает статью
через `FOR UPDATE SKIP LOCKED`, поэтому параллельные процессы не получают одну и ту же.
Подробнее — [ADR-0002](adr/0002-skip-locked.md).

---

## Полный поток и demo-поток

- **Полный поток** (`generate`) — все шесть стадий: structure → article + info → review →
  fix → html → result.
- **Demo-поток** (`demo-generate`, `retry`) — сокращённый: prepare → structure →
  article + info → result. Без review, fix и html.
- **`run <external_id>`** — самый короткий: article + info → result, **без research**.
  Промпт получает пустые `{{.GeneratedStructure}}`, `{{.Keywords}}`, `{{.LSIWords}}`,
  поэтому результат заведомо низкого качества.
- **Dry-run** — изолированная БД (порт 5433) и stub-ответы вместо LLM. Сейчас сломан,
  см. C1/C2 в [бэклоге аудита](AUDIT-2026-08.md).
