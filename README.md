# SEO Pipeline

SEO Pipeline — CLI-приложение на Go для импорта заданий на статьи, сбора SEO-данных и генерации файлов.

Задачи работают с двумя площадками проекта — dpoprof.ru и obuchim-specialista.ru:

- **`task_1`** — старая реализация статей блога: работает как раньше, но сохранена только ради обратной совместимости и будет удалена; новое в неё не добавляется;
- **`pprof_1`** — статьи блога dpoprof.ru;
- **`pprof_2`** — коммерческие страницы услуг dpoprof.ru;
- **`obuch_1`** — статьи блога obuchim-specialista.ru;
- **`obuch_2`** — страницы услуг obuchim-specialista.ru;
- **`pprof_audit_1`**, **`pprof_audit_2`** — проверка уже опубликованных статей и страниц услуг: отчёт для человека, в блог ничего не пишется.

Новые изменения делаются в общем pipeline (`internal/pipeline`) и в задачах поверх него, а не в `task_1`. Движок у задач генерации общий, различаются они конфигурацией и порядком стадий: см. «Задачи генерации». Задачи правки уже опубликованных страниц (`pprof_fix_1`…`pprof_fix_9`) и `pprof_template_1` удалены 05.10.2026 — см. «Правка опубликованных страниц».

Состояние статей и пути к артефактам хранятся в PostgreSQL. Keys.so и Arsenkin используются через Playwright, генерационные этапы — через настроенные LLM-провайдеры. Для безопасной локальной проверки предусмотрен изолированный dry-run без внешних запросов.

## Структура проекта

- `cmd/seo-pipeline/` — CLI, сборка зависимостей и запуск операций; реестр задач — `tasks.go`.
- `config/config.yaml` — маршрутизация LLM-стадий `task_1` и параметры моделей.
- `config/config.deepseek.yaml` — наложение для режима `LLM_MODE=deepseek` (`task_1`).
- `config/pprof_1.yaml`, `config/pprof_2.yaml`, `config/obuch_1.yaml`, `config/obuch_2.yaml` — самостоятельные схемы стадий задач генерации, без наложения; там же секция `pipeline:` (публикация после прогона, Google Docs, Keys.so).
- `config/pprof_audit_1.yaml`, `config/pprof_audit_2.yaml` — схемы единственной стадии задач аудита (`audit`).
- `input/task_1/`, `input/pprof_1/`, `input/pprof_2/`, `input/obuch_1/`, `input/obuch_2/` — каталоги Excel импорта, у каждой задачи свой. Имя книги значения не имеет: импорт берёт единственный `.xlsx` каталога. Рядом — `images/` (обложки) и `regulations/` (регламенты, прикрепляемые к стадиям).
- `input/pprof_audit_1/`, `input/pprof_audit_2/` — вход задач аудита: файл со строками «индекс + ссылка», а книга Excel может дать ещё две необязательные колонки — ID записи и тему страницы.
- `internal/config/` — загрузка и проверка конфигурации.
- `internal/integrations/keysso/`, `internal/integrations/arsenkin/`, `internal/integrations/google/` — интеграции через Playwright.
- `internal/integrations/wordpress/` — публикация и чтение записей площадок; `sitepage/` — название программы с её страницы для перелинковки; `netbind/` — привязка HTTP-клиентов к сетевому интерфейсу (`NETWORK_INTERFACE`).
- `internal/catalog/` — каталог услуг площадок: сбор, разбор профессий, подбор услуг под статью; различия площадок — `site.go`.
- `internal/llm/` — маршрутизация стадий, таймауты и повторы; `gemini/` и `deepseekweb/` — клиенты провайдеров.
- `internal/storage/` — подключение к PostgreSQL.
- `internal/tasks/` — тип `Profile`: всё, чем одна задача отличается от другой;
  - `task1/`, `pprof1/`, `pprof2/`, `obuch1/`, `obuch2/` — профиль задачи (`profile.go`), у задач генерации кроме `task_1` — ещё свой поток (`flow.go`) и карточка или кнопка призыва (`cta.go`); пакеты задач друг друга не импортируют;
  - `pprofaudit1/`, `pprofaudit2/` — профили задач аудита: каталоги, схема, регламент проверки и список обязательных полей. Кода потока в них нет.
- `internal/pipeline/` — общий движок задач:
  - `article/` — модели статьи, этапа, результата;
  - `importer/` — чтение Excel и отчёт импорта;
  - `repository/` — доступ к PostgreSQL и проверка схемы;
  - `generation/` — pipeline генерационных стадий, чистка и оформление разметки (`blocks.go`);
  - `taskflow/` — общее у потоков задач: открыть диалог, отрендерить промпт, записать ошибку статьи;
  - `keywords/` — резервный подбор исходных запросов моделью;
  - `llmchat/` — диалог с моделью поверх роутера: «начать беседу» и «продолжить»;
  - `articleaudit/` — поток проверки уже опубликованных страниц: `fetch → audit → report`. Общий у `pprof_audit_1` и `pprof_audit_2`; в блог не пишет ничего — метода записи нет в самом интерфейсе площадки;
  - `pagebatch/` — разбор входного файла аудита и предохранитель прогона;
  - `output/` — атомарная публикация артефактов;
  - `diagnostics/` — снимки prepare, отчёт проверок, логи по статьям;
  - `demo/` — сборка каталога `DEMO` для ручного прогона;
  - `result/` — сборка `result.md`;
  - `validator/` — проверки без отдельной CLI-команды.
- `migrations/<задача>/` — схема PostgreSQL каждой задачи: baseline `000001_schema.up.sql` и пронумерованные файлы поверх него; общего каталога нет. `migrations/site/` и `migrations/site_obuchim/` — схемы каталогов услуг площадок. Раскладка — `migrations/README.md`.
- `tasks/task_1/prompts/` — промпты стадий `task_1`; `deepseek/` — версии для одного диалога.
- `tasks/common/prompts/demo/` — объединённый промпт ручного чата DEMO, общий у всех задач.
- `tasks/common/templates/blocks/` — шаблоны оформления блоков тела статьи, общие у `obuch_1` и `obuch_2`.
- `tasks/pprof_1/prompts/`, `tasks/pprof_2/prompts/`, `tasks/obuch_1/prompts/`, `tasks/obuch_2/prompts/` — промпты задач плоским списком, порядок виден по номерам; чужих промптов задача не использует.
- `tasks/pprof_audit_1/prompts/audit.txt`, `tasks/pprof_audit_2/prompts/audit.txt` — регламенты проверки.
- `tasks/<задача>/templates/` — шаблон `result.md`, а у задач с призывом — шаблон карточки или кнопки (`cta_card.html`, `cta_button.html`).
- `tasks/<задача>/output/` — создаваемые артефакты статей.
- `output/<задача>/` — отчёты импорта (`import-reports/`) и диагностика неудачных попыток Keys.so, Arsenkin, DeepSeek и Google (`debug/`); у `task_1` каталог называется `output/task1/`. У каждой задачи свои: `reset` одной не трогает данные другой.

