-- =============================================================================
-- Основная информация о статье и состоянии пайплайна.
-- =============================================================================
CREATE TABLE articles (
    id BIGSERIAL PRIMARY KEY,

    -- Название статьи.
    title TEXT NOT NULL,

    -- Общий статус обработки.
    --
    -- pending    - ожидает обработки
    -- processing - сейчас выполняется
    -- completed  - полностью завершена
    -- failed     - завершилась ошибкой
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (
            status IN (
                'pending',
                'processing',
                'completed',
                'failed'
            )
        ),

    -- Текущий этап обработки.
    current_step TEXT DEFAULT 'arsenkin_collection'
        CHECK (
            current_step IS NULL
            OR current_step IN (
                'arsenkin_collection',
                'arsenkin_cleanup',
                'structure_generation',
                'article_generation',
                'metadata_generation',
                'reading_time_calculation',
                'html_generation',
                'final_file_assembly'
            )
        ),

    -- Последняя ошибка пайплайна.
    error_message TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (
        status <> 'completed'
        OR current_step IS NULL
    )
);


-- =============================================================================
-- Исходные данные статьи из Excel.
-- =============================================================================
CREATE TABLE article_inputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    category TEXT,
    header TEXT,
    image_slug TEXT,
    meta_description TEXT,
    key_word TEXT,
    reference_url TEXT,
    author TEXT,
    links TEXT,
    professions TEXT
);


-- =============================================================================
-- Данные, полученные после анализа конкурентов.
-- =============================================================================
CREATE TABLE article_research (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    competitor_structure TEXT,

    cleaned_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,

    lsi_words JSONB NOT NULL DEFAULT '[]'::jsonb,

    collected_at TIMESTAMPTZ,

    -- Время последнего изменения любой части исследования.
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Метаданные готовой статьи.
-- =============================================================================
CREATE TABLE article_metadata (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Ответ третьего промта:
    -- метки, TL;DR и FAQ.
    metadata_text TEXT,

    -- Рассчитывается средствами Go.
    reading_time_minutes INTEGER
        CHECK (
            reading_time_minutes IS NULL
            OR reading_time_minutes >= 0
        ),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Пути к сформированным файлам.
-- =============================================================================
CREATE TABLE article_outputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    structure_path TEXT,
    article_path TEXT,
    metadata_path TEXT,
    html_path TEXT,
    final_path TEXT,

    word_count INTEGER
        CHECK (
            word_count IS NULL
            OR word_count >= 0
        ),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


CREATE INDEX idx_articles_status
    ON articles(status);

CREATE INDEX idx_articles_current_step
    ON articles(current_step);
