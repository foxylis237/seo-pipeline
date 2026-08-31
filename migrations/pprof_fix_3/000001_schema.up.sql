-- =============================================================================
-- Схема pprof_fix_3 целиком, одним файлом.
--
-- ВАЖНО: применяется ТОЛЬКО к схеме pprof_fix_3.
--
--   docker exec -i seo-postgres psql -U seo -d seo \
--     -v ON_ERROR_STOP=1 -c 'SET search_path TO pprof_fix_3' -f - \
--     < migrations/pprof_fix_3/000001_schema.up.sql
--
-- Почему таблица одна, а не набор из article_inputs, article_outputs и article_metadata,
-- как у остальных задач: pprof_fix_3 не создаёт статью, а правит уже опубликованную. У неё
-- нет ни research, ни структуры, ни метаданных, ни промежуточных стадий — есть ссылка на
-- существующую запись и два состояния текста, до и после. Разносить это по пяти таблицам
-- значило бы описывать чужой процесс.
--
-- Схему проверяет не repository.ValidateSchema (он знает только таблицы движка), а сама
-- задача при первом запросе: колонок мало, и они перечислены в internal/pipeline/articlefix.
-- =============================================================================

CREATE TABLE IF NOT EXISTS articles (
    id BIGSERIAL PRIMARY KEY,

    -- Индекс из входного файла. Именно его человек указывает в CLI.
    external_id TEXT NOT NULL UNIQUE,

    -- Адрес опубликованной статьи, как он записан во входном файле.
    source_url TEXT NOT NULL,

    -- Слаг из адреса. Хранится отдельно, потому что по нему статья ищется в блоге и им же
    -- называется каталог артефактов: вычислять его заново в двух местах нельзя.
    slug TEXT NOT NULL,

    -- Идентификатор записи в WordPress. NULL до первого успешного поиска по слагу.
    post_id BIGINT,

    -- Заголовки. Колонки те же, что у остальных задач правки, — таблица у них общая по
    -- форме, — но у pprof_fix_3 значения совпадают: заголовок она не меняет, и new_title
    -- равен old_title. Так видно, что заголовок прочитан, а не потерян.
    old_title TEXT,
    new_title TEXT,

    -- pending    - импортирована, не обработана
    -- processing - выполняется
    -- completed  - текст и заголовок записаны в блог
    -- failed     - остановлена ошибкой, в блог ничего не ушло
    status TEXT NOT NULL DEFAULT 'pending',

    -- Текущая блокирующая ошибка. Снимается следующим успешным прогоном.
    error_message TEXT,

    -- Пути артефактов относительно OUTPUT_DIR, как и у остальных задач.
    original_path TEXT,
    prompt_path TEXT,
    rewritten_path TEXT,
    result_path TEXT,

    -- Момент записи в блог. Непустое значение — защита от повторной правки: прогон уже
    -- переписал статью, и второй проход отправил бы модели собственный вывод.
    updated_post_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT articles_status_check
        CHECK (status IN ('pending', 'processing', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS articles_status_idx ON articles (status);