## Задачи генерации

Задача — это отдельный пайплайн со своими каталогами, промптами, схемой стадий и схемой PostgreSQL. Набор команд у задач генерации общий: `make <task> <операция>`, где `<task>` — `task-1`, `pprof-1`, `pprof-2`, `obuch-1` или `obuch-2`. Движок общий (`internal/pipeline`), конфигурация — в `internal/tasks/<задача>`, а у задач кроме `task_1` ещё и свой поток генерации. Пакеты задач друг друга не импортируют.

| Что | `task_1` | `pprof_1` | `pprof_2` | `obuch_1` | `obuch_2` |
|---|---|---|---|---|---|
| Площадка | dpoprof.ru | dpoprof.ru | dpoprof.ru | obuchim-specialista.ru | obuchim-specialista.ru |
| Что пишет | статьи блога | статьи блога | **коммерческие страницы услуг** | статьи блога | **страницы услуг** |
| Провайдеры | Gemini + DeepSeek Web, схема выбирается на статью | только DeepSeek Web | только DeepSeek Web | только DeepSeek Web | только DeepSeek Web |
| Схемы стадий | `config/config.yaml` + наложение `config.deepseek.yaml` | `config/pprof_1.yaml`, одна | `config/pprof_2.yaml`, одна | `config/obuch_1.yaml`, одна | `config/obuch_2.yaml`, одна |
| Стадии | `structure`, `article`, `info`, `review`, `fix`, `html` | `structure`, `expert`, `review`, `info`, `html` | `structure`, `article`, `review`, `html` | `structure`, `expert`, `review`, `info`, `html` | `structure`, `article`, `review`, `html` |
| Стадия `info` | есть | есть | **нет**: FAQ разбирается из текста страницы, TL;DR нет | есть | нет |
| Перелинковка | стадия `html` | стадия `html` | **нет**: ссылки в разметке запрещены | стадия `html` | нет |
| Призыв в конце | нет | карточка ДПО ПРОФ (код) | кнопка заявки (код) | карточка `sp-cta-card` (код) | кнопка заявки (код) |
| Промпты | `tasks/task_1/prompts/` | `tasks/pprof_1/prompts/` | `tasks/pprof_2/prompts/` | `tasks/obuch_1/prompts/` | `tasks/obuch_2/prompts/` |
| Артефакты | `tasks/task_1/output/` | `tasks/pprof_1/output/` | `tasks/pprof_2/output/` | `tasks/obuch_1/output/` | `tasks/obuch_2/output/` |
| Схема PostgreSQL | `public` | `pprof_1` | `pprof_2` | `obuch_1` | `obuch_2` |
| Колонки `article_inputs` сверх обязательных | `author`, `links`, `professions`, `tags` | `author`, `course_url`, `links`, `professions`, `seo_title`, `tags` | `seo_title`, `section`, `profession`, `teachers`, `service_name`, `hours`, `duration`, `price`, `document`, `attestation` | `course_url`, `links`, `seo_title`, `tags` | `seo_title`, `profession`, `post_type`, `hours`, `duration`, `price`, `document`, `attestation`, `image_source_url` |
| Схема PostgreSQL собирается из | `migrations/task_1/` | `migrations/pprof_1/` | `migrations/pprof_2/` | `migrations/obuch_1/` | `migrations/obuch_2/` |
| Excel | `input/task_1/*.xlsx` | `input/pprof_1/*.xlsx` | `input/pprof_2/*.xlsx` | `input/obuch_1/*.xlsx` | `input/obuch_2/*.xlsx` |
| Префикс переменных | нет | `PPROF_1_` | `PPROF_2_` | `OBUCH_1_` | `OBUCH_2_` |
| Каталог услуг | — | `site` | `site` | `site_obuchim` | `site_obuchim` |
| Папка Google Drive | общая | общая | своя | своя | своя |

### `pprof_1` — статьи блога dpoprof.ru

```text
Чат 1   structure                       → generated/structure.txt
Чат 2   1_expert → 2_editor → info      → article.txt, fixed_article.txt, TL;DR и FAQ
Чат 3   4_html + карточка призыва       → article.html → result.md
```

- Чат 2 неделим: браузерная беседа не переживает процесс, поэтому `article`, `info`, `review` и `fix` — четыре имени одного действия, прогоняющего чат 2 целиком.
- Редактура одна — `review` с промптом `2_editor.txt`; метаданные пишутся по отредактированному тексту.
- Регламент статьи (`input/pprof_1/regulations/article`) прикреплён к `structure` и `expert`, регламент вёрстки (`regulations/html`) — к `html`. Поиск в интернете включён только у `expert`.
- Базовый `tasks/pprof_1/prompts/article.txt` **в модель не уходит**: он сохраняется в `prompts/article_prompt.txt` и выгружается в Google Docs, а текст пишет `expert`.
- Статья заканчивается **карточкой ДПО ПРОФ, которую ставит код** (шаблон `tasks/pprof_1/templates/cta_card.html`). Адрес кнопки — колонка `course_url`; пустая колонка роняет статью до первого сообщения модели.
- Ссылок перелинковки (колонка `links`) в тексте должно быть от трёх; недостающие код доспрашивает у модели короткой строкой в том же чате.
- Под статьёй публикуются три связанных курса из каталога услуг (`related_courses`); они же печатаются в `result.md`.

### `pprof_2` — страницы услуг dpoprof.ru

```text
Чат 1   structure          → generated/structure.txt
Чат 2   article → review   → article.txt (черновик), fixed_article.txt (финал)
        разбор FAQ         → article_metadata.faq
Чат 3   html + кнопка      → article.html → result.md
```

