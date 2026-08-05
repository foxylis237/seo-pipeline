-- =============================================================================
-- Baseline-схема SEO-пайплайна (task_1).
--
-- Описывает только состояние, которое реально использует код: каждая колонка
-- либо читается, либо пишется репозиторием. Историю миграций 000001..000008
-- эта схема заменяет целиком.
--
-- Порядок таблиц: articles, затем по одной таблице на фазу пайплайна.
-- Каждая дочерняя таблица связана с articles через ON DELETE CASCADE —
-- на это опирается repository.ValidateSchema.
-- =============================================================================


-- =============================================================================
-- Статья и состояние её обработки.
-- =============================================================================
CREATE TABLE articles (
    id BIGSERIAL PRIMARY KEY,

    -- Идентификатор из колонки "id" входного Excel. Именно его пользователь
    -- указывает в CLI; articles.id наружу не выходит.
    external_id TEXT NOT NULL,

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

    CONSTRAINT articles_external_id_key
        UNIQUE (external_id),

    CONSTRAINT articles_status_check
        CHECK (
            status IN ('pending', 'processing', 'completed', 'failed')
        ),

    -- Список этапов, которые пайплайн действительно записывает.
    -- Добавление нового этапа требует новой миграции и правки schema.go.
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
        )
);


-- =============================================================================
-- Исходные данные статьи из Excel. Записываются один раз при импорте.
-- =============================================================================
CREATE TABLE article_inputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Обязательные колонки Excel: импортёр отклоняет строку без них.
    image_slug TEXT NOT NULL,
    reference_url TEXT NOT NULL,

    category TEXT,
    header TEXT,
    meta_description TEXT,
    key_word TEXT,
    author TEXT,
    links TEXT,
    professions TEXT
);


-- =============================================================================
-- Результат разведки конкурентов: Keys.so + Arsenkin.
-- Заполняется целиком одной транзакцией на этапе prepare.
-- =============================================================================
CREATE TABLE article_research (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Структура конкурентов от Arsenkin. Её наличие — признак того,
    -- что prepare для статьи завершён.
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
-- Информация для публикации, разобранная из ответа LLM.
-- =============================================================================
CREATE TABLE article_metadata (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Сырой ответ модели целиком: из него достаётся остаточный текст,
    -- не попавший ни в одну из разобранных секций.
    metadata_text TEXT,

    -- Разобранные секции ответа.
    tags TEXT,
    tldr TEXT,
    faq TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Пути к артефактам на диске, относительно OUTPUT_DIR.
-- Имена колонок соответствуют именам файлов, которые пишет output.Writer.
-- =============================================================================
CREATE TABLE article_outputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    structure_path TEXT,
    article_path TEXT,
    review_path TEXT,
    fixed_article_path TEXT,
    html_path TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Неизменяемая история сбоев обработки.
-- =============================================================================
CREATE TABLE article_errors (
    id BIGSERIAL PRIMARY KEY,

    article_id BIGINT NOT NULL
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Дублируется из articles намеренно: история читается по внешнему
    -- идентификатору, который пользователь вводит в CLI.
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
CREATE INDEX idx_articles_status_id
    ON articles (status, id);

-- GetPendingForOperation: WHERE current_step IN (...).
CREATE INDEX idx_articles_current_step
    ON articles (current_step)
    WHERE current_step IS NOT NULL;

-- ListArticlesWithErrors: боковой запрос за последней ошибкой статьи.
CREATE INDEX idx_article_errors_article_id_created_at
    ON article_errors (article_id, created_at DESC, id DESC);

-- ListErrors с фильтром по external_id.
CREATE INDEX idx_article_errors_external_id_created_at
    ON article_errors (external_id, created_at DESC, id DESC);

-- ListErrors без фильтра: ORDER BY created_at DESC, id DESC LIMIT n.
CREATE INDEX idx_article_errors_created_at
    ON article_errors (created_at DESC, id DESC);
