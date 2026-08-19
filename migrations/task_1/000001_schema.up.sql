-- =============================================================================
-- Схема task_1 целиком, одним файлом. Живёт в схеме public.
--
-- Применяется автоматически при первой инициализации volume — docker/dry-run-init.sh
-- проходит по migrations/task_1/*.up.sql, — и руками для уже существующей базы:
--
--   docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
--     -f - < migrations/task_1/000001_schema.up.sql
--
-- Файл описывает то же состояние, к которому схему привели миграции 000001–000004 (baseline,
-- google_doc_url, перенос меток во входные данные, отметка о публикации). Каждая команда
-- идемпотентна (IF NOT EXISTS), поэтому применение к живой базе ничего не меняет: в public
-- лежат настоящие статьи task_1.
--
-- Набор колонок совпадает с pprof_1: обе задачи пишут статьи блога. Отличий от pprof_2 —
-- четыре: author, links, professions в article_inputs и tldr в article_metadata; своих
-- колонок pprof_2 здесь нет.
--
-- task_1 — старая реализация: команды работают и должны продолжать работать, но новое
-- делается в общем движке и в задачах поверх него.
-- =============================================================================

-- =============================================================================
-- Статья и состояние её обработки.
-- =============================================================================
CREATE TABLE IF NOT EXISTS articles (
    id BIGSERIAL PRIMARY KEY,

    -- Идентификатор из колонки "id" входного Excel. Именно его пользователь указывает
    -- в CLI; articles.id наружу не выходит.
    external_id TEXT NOT NULL,

    -- Название статьи из колонки article_name книги импорта.
    title TEXT NOT NULL,

    -- pending    - ожидает обработки
    -- processing - сейчас выполняется
    -- completed  - полностью завершена
    -- failed     - завершилась ошибкой
    status TEXT NOT NULL DEFAULT 'pending',

    -- Текущий этап. NULL только у завершённой статьи.
    current_step TEXT DEFAULT 'arsenkin_collection',

    -- Текущая блокирующая ошибка. История ошибок — в article_errors.
    error_message TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Отметка о публикации живёт здесь, а не в article_outputs: ту строку сносят clear,
    -- reset <id>, regenerate и штатный повторный prepare, и защита от дубля в блоге исчезла
    -- бы вместе с ней. Состояний три: записи нет, запись собрал наш publisher, запись
    -- существовала до нас и привязана командой mark-published.
    wordpress_status TEXT NOT NULL DEFAULT 'not_published',
    wordpress_post_id BIGINT,
    wordpress_url TEXT,

    CONSTRAINT articles_external_id_key
        UNIQUE (external_id),

    CONSTRAINT articles_status_check
        CHECK (
            status IN ('pending', 'processing', 'completed', 'failed')
        ),

    -- Список этапов, которые пайплайн действительно записывает. Имена общие у всех задач:
    -- их знает репозиторий, а не поток pprof_2.
    CONSTRAINT articles_current_step_check
        CHECK (
            current_step IS NULL
            OR current_step IN (
                'arsenkin_collection',
                'structure_generation',
                'article_generation',
                'metadata_generation',
                'article_review',
                'html_generation',
                'final_file_assembly'
            )
        ),

    CONSTRAINT articles_completed_has_no_step_check
        CHECK (
            status <> 'completed'
            OR current_step IS NULL
        ),

    CONSTRAINT articles_wordpress_status_check
        CHECK (wordpress_status IN ('not_published', 'published', 'linked'))
);


-- =============================================================================
-- Исходные данные статьи из Excel. Записываются при импорте.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_inputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Обязательные колонки книги: импортёр отклоняет строку без них.
    image_slug TEXT NOT NULL,
    reference_url TEXT NOT NULL,

    category TEXT,
    header TEXT,
    meta_description TEXT,
    key_word TEXT,

    -- Поля задач, которые пишут статьи блога.
    author TEXT,        -- автор статьи, раздел result.md
    links TEXT,         -- адреса для перелинковки на стадии html
    professions TEXT,   -- список похожих профессий, раздел result.md
    tags TEXT           -- метки записи в блоге
);


-- =============================================================================
-- Результат разведки конкурентов: Keys.so + Arsenkin.
-- Заполняется целиком одной транзакцией на этапе prepare.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_research (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Структура конкурентов от Arsenkin. Её наличие — признак того, что prepare завершён.
    competitor_structure TEXT,

    -- Очищенные запросы Keys.so: вход Arsenkin, хранится как провенанс.
    cleaned_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- [{query, frequency}] из Wordstat.
    wordstat_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- LSI-слова Arsenkin Copywriters.
    lsi_words JSONB NOT NULL DEFAULT '[]'::jsonb,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Информация для публикации, разобранная из ответа стадии info.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_metadata (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Сырой ответ модели целиком: из него достаётся остаточный текст, не попавший ни в одну
    -- из разобранных секций.
    metadata_text TEXT,

    -- Разобранные секции ответа.
    tldr TEXT,
    faq TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Пути к артефактам на диске, относительно OUTPUT_DIR.
-- Имена колонок соответствуют именам файлов, которые пишет output.Writer.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_outputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    structure_path TEXT,
    article_path TEXT,
    review_path TEXT,
    fixed_article_path TEXT,
    html_path TEXT,

    -- Адрес документа Google с промптом страницы. NULL означает «промпт ещё не публиковался».
    google_doc_url TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Неизменяемая история сбоев обработки.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_errors (
    id BIGSERIAL PRIMARY KEY,

    article_id BIGINT NOT NULL
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Дублируется из articles намеренно: история читается по внешнему идентификатору,
    -- который пользователь вводит в CLI.
    external_id TEXT NOT NULL,

    -- Этап, на котором произошёл сбой, и классифицированная операция.
    step TEXT,
    operation TEXT,

    error_message TEXT NOT NULL,
    retryable BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Индексы. Каждый соответствует конкретному запросу репозитория.
-- =============================================================================

-- ClaimNextIncomplete: WHERE status = 'pending' ORDER BY id ASC.
CREATE INDEX IF NOT EXISTS idx_articles_status_id
    ON articles (status, id);

-- GetPendingForOperation: WHERE current_step IN (...).
CREATE INDEX IF NOT EXISTS idx_articles_current_step
    ON articles (current_step)
    WHERE current_step IS NOT NULL;

-- ListArticlesWithErrors: боковой запрос за последней ошибкой статьи.
CREATE INDEX IF NOT EXISTS idx_article_errors_article_id_created_at
    ON article_errors (article_id, created_at DESC, id DESC);

-- ListErrors с фильтром по external_id.
CREATE INDEX IF NOT EXISTS idx_article_errors_external_id_created_at
    ON article_errors (external_id, created_at DESC, id DESC);

-- ListErrors без фильтра: ORDER BY created_at DESC, id DESC LIMIT n.
CREATE INDEX IF NOT EXISTS idx_article_errors_created_at
    ON article_errors (created_at DESC, id DESC);