- Страницу пишет основной промпт `article.txt` — **он уходит в модель** и он же выгружается в Google Docs. Редактура `review` возвращает страницу целиком.
- Стадии `info` нет: FAQ разбирается из текста после ревью (`pprof2.ExtractFAQ`), TL;DR и времени чтения нет. Пустой FAQ генерацию не роняет, но публикацию останавливает.
- Перелинковки нет: ссылки в разметке запрещены. Кнопку заявки ставит код из `tasks/pprof_2/templates/cta_button.html`.
- Числа программы (`hours`, `duration`, `price`, `document`, `attestation`) промпт подставляет дословно; пустая колонка — прежнее правило по виду программы.
- Блок вопросов и карточка преподавателя после публикации появляются на странице только после одного «Обновить» записи в админке WordPress.

Колонки книги (подробно — `input/pprof_2/README.md`):

| Excel | Куда идёт |
|---|---|
| `id` | `external_id`, идентификатор во всех командах |
| `article_name` | заголовок статьи и записи |
| `service_name` | «Название услуги» — сама услуга, без уточнений |
| `slug` | каталог артефактов, адрес записи и «URL картинки» |
| `header` | «Заголовок (H1)» и alt обложки |
| `seo_title`, `meta_description`, `key_word` | SEO-заголовок, мета-описание, фокусное слово |
| `reference_url` | вход Keys.so на этапе `prepare` |
| `category`, `section` | «Категория» и «Рубрика» — два уровня каталога |
| `profession`, `teachers` | «Название профессии», «Преподаватели» |
| `hours`, `duration`, `price`, `document`, `attestation` | числа программы |

Обязательны `id`, `article_name`, `slug`, `reference_url`.

### `obuch_1` — статьи блога obuchim-specialista.ru

Те же стадии и границы чатов, что у `pprof_1` (`structure` → `expert` → `review` → `info` → `html`), но свой поток (`internal/tasks/obuch1`), свои промпты и своя площадка — доступ по `OBUCH_1_WORDPRESS_*`.

- Чат разметки принимает пять сообщений: три на разметку, одно на доспрос перелинковки, одно на строки карточки призыва.
- Последний раздел — призыв одним абзацем; под ним код ставит карточку `sp-cta-card` из `tasks/obuch_1/templates/cta_card.html`. Адрес кнопки — колонка `course_url`: пустая или отсутствующая в каталоге услуг площадки роняет статью до первого сообщения модели.
- Стилей от модели в теле нет: чистка `CleanPlainBlogMarkup` переводит разметку в классы темы `wp-block-*`, а блоки, помеченные моделью (`sp-note`, `sp-stats`, `sp-steps`, `sp-proscons`), оформляет код по шаблонам `tasks/common/templates/blocks/`.
- Регламенты: `input/obuch_1/regulations/article` (к `structure` и `expert`) и `input/obuch_1/regulations/html` (к `html`).
- Колонки сверх обязательных: `course_url`, `links`, `seo_title`, `tags`.
- Публикация: рубрика «Блог», метки, ярлык, обложка, тело и три ключа Yoast; полей ACF у площадки нет, картинку в тело задача не вставляет. `publish_after_run: true`, Google Docs — своя папка.

### `obuch_2` — страницы услуг obuchim-specialista.ru

Относится к `pprof_2` так же, как `obuch_1` к `pprof_1`: стадии `structure` → `article` → `review` → `html`, свой поток (`internal/tasks/obuch2`), доступ по `OBUCH_2_WORDPRESS_*`.

- Форму страницы задаёт **скелет из десяти H2**, снятый с живых страниц; чат 1 заполняет в нём заголовки, модули программы и портреты аудитории.
- **Числа программы приходят колонками книги** (`hours`, `duration`, `price`, `document`, `attestation`) и подставляются дословно. Перелинковки нет. Поиск в интернете включён только у `article` — ради источника под таблицей заработка.
- Таблиц на странице две: плашка параметров и таблица заработка с источником.
- Кнопку заявки ставит код (`tasks/obuch_2/templates/cta_card.html`); у модели спрашивается только надпись на ней.
- Публикация уходит в тип записи из колонки `post_type` с рубрикой `cat_<тип>`, и **поля программы заполняет код**: плашка «Параметры программы» и аккордеон модулей (разбираются из готовой разметки, `obuch2.ParseModules`). Публикация останавливается до первого запроса в блог, если часы модулей не сходятся с объёмом из книги, если часы, срок или цена в тексте расходятся с книгой (`obuch2.CheckProgramFacts`) или если документ и аттестация книги не совпадают со служебными значениями типа записи (`obuch2.CheckProgramPreset`).
- Колонки сверх обязательных: `seo_title`, `profession`, `post_type`, `hours`, `duration`, `price`, `document`, `attestation`, `image_source_url`.
- `publish_after_run: true`, Google Docs — своя папка.

### Первый запуск задачи генерации

Схема PostgreSQL каждой задачи создаётся руками — migration runner в проекте нет. Применяются **только файлы своей задачи** по порядку номеров:

```bash
# 1. Схема и её таблицы (пример — obuch_1; у pprof_1 файлов три, у pprof_2 — два)
docker exec -i seo-postgres psql -U seo -d seo -c 'CREATE SCHEMA IF NOT EXISTS obuch_1'
for f in migrations/obuch_1/*.up.sql; do
  docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
    -c 'SET search_path TO obuch_1' -f - < "$f"
done

# 2. Входные данные
cp <книга>.xlsx input/obuch_1/
cp <регламенты> input/obuch_1/regulations/article/ input/obuch_1/regulations/html/
cp <обложки>.webp input/obuch_1/images/      # имя файла — external_id

# 3. Каталог услуг площадки — нужен перелинковке, сверке course_url и связанным курсам
make obuch-1 catalog-sync

# 4. Импорт и сверка — ни денег, ни внешних сервисов
make obuch-1 import
make obuch-1 import-check
make obuch-1 wordpress-check
```

Дальше `prepare` и `run` — они **тратят деньги** и ходят в Keys.so, Arsenkin и DeepSeek. То же самое понадобится в базе dry-run на `localhost:5433`, если планируется `dry-run`. Пока схемы нет, любая команда задачи останавливается на проверке схемы — это ожидаемо и безопасно.

