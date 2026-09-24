-- =============================================================================
-- Схема obuch_2 целиком, одним файлом.
--
-- ВАЖНО: применяется ТОЛЬКО к схеме obuch_2 и заменяет собой любой другой каталог
-- migrations/. Накатывать чужие миграции поверх не нужно и нельзя: они заведут колонки,
-- которых у этой задачи быть не должно, и repository.ValidateSchema остановит первую же
-- команду на «unexpected column».
--
--   docker exec -i seo-postgres psql -U seo -d seo -c 'CREATE SCHEMA IF NOT EXISTS obuch_2'
--   docker exec -i seo-postgres psql -U seo -d seo \
--     -v ON_ERROR_STOP=1 -c 'SET search_path TO obuch_2' -f - \
--     < migrations/obuch_2/000001_schema.up.sql
--
-- Почему своя baseline, а не чужая плюс правки: obuch_2 пишет коммерческие страницы услуг
-- второй площадки, и набор полей у него свой. Цепочка «чужая схема → ADD девять своих
-- колонок → DROP семь чужих» описывала бы историю чужих решений, а не эту задачу.
--
-- Чего здесь нет и почему:
--   article_inputs.author       — автора у страницы услуги нет;
--   article_inputs.teachers     — связи ACF и типа записи teacher у площадки нет вовсе,
--                                 преподавателей модель придумывает прямо в тексте;
--   article_inputs.links        — перелинковки у страницы услуги нет, промпт запрещает ссылки;
--   article_inputs.professions  — блока связанных курсов под страницей нет;
--   article_inputs.tags         — меток в блог задача не публикует;
--   article_inputs.section      — раздела каталога у этой площадки нет;
--   article_inputs.service_name — короткое имя услуги здесь никто не читает.
-- Набор необязательных колонок объявлен в профиле (internal/tasks/obuch2), и он обязан
-- совпадать с этим файлом: проверка схемы строгая в обе стороны.
-- =============================================================================


-- =============================================================================
-- Страница и состояние её обработки.
-- =============================================================================
CREATE TABLE IF NOT EXISTS articles (
    id BIGSERIAL PRIMARY KEY,

    -- Идентификатор из колонки "id" входного Excel. Именно его пользователь указывает
    -- в CLI; articles.id наружу не выходит.
    external_id TEXT NOT NULL,

    -- Полное название страницы из колонки article_name.
    title TEXT NOT NULL,

    -- pending    - ожидает обработки
    -- processing - сейчас выполняется
    -- completed  - полностью завершена
    -- failed     - завершилась ошибкой
    status TEXT NOT NULL DEFAULT 'pending',

    -- Текущий этап. NULL только у завершённой страницы.
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
    -- их знает репозиторий, а не поток obuch_2.
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
-- Исходные данные страницы из Excel. Записываются при импорте.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_inputs (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    -- Обязательные колонки книги: импортёр отклоняет строку без них. image_slug приходит
    -- колонкой slug, reference_url — адресом конкурента для Keys.so.
    image_slug TEXT NOT NULL,
    reference_url TEXT NOT NULL,

    category TEXT,
    header TEXT,
    meta_description TEXT,
    key_word TEXT,

    -- Свои поля obuch_2.
    seo_title TEXT,   -- «сео-заголовок»: заголовок для выдачи, отдельный от названия
    profession TEXT,  -- название профессии, о которой страница
    post_type TEXT,   -- тип записи площадки; он же выбирает таксономию рубрики cat_<тип>

    -- Числа программы. Приходят из книги, а не от модели: те же значения человек заполняет
    -- в полях записи руками, и второй источник того же числа разошёлся бы с первым молча —
    -- на живых страницах площадки они уже разошлись («150 часов» в тексте против «144 часа»
    -- в плашке темы).
    hours TEXT,        -- объём программы в академических часах
    duration TEXT,     -- срок обучения
    price TEXT,        -- стоимость
    document TEXT,     -- документ по итогам обучения
    attestation TEXT,  -- форма итоговой аттестации

    -- Адрес фотографии на стоке. Пустой означает, что абзаца-ссылки под картинкой в теле
    -- записи не будет вовсе: ссылка на главную страницу стока вместо конкретного кадра —
    -- выдуманная атрибуция, а в опубликованной записи её уже не отличить от настоящей.
    image_source_url TEXT
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
-- Разделы публикации. У obuch_2 эта таблица остаётся пустой.
--
-- Стадии info у задачи нет, и разбором из текста страницы FAQ тоже не вынимается: поля под
-- частые вопросы у площадки не существует — ни репитера, ни отдельных ключей, — и вопросы
-- остаются в теле страницы.
--
-- Колонка tldr здесь есть, хотя в неё никто не пишет, и это цена одного решения, а не
-- недосмотр. Наличие колонки сегодня выводится из признака MetadataFAQOnly
-- (repository.SchemaProfile.WithoutTLDR), а тот же признак поднимает на публикации
-- требование непустого FAQ. Выставить его, чтобы убрать колонку, нельзя: заполнять FAQ
-- нечем, и не опубликовалась бы ни одна страница. Одна всегда-NULL колонка дешевле
-- четвёртого признака метаданных в профиле движка.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_metadata (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    metadata_text TEXT,
    tldr TEXT,
    faq TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Пути к артефактам на диске, относительно OUTPUT_DIR.
--
-- article_path у obuch_2 — черновик основного промпта, fixed_article_path — страница после
-- ревью, и её читают разметка и result.md. review_path указывает на тот же файл, что и
-- fixed_article_path: списка замечаний ревью не отдаёт, отдельного текста у него нет, а
-- пустой слот раннер считает невыполненным этапом.
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

-- ListArticlesWithErrors: боковой запрос за последней ошибкой страницы.
CREATE INDEX IF NOT EXISTS idx_article_errors_article_id_created_at
    ON article_errors (article_id, created_at DESC, id DESC);

-- ListErrors с фильтром по external_id.
CREATE INDEX IF NOT EXISTS idx_article_errors_external_id_created_at
    ON article_errors (external_id, created_at DESC, id DESC);

-- ListErrors без фильтра: ORDER BY created_at DESC, id DESC LIMIT n.
CREATE INDEX IF NOT EXISTS idx_article_errors_created_at
    ON article_errors (created_at DESC, id DESC);
