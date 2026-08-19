-- =============================================================================
-- Схема pprof_2 целиком, одним файлом.
--
-- ВАЖНО: применяется ТОЛЬКО к схеме pprof_2 и заменяет собой общий каталог migrations/.
-- Накатывать общие миграции поверх не нужно и нельзя: они заведут колонки, которых у этой
-- задачи быть не должно, и repository.ValidateSchema остановит первую же команду на
-- «unexpected column».
--
--   docker exec -i seo-postgres psql -U seo -d seo \
--     -v ON_ERROR_STOP=1 -c 'SET search_path TO pprof_2' -f - \
--     < migrations/pprof_2/000001_schema.up.sql
--
-- Почему своя baseline, а не общая плюс правки: pprof_2 пишет коммерческие страницы услуг, и
-- набор полей у него другой. Цепочка «общая схема → ADD четыре свои колонки → DROP четыре
-- чужие» описывала бы историю чужих решений, а не эту задачу; читать её пришлось бы задом
-- наперёд. Данные при этом не теряются: строки article_inputs восстанавливает повторный
-- import из книги, всё остальное — прогон.
--
-- Чего здесь нет и почему:
--   article_inputs.author      — автора у страницы услуги нет, преподаватели в своей колонке;
--   article_inputs.links       — перелинковки у pprof_2 нет, промпт html запрещает ссылки;
--   article_inputs.professions — списка похожих профессий result.md не печатает;
--   article_inputs.tags        — меток в блог задача не публикует;
--   article_metadata.tldr      — TL;DR она не генерирует, из разделов info есть только FAQ.
-- Набор необязательных колонок объявлен в профиле (internal/tasks/pprof2), и он обязан
-- совпадать с этим файлом: проверка схемы строгая в обе стороны.
-- =============================================================================


-- =============================================================================
-- Статья и состояние её обработки.
-- =============================================================================
CREATE TABLE IF NOT EXISTS articles (
    id BIGSERIAL PRIMARY KEY,

    -- Идентификатор из колонки "id" входного Excel. Именно его пользователь указывает
    -- в CLI; articles.id наружу не выходит.
    external_id TEXT NOT NULL,

    -- Полное название страницы из колонки article_name. Короткое название услуги живёт
    -- отдельной колонкой article_inputs.service_name: это два разных значения.
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

    -- Свои поля pprof_2.
    --
    -- Колонки author здесь нет: у страницы услуги автора не бывает, а преподаватели приходят
    -- колонкой teachers и живут в своём поле. Держать обе значило бы хранить одно значение
    -- дважды и сверять импорт с колонкой, которую никто не читает.
    seo_title TEXT,     -- «сео-заголовок»: заголовок для выдачи, отдельный от названия
    section TEXT,       -- «раздел»: рубрика верхнего уровня, крупнее категории
    profession TEXT,    -- название профессии, о которой страница
    teachers TEXT,      -- преподаватели программы
    service_name TEXT   -- короткое название услуги, без уточнений полного названия
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
-- Разделы публикации. У pprof_2 это только частые вопросы.
--
-- Стадии info у задачи нет: FAQ вынимается разбором из уже написанной страницы, и в
-- metadata_text ложится он же — второго текста, из которого он разбирался бы, не существует.
-- Колонки tldr здесь нет: TL;DR задача не генерирует, и пустая колонка обещала бы раздел,
-- которого не будет.
-- =============================================================================
CREATE TABLE IF NOT EXISTS article_metadata (
    article_id BIGINT PRIMARY KEY
        REFERENCES articles(id)
        ON DELETE CASCADE,

    metadata_text TEXT,
    faq TEXT,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- =============================================================================
-- Пути к артефактам на диске, относительно OUTPUT_DIR.
--
-- review_path и fixed_article_path у pprof_2 указывают на тот же файл, что article_path:
-- ревью в потоке пока нет, финальный текст — это текст основного промпта. Колонки остаются
-- на месте: они вернутся к своему смыслу вместе со стадией ревью.
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