### Каталог услуг площадки

Каталог — программы площадки, их рубрики и профессии. Он нужен перелинковке, сверке адреса кнопки (`course_url`) и блоку связанных курсов. Каталог принадлежит площадке, а не задаче:

| Площадка | Схема | Задачи |
|---|---|---|
| dpoprof.ru | `site` | `pprof_1`, `pprof_2` |
| obuchim-specialista.ru | `site_obuchim` | `obuch_1`, `obuch_2` |

```bash
# один раз: пустые таблицы каталогов
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 -f - < migrations/site/000001_schema.up.sql
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 -f - < migrations/site_obuchim/000001_schema.up.sql

make pprof-1 catalog-sync      # собрать каталог своей площадки; блог только читается
make pprof-1 catalog-show 45   # какие услуги подберутся статье, без записи
```

`catalog-sync` переписывает услуги начисто (около полуминуты), а профессии и их синонимы, добавленные руками, сохраняет. Код — `internal/catalog/`.

### Правка опубликованных страниц

Задачи правки уже опубликованных страниц `pprof_fix_1`…`pprof_fix_9` и задача `pprof_template_1` удалены 05.10.2026. Опубликованные страницы теперь правятся вручную, вне приложения. Оригиналы правленых страниц, входные пачки и промпты сохранены в `archive/pprof_fix-2026-10-05/` (вне git); схемы `pprof_fix_*` и `pprof_template_1` остались в базе как история и приложением не используются.

## Задачи `pprof_audit_1` и `pprof_audit_2` — проверка опубликованных страниц

Они **проверяют опубликованную страницу** и складывают находки в отчёт для человека. `pprof_audit_1` — статьи блога, `pprof_audit_2` — страницы услуг. Поток общий (`internal/pipeline/articleaudit`), задача добавляет к нему свой регламент проверки (`tasks/<задача>/prompts/audit.txt`) и свой список обязательных полей.

**В блог они не пишут ничего** — интерфейс площадки у аудита состоит только из читающих методов.

```text
make pprof-audit-1 import         индексы и ссылки из файла в input/pprof_audit_1
make pprof-audit-1 run plan [ID]  что будет проверено и какие поля пусты; модель не спрашивается
make pprof-audit-1 run [ID]       проверка и отчёт; без ID — все непроверенные
make pprof-audit-1 report         сводка по пачке; ни сети, ни модели, ни денег
make pprof-audit-1 reset          задача к нулю: пустая таблица и пустые артефакты
```

### Что проверяется

- **Текст страницы** — модель по регламенту: оценка `X/20` и разбор по критериям (балл и фраза на каждый), критические ошибки, все ошибки списком. Модели уходят только название, тело и блок вопросов; полей записи она не видит.
- **Запись** — код по закрытому списку обязательных полей (`ArticleAudit.RequiredFields`). У `pprof_audit_2` — десять: рубрика, название записи, обложка, `prof_title`, `prof_name`, фокусное слово, SEO-заголовок, мета-описание, `teachers`, `faq_loop`. У `pprof_audit_1` — двенадцать: рубрика, метки, название записи, `prof_title`, `prof_name`, `blog_tldr`, `blog_read`, `author_link`, `related_courses`, фокусное слово, SEO-заголовок, мета-описание. Незаполненное попадает в общий список ошибок.
- **Длина полей выдачи** — код: `_yoast_wpseo_title` до 60 знаков, `_yoast_wpseo_metadesc` до 160; `run plan` печатает запас («55 из 60»).
- **Перелинковка** — код: у `pprof_audit_1` норма три внутренние ссылки, у `pprof_audit_2` проверка выключена.
- **Блок FAQ** — у `pprof_audit_1` норма шесть вопросов, у `pprof_audit_2` число печатается без оценки.

### Вход

Один файл в `input/<задача>/`, имя любое: книга Excel, HTML, CSV или текст со строками «индекс + ссылка». Правило — **первое число до первой ссылки** в строке; ссылка без индекса и повтор индекса — отказ импорта с номером строки. Индекс — это `external_id`, слаг берётся из адреса.

Книга Excel может дать ещё две необязательные колонки с заголовками: **ID записи** (один запрос вместо перебора; слаг найденной записи всё равно сверяется со ссылкой) и **тема** страницы (основание для проверки «о том ли поле»).

### Отчёт и артефакты

```text
tasks/pprof_audit_1/output/
  summary.md                 сводка: оценки худшими вперёд, где теряются баллы, что не заполнено, частые ошибки
  fixes.xlsx                 та же пачка списком задач для человека
  <индекс>-<слаг>/
    original/article.html    страница, как она прочитана из блога
    original/fields.json     поля записи
    prompts/audit_prompt.txt что ушло в модель
    generated/audit.txt      сырой ответ модели
    result.md                отчёт страницы
```

`result.md` начинается с оценки `X/20` (ненайденная — словами, а не нулём), ссылки и строки баллов по критериям; дальше — детальный разбор, критические ошибки, все ошибки и «Не разобрано» — всё, что не легло ни в один раздел ответа. `summary.md` и `fixes.xlsx` собирает `report` из сохранённых артефактов, поэтому пересобирать их можно сколько угодно.

### Первый запуск

```bash
# 1. Схема PostgreSQL — руками, как у остальных задач
docker exec -i seo-postgres psql -U seo -d seo -c 'CREATE SCHEMA IF NOT EXISTS pprof_audit_1'
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
  -c 'SET search_path TO pprof_audit_1' -f - < migrations/pprof_audit_1/000001_schema.up.sql

# 2. Переменные площадки (значения те же, что у pprof_2):
#    PPROF_AUDIT_1_WORDPRESS_URL, PPROF_AUDIT_1_WORDPRESS_USERNAME, PPROF_AUDIT_1_WORDPRESS_APP_PASSWORD

# 3. Вход и план — ни рубля за модель
make pprof-audit-1 import
make pprof-audit-1 run plan
```

Метка `<!-- ЗАПОЛНИТЬ -->` в промпте останавливает `run` до модели; `run plan` работает и с ней. Формат ответа в конце промпта — контракт с разбором, его держит тест `TestArticleAuditPromptFormatMatchesParser`.

## Требования и конфигурация

