-- =============================================================================
-- Схема pprof_audit_2 целиком, одним файлом.
--
-- ВАЖНО: применяется ТОЛЬКО к схеме pprof_audit_2.
--
--   docker exec -i seo-postgres psql -U seo -d seo \
--     -v ON_ERROR_STOP=1 -c 'SET search_path TO pprof_audit_2' -f - \
--     < migrations/pprof_audit_2/000001_schema.up.sql
--
-- Почему таблица одна, а не набор из article_inputs, article_outputs и article_metadata,
-- как у задач генерации: pprof_audit_2 не создаёт страницу и не правит её, а проверяет уже
-- опубликованную. У неё нет ни research, ни структуры, ни метаданных, ни промежуточных
-- стадий — есть ссылка на существующую запись и один отчёт о ней.
--
-- Схему проверяет не repository.ValidateSchema (он знает только таблицы движка), а сама
-- задача при первом запросе: колонок мало, и они перечислены в
-- internal/pipeline/articleaudit.
-- =============================================================================

CREATE TABLE IF NOT EXISTS articles (
    id BIGSERIAL PRIMARY KEY,

    -- Индекс из входного файла. Именно его человек указывает в CLI.
    external_id TEXT NOT NULL UNIQUE,

    -- Адрес опубликованной страницы, как он записан во входном файле.
    source_url TEXT NOT NULL,

    -- Слаг из адреса. Хранится отдельно, потому что по нему страница ищется в блоге и им же
    -- называется каталог артефактов: вычислять его заново в двух местах нельзя.
    slug TEXT NOT NULL,

    -- Тема страницы своими словами, если она названа во входном файле. NULL — законное
    -- состояние: тогда основанием проверки служит заголовок записи.
    topic TEXT,

    -- Идентификатор записи в WordPress. Может прийти из входного файла — тогда поиск по
    -- слагу не нужен вовсе: это один запрос вместо перебора тридцати страниц по сотне
    -- записей. NULL до первого успешного поиска.
    post_id BIGINT,

    -- Тип записи, каким его отдала площадка. Нужен человеку: по нему видно, что проверена
    -- страница услуги, а не одноимённая запись блога.
    post_type TEXT,

    -- pending    - импортирована, не проверена
    -- processing - выполняется
    -- completed  - отчёт собран
    -- failed     - остановлена ошибкой, отчёта нет
    status TEXT NOT NULL DEFAULT 'pending',

    -- Текущая блокирующая ошибка. Снимается следующим успешным прогоном.
    error_message TEXT,

    -- Пути артефактов относительно OUTPUT_DIR, как и у остальных задач.
    -- fields_path — поля записи, как они были прочитаны; audit_path — сырой ответ модели до
    -- разбора. Оба хранятся обязательно: по ним видно, что модель на самом деле сказала и
    -- что она вообще видела, когда разбор дал «Не разобрано».
    original_path TEXT,
    fields_path TEXT,
    prompt_path TEXT,
    audit_path TEXT,
    result_path TEXT,

    -- Итоговая оценка отчёта. NULL означает, что оценки в ответе не нашлось: подставлять сюда
    -- ноль нельзя — по этой колонке сортируют пачку, и неразобранный отчёт встал бы впереди
    -- худшей страницы.
    score INTEGER,
    score_max INTEGER,

    -- Счётчики: находок модели списком и незаполненных обязательных полей. Нужны сводке —
    -- по ним видно, за какую страницу браться, не открывая отчёт.
    findings_count INTEGER NOT NULL DEFAULT 0,
    missing_fields_count INTEGER NOT NULL DEFAULT 0,

    -- Момент проверки. Непустое значение — защита от повтора: за ответ модели уже заплачено,
    -- и второй проход платил бы снова за тот же отчёт.
    checked_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT articles_status_check
        CHECK (status IN ('pending', 'processing', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS articles_status_idx ON articles (status);

-- По оценке сортируют пачку: что чинить первым, видно без открытия отчётов.
CREATE INDEX IF NOT EXISTS articles_score_idx ON articles (score);