Нужны Go 1.25 и PostgreSQL с применёнными миграциями. Для `prepare` требуется Playwright/Chromium и доступ к Keys.so и Arsenkin; для генерационных команд — настроенные LLM-провайдеры.

По умолчанию приложение ищет `.env` на один каталог выше корня проекта. Путь можно переопределить через `ENV_FILE`; старое имя `SEO_PIPELINE_ENV` также поддерживается. Переменные окружения имеют приоритет над `.env`.

| Переменная | Назначение |
|---|---|
| `DATABASE_URL` | Подключение к основной PostgreSQL. Общее для задач: разводит их `search_path` из профиля, а не отдельный DSN. |
| `APP_ENV` | Окружение; dry-run разрешён только для `local` и `test`. |
| `DRY_RUN_DATABASE_URL` | Отдельная БД dry-run; по умолчанию `seo_dry_run` на `localhost:5433`. |
| `TEST_DATABASE_URL` | БД для тестов `internal/pipeline/repository`. Без неё тесты репозитория молча пропускаются, а `make test` остаётся зелёным. |
| `INPUT_FILE_PATH`, `OUTPUT_DIR` | Книга Excel и каталог артефактов `task_1`; по умолчанию единственный `.xlsx` из `input/task_1/` и `tasks/task_1/output`. |
| `<ПРЕФИКС>OUTPUT_DIR`, `<ПРЕФИКС>INPUT_FILE_PATH`, `<ПРЕФИКС>DATABASE_URL`, `<ПРЕФИКС>DRY_RUN_DATABASE_URL` | Точечное переопределение задачи. Префиксы: `PPROF_1_`, `PPROF_2_`, `OBUCH_1_`, `OBUCH_2_`, `PPROF_AUDIT_1_`, `PPROF_AUDIT_2_`. Переменная без префикса принадлежит `task_1` и в другие задачи не протекает. |
| `<ПРЕФИКС>WORDPRESS_URL`, `<ПРЕФИКС>WORDPRESS_USERNAME`, `<ПРЕФИКС>WORDPRESS_APP_PASSWORD` | Площадка задачи: адрес, логин, Application Password. У `pprof_*` — dpoprof.ru, у `obuch_*` — obuchim-specialista.ru. |
| `NETWORK_INTERFACE` | Сетевой интерфейс для HTTP-клиентов `wordpress` и `sitepage`, например `en0`. Нужен, когда VPN-туннель рвёт TLS с obuchim-specialista.ru (команда падает с `EOF`); пусто — прежнее поведение. Playwright им не пользуется. |
| `LLM_MODE` | Режим маршрутизации `task_1`: пусто или `gemini` — обычная схема, `deepseek` — все стадии через DeepSeek Web. На остальные задачи не влияет. |
| `GEMINI_API_KEY`, `GEMINI_MODEL` | Доступ и модель Gemini. |
| `OPENROUTER_API_KEY`, `OPENROUTER_MODEL` | Доступ и модель OpenRouter. Провайдер объявлен в `config/config.yaml`, но ни одной стадии по умолчанию не назначен. |
| `KEYS_SO_EMAIL`, `KEYS_SO_PASSWORD` | Доступ к Keys.so. |
| `ARSENKIN_EMAIL`, `ARSENKIN_PASSWORD` | Доступ к Arsenkin. |
| `ARSENKIN_HEADLESS` | Headless-режим Arsenkin, по умолчанию `true`. |
| `LOG_LEVEL` | `debug`, `info`, `warn` или `error`. |
| `LOG_FORMAT` | `auto` (по умолчанию), `pretty`, `text` или `json`. См. «Логи». |
| `ENV_FILE` | Явный путь к env-файлу. |

Keys.so, Arsenkin, DeepSeek и Google у задач общие: аккаунты одни и те же.

**Секция `pipeline:` в конфиге задачи** (`config/<задача>.yaml`) решает, чем прогон занимается помимо текста:

| Ключ | Умолчание | Что делает |
|---|---|---|
| `publish_after_run` | `false` | публиковать статью в WordPress сразу после `run` и `regenerate` |
| `google_docs` | `true` | выгружать промпт статьи в Google Docs по ходу прогона |
| `keysso` | `true` | собирать запросы у конкурента в Keys.so; `false` — запросы подбирает модель |

Сегодня `publish_after_run: true` у `pprof_1`, `pprof_2`, `obuch_1` и `obuch_2`. Отменить публикацию приложение не умеет.

LLM-стадия может содержать упорядоченный fallback `targets`. Старые поля `provider` и `model` остаются допустимы и означают один target:

```yaml
targets:
  - provider: gemini
    model: ${GEMINI_MODEL}
  - provider: openrouter
    model: ${OPENROUTER_MODEL}
```

### Первый локальный запуск

```bash
cp .env.example ../.env      # конфигурация ожидается рядом с каталогом проекта
make docker-up
make login deepseek          # ручной вход, один раз
make login google            # если нужна выгрузка промптов в Google Docs
```

Дальше — схема и вход задачи (см. «Первый запуск задачи генерации»). Для безопасной проверки всего pipeline без внешних сервисов и платных API — `make <задача> dry-run`.

## Makefile

Операции запускаются через namespace задачи: `make <task> <operation> [аргумент]`, где `<task>` — `task-1`, `pprof-1`, `pprof-2`, `obuch-1`, `obuch-2`, `pprof-audit-1` или `pprof-audit-2`. Рецепт у задач один, поэтому набор команд, разбор аргументов и тексты ошибок у задач генерации совпадают; `make help` показывает основные из них. Аргумент — `external_id` из Excel, не внутренний `articles.id`.

Без ID команда последовательно обрабатывает все статьи, которым по состоянию PostgreSQL нужен этот этап; с ID — только указанную. Ошибка одной статьи фиксируется в её статусе и логах, остальные продолжают обрабатываться, а итоговый код остаётся ненулевым.

Вход в сервисы общий для задач: `make login deepseek`, `make login google`, `make login keysso` (капча нового аккаунта Keys.so в видимом окне). Прежние `make <task> deepseek-login` и `make <task> google-login` оставлены алиасами. **`make login deepseek` удаляет сохранённый профиль до входа.**

### Шпаргалка

Пометки: **$** — тратит деньги на LLM, **→** — ходит во внешний сервис (Keys.so, Arsenkin, Google, WordPress), **!** — необратимо. `37` и `45` — примеры `external_id`.

#### Задачи генерации: `pprof_1`, `pprof_2`, `obuch_1`, `obuch_2`

Команды одинаковы у всех четырёх; ниже — на примере `pprof-1`.

```bash
# Импорт и сверка
make pprof-1 import                 # весь Excel
make pprof-1 import 10              # только первые 10 новых строк
make pprof-1 import-check [37]      # сверить импорт с Excel

# Ключевые запросы вручную
make pprof-1 keywords 37            # спросит запросы и заменит ими сбор Keys.so

# Сбор research                     →
make pprof-1 prepare [37]

# Генерация                         $ →
make pprof-1 run [37]               # полный прогон с возобновлением; generate — то же
make pprof-1 run plan [37]          # где возобновится, ничего не запуская
make pprof-1 article [37]           # чат 2 целиком; info, review, fix — то же самое
make pprof-1 html [37]              # чат 3: разметка
make pprof-1 retry [37]             # снять ошибку и прогнать
make pprof-1 regenerate 37          # пересоздать статью целиком, research сохраняется
make pprof-1 result [37]            # собрать result.md

# Диагностика
make pprof-1 errors [37]            # текущая сохранённая ошибка
make pprof-1 dry-run                # офлайн-прогон; требует схемы задачи на 5433

# Google Docs                       →
make pprof-1 google-publish [45]    # промпты в Google Docs, без LLM

# Каталог услуг                     →
make pprof-1 catalog-sync           # собрать каталог своей площадки
make pprof-1 catalog-show 45        # что подберётся статье, без записи

# WordPress                         → !
make pprof-1 wordpress-check        # доступ к площадке, без записи
make pprof-1 publish plan [45]      # что уйдёт в WordPress, без записи
make pprof-1 publish 45             # опубликовать одну статью                !
make pprof-1 publish                # все готовые, с подтверждением           !
make pprof-1 republish 45           # переписать тело, заголовок и поля опубликованной записи !
make pprof-1 mark-published 45 21593  # привязать к существующей записи блога

# DEMO для ручного прогона          $
make pprof-1 demo-generate [45]

# Очистка                           !
make pprof-1 clear 37               # одну статью к состоянию после импорта
make pprof-1 reset 37               # одну статью к состоянию до импорта
make pprof-1 reset                  # стереть состояние задачи
```

#### `task_1`

Те же команды через `make task-1 …`. Отличия: у `task_1` настоящие отдельные `article` (он же `info`), `review` и `fix`; `fix` продолжает чат ревью и при отдельном запуске восстанавливает историю из `article.txt` и `review.txt` без обращения к модели.

#### `pprof_audit_1`, `pprof_audit_2`

```bash
make pprof-audit-1 import [5]       # индексы и ссылки из input/pprof_audit_1/
make pprof-audit-1 wordpress-check  # доступ к площадке, без записи      →
make pprof-audit-1 run plan [12]    # что будет проверено, какие поля пусты →
make pprof-audit-1 run [12]         # проверка и отчёт                   $ →
make pprof-audit-1 report           # сводка по пачке, без сети и модели
make pprof-audit-1 reset            # задача к нулю, спросит подтверждение !
```

У `run` нет `!` — в блог аудит не пишет. Необратим только `reset`: вместе с таблицей он удаляет оплаченные отчёты.

#### Общие команды

```bash
make login deepseek | google | keysso   # ручной вход, откроет браузер →

make docker-up                      # поднять оба PostgreSQL и дождаться
make docker-start                   # то же самое
make docker-stop                    # остановить контейнеры, они сохраняются
make docker-restart                 # перезапустить
make docker-ps                      # состояние сервисов
make docker-logs                    # последние 100 строк и слежение
make docker-down                    # удалить контейнеры и сеть, volumes сохраняются

make test                           # go test ./...
make test-race                      # go test -race ./...
make fmt | vet | lint | lint-fix    # форматирование и анализ
make build                          # bin/seo-pipeline
make help                           # короткий список команд
```

`postgres` доступен на `localhost:5432`, `postgres-dry-run` — на `localhost:5433`. Оба при первой инициализации volume применяют `migrations/task_1/*.up.sql` в `public`; схемы остальных задач создаются руками.

`make test` зелёный и без `TEST_DATABASE_URL`, но тесты репозитория при этом пропускаются.

## CLI без Makefile

```bash
go run ./cmd/seo-pipeline <задача> <операция> [external_id]   # task-1 или task_1, pprof-1 или pprof_1, …
go run ./cmd/seo-pipeline task-1 run --plan
go run ./cmd/seo-pipeline task-1 reset --yes
go run ./cmd/seo-pipeline login deepseek
```

Флаг `--yes` поддерживается только для `reset` и `clear`. При `Ctrl+C`, `SIGINT` или `SIGTERM` текущие операции прекращаются, а созданные ресурсы закрываются.

## Импорт

Книга Excel берётся из каталога задачи (`input/<задача>/`): подходит единственный `.xlsx`, имя значения не имеет. Нет файлов или их несколько — импорт называет каталог и перечисляет найденное; явный путь — `<ПРЕФИКС>INPUT_FILE_PATH`. Используется лист `Лист1`, а если его нет — первый лист.

Обязательны `id`, `article_name`, `image_slug` (у книг услуг — `slug`) и `reference_url`; остальные колонки задача объявляет своим профилем. Значение `NULL` без учёта регистра считается пустым.

- Импорт возобновляемый: существующий `external_id` пропускается без обновления статуса и результатов. Колонки `article_inputs`, у которых значение `NULL`, импорт дозаполняет, заполненные не переписывает.
- `import 10` — первые 10 новых валидных статей; существующие, пустые и некорректные строки в лимит не входят.
- Ошибки строк не останавливают остальные; дубликаты `external_id` после первой корректной строки отмечаются как ошибки файла.
- Отчёты — `output/<задача>/import-reports/import-<timestamp>.json` и `latest.json`.
- `import-check` сверяет импорт с Excel, ничего не меняя: полноту переноса, уникальность `external_id`, совпадение полей и связь `articles ↔ article_inputs`.

**Сменить тему статьи повторным импортом нельзя**: заполненные значения он не трогает, и статья пишется по данным на момент импорта.

## Pipeline

### Ключевые запросы

Исходный список запросов берётся из первого доступного источника:

1. **Ручная вставка** — `make <задача> keywords <external_id>`: по запросу в строке (можно вставлять колонку из Excel вместе с частотностями), конец — пустая строка или Ctrl-D. Перезапись безусловная: прежний research стирается, статья возвращается к `pending` / `arsenkin_collection`. Наружу команда не ходит.
2. **Keys.so** — сбор по `reference_url` конкурента и чистка дублей (`delete-double`).
3. **Резервный подбор моделью** (промпт `keywords`) — при любом отказе Keys.so, кроме несостоявшегося входа: тогда статья падает с подсказкой `make login keysso`. Отказ пишется в лог как «ПРОБЛЕМА С KEYS.SO» и в `prepare-report.json`.

Чистка от дублей Keys.so проходит при любом источнике. При `pipeline.keysso: false` браузер не открывается, дубли снимает модель.

Перед формой Wordstat клиент Arsenkin заменяет в фразах всё, кроме букв и цифр, пробелом (форма молча не принимает `-`, `/`, `"`) и обрезает список до 49 запросов; что вычищено — в логе `wordstat_sanitize`. В `cleaned_keywords` запросы остаются как пришли. Частотности даёт только Wordstat.

### Подготовка research

`prepare [external_id]` собирает Keys.so и Arsenkin (Wordstat и Copywriters) целиком в памяти и только после успеха атомарно заменяет research; при ошибке прежние данные сохраняются. Причины отказов Keys.so: `no_data` (окончательный, без повторов), `maintenance`, `navigation_error`, `timeout`, `unexpected_page`. Снимки неудачных попыток (`screenshot.png`, `page.html`, `info.json` без cookies) — в `output/<задача>/debug/keysso/` и `debug/arsenkin/`.

### DeepSeek Web

Провайдер `deepseek_web` работает через веб-интерфейс `chat.deepseek.com` и Playwright; профиль — `data/browser/deepseek/`. Первый вход — `make login deepseek`: профиль удаляется, в видимом окне вход проходит человек. Если профилем пользуется другой процесс, команда откажется работать.

Проверка Cloudflare посреди работы — не блокировка: открывается видимое окно с тем же профилем, проверку проходит человек, и стадия повторяется. Не прошли — ошибка `DeepSeek requires manual captcha verification`.

Режим ответа (`mode`: `default`, `expert`, `vision`) и поиск в интернете (`search: true`) задаются на стадию в `config/<задача>.yaml`.

### Два режима LLM (`task_1`)

| `LLM_MODE` | Схема |
|---|---|
| не задан или `gemini` | `structure`, `info`, `html` → DeepSeek; `article`, `review`, `fix` → Gemini |
| `deepseek` | `config/config.yaml` + `config/config.deepseek.yaml`: все стадии через DeepSeek, одна беседа на статью |

Режим выбирается один раз на статью. Если Gemini исчерпал квоту, он выключается на 24 часа (`data/llm/gemini-unavailable`; удалить файл — вернуть досрочно), а текущая статья переделывается через DeepSeek.

### Публикация промпта в Google Docs

Сохранённый промпт статьи (`prompts/article_prompt.txt`) уходит в Google Docs документом `Промт: <название статьи>`: найденный документ перезаписывается, копий не появляется. Папка — общая [Статьи ДПО ПРОФ](https://drive.google.com/drive/folders/1N-NRlswacwqKWUOEiA1OS3tKT_V_yLiS) или своя у задачи (`Profile.GoogleFolderURL`).

- Первый вход — `make login google` (в установленном Chrome); профиль — `data/browser/google/`.
- Повторная выгрузка без генерации — `make <задача> google-publish [ID]`; запуск после обрыва безопасен.
- Публикация идёт фоном и генерацию не роняет; без сессии в логе `needs_manual_login=true`. Временные отказы повторяются до 3 раз.
- Ссылка ложится в `article_outputs.google_doc_url` и печатается разделом «Гугл Док» в `result.md`; появилась позже — `make <задача> result <ID>`.

### Публикация в WordPress

- `publish plan [ID]` — что уйдёт в запись, без единого запроса на запись. `publish [ID]` **необратимо** создаёт запись: сначала обложка `input/<задача>/images/<external_id>.webp`, потом запись. Без ID уходят все готовые статьи.
- Рубрика обязана существовать в блоге; метки из `tags` заводятся сами (опечатка даст мусорную метку — смотрите `publish plan`).
- Адрес записи — из `image_slug`, SEO-заголовок — из `seo_title` (пустой — название статьи).
- От повтора защищает отметка `articles.wordpress_status`; `mark-published <id> [post_id]` привязывает статью к уже существующей записи.
- `regenerate` выложенную статью заново не публикует; новый текст в блог уносит `republish <id>` — переписывает тело, заголовок, связанные курсы, TL;DR и время чтения.
- После трёх подряд отказов площадки публикация в этом прогоне выключается, остальные статьи проходят все этапы.

### Run, retry и regenerate

- `run` — полный pipeline с возобновлением: готовые этапы пропускаются по сохранённым артефактам; `completed` ставится только после `html` и сборки `result.md`. Без ID берёт все статьи со `status <> 'completed'`. `run plan` показывает точку возобновления.
- `retry` снимает сохранённую ошибку и проводит статью тем же раннером.
- `regenerate <external_id>` сбрасывает генерацию одной статьи и проводит её заново; импорт и research сохраняются. Итог проверяется: `completed`, непустой `html_path`, существующий `result.md`.
- Два параллельных `run` одной задачи разойдутся по одним и тем же статьям — не запускайте их одновременно.

### Каталог DEMO

`make <задача> demo-generate [ID]` собирает каталог `DEMO` внутри каталога статьи из сохранённого состояния и статью по pipeline не двигает. В корне — `result.md` и `fix_links_html_prompt.txt` (объединённый промпт ручного чата из `tasks/common/prompts/demo/fix_links_html.txt`); промпт статьи — `DEMO/prompts/article_prompt.txt`.

### Очистка и сброс

| Команда | Что стирает | Что остаётся |
|---|---|---|
| `clear <id>` | research, metadata, outputs, ошибки статьи и её каталог целиком | строка `articles` и `article_inputs` — можно сразу `run` |
| `reset <id>` | то же плюс `article_inputs` | строка `articles` с прежним `id` — нужен повторный `import` |
| `reset` | все таблицы задачи со счётчиками, `OUTPUT_DIR`, отчёты импорта и `debug/` | ничего |

Все три **необратимы**: печатают отчёт с масштабом удаления и требуют ввести слово `clear` или `reset`; без терминала — `--yes`. Порядок — сначала БД, потом файлы, поэтому сбой чинится повторным запуском. Каждая команда работает только в схеме своей задачи.

## Dry-run

```bash
make <задача> dry-run
```

Точный эквивалент для `task_1`:

```bash
docker compose up -d --wait postgres-dry-run
go test ./...
go vet ./...
APP_ENV=test DRY_RUN_DATABASE_URL='postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable' go run ./cmd/seo-pipeline task-1 run --dry-run
```

Dry-run печатает разрешённую маршрутизацию — режим, доступность провайдеров и провайдера с таймаутом каждой стадии, — а затем прогоняет поток задачи целиком на детерминированных локальных ответах: импорт, все стадии, `result.md` и проверку артефактов. Ни Gemini, ни Keys.so, ни Arsenkin, ни Playwright не вызываются.

Разрешён только при `APP_ENV=local` или `APP_ENV=test` и только для БД, имя которой содержит `test`, `dry_run` или `dry-run`. Перед запуском очищаются dry-run БД и `<OUTPUT_DIR>/dry-run`. Каждая задача идёт в свою схему на `localhost:5433` — её нужно создать заранее; книгу импорта прогон берёт настоящую.

## Статусы и этапы

Статусы `articles`: `pending` (импортирована), `processing` (проходит pipeline), `completed` (`result.md` опубликован), `failed` (операция завершилась ошибкой).

Этапы `current_step`: `arsenkin_collection`, `structure_generation`, `article_generation`, `metadata_generation`, `article_review`, `html_generation`, `final_file_assembly`. При `completed` этап равен `NULL`; при ошибке сохраняются текущий этап и `error_message`. Оба списка закреплены `CHECK`-ограничениями в baseline каждой задачи.

## Логи

| `LOG_FORMAT` | Что делает |
|---|---|
| `auto` (по умолчанию) | в терминале — человекочитаемый `pretty`, в файл или пайп — `text` |
| `pretty` | человекочитаемый формат всегда |
| `text` | `key=value` |
| `json` | structured JSON для Loki, ELK и подобного |

`pretty` обрезает строки по ширине окна (`COLUMNS`), но никогда не обрезает текст ошибки и строки `warn`/`error`; цвет выключается `NO_COLOR`.

**Логи статей** — `<external_id>-<slug>/logs/<операция>.log` — всегда пишутся в `text` или `json` со всеми полями.

## Артефакты и диагностика

Для статьи `external_id=37` и `image_slug=primer`:

```text
tasks/<задача>/output/37-primer/
  prepare/
    input.json              исходные данные статьи
    keysso.json             запросы и их источник: manual, keysso или модель
    arsenkin.json           частотности Wordstat, LSI, структура конкурентов
    prepare-report.json     результат каждой проверки prepare, и при отказе тоже
  logs/<операция>.log
  prompts/                  промпты стадий, в том числе article_prompt.txt
  generated/                structure.txt, article.txt, fixed_article.txt, …
  article.html
  result.md
  DEMO/                     после demo-generate
```

Файлы пишутся во временные и публикуются атомарным rename вместе с сохранением состояния этапа; при ошибке остаётся предыдущая версия. Пути в PostgreSQL относительны к `OUTPUT_DIR`. Диагностика неудачных попыток Keys.so, Arsenkin, DeepSeek и Google — `output/<задача>/debug/`.

## История ошибок обработки

Последняя ошибка статьи — в `articles.error_message`, неизменяемая история — в `article_errors`. `make <задача> errors [ID]` показывает **текущую** ошибку; историю читают запросом:

```sql
SELECT ae.created_at, ae.external_id, a.title, ae.step, ae.operation, ae.retryable, ae.error_message
FROM article_errors ae
JOIN articles a ON a.id = ae.article_id
ORDER BY ae.created_at DESC;
```

Ошибки строк Excel живут в отчётах импорта и в `article_errors` не переносятся.

## PostgreSQL и миграции

Таблицы задачи генерации: `articles`, `article_inputs`, `article_research`, `article_metadata`, `article_outputs`, `article_errors`. Перед каждой командой приложение сверяет схему (`repository.ValidateSchema`) в обе стороны: отсутствующая колонка и лишняя — одинаково ошибка. Миграции автоматически не применяются.

Изоляция задач — `search_path` из профиля: `task_1` → `public`, остальные — в схеме своего имени. Схему каждой задачи описывает свой каталог `migrations/<задача>/`, и применяется он **вместо** чужих, а не вдобавок: смешивать каталоги нельзя. Новую миграцию к живой базе применяют руками:

```bash
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
  -c 'SET search_path TO <задача>' -f - < migrations/<задача>/<файл>.up.sql
```

Раскладка — `migrations/README.md`. Схемы каталогов услуг (`site`, `site_obuchim`) задачам не принадлежат и проверкой схемы не сверяются.

Чистая база с нуля:

```bash
docker compose down -v           # удалить volumes, иначе миграции не применятся повторно
make docker-up
```

## Текущие ограничения

- `task_1` — устаревшая реализация, сохранённая для обратной совместимости; в перспективе будет удалена.
- Задачи кроме `task_1` работают только через DeepSeek Web.
- `article`, `info`, `review` и `fix` у задач кроме `task_1` — имена одного действия: чат 2 прогоняется целиком.
- Отменить публикацию в WordPress приложение не умеет; `republish` перезаписывает живую запись без отката (прежний текст — только в ревизиях WordPress).
- Схема PostgreSQL новой задачи создаётся руками, как и миграции.
- Отдельной CLI-команды только для `structure` нет; автоматического retry для `failed` нет.
- Повторный генерационный запуск снова обращается к LLM и тратит квоту.
- В форму Wordstat уходит не более 49 запросов.

## Проверка проекта без Makefile

```bash
go fmt ./...
go vet ./...
golangci-lint run ./...
go test ./...
go test -race ./...
go build -o bin/seo-pipeline ./cmd/seo-pipeline
```
