-- =============================================================================
-- Отметка о публикации статьи в WordPress.
--
-- wordpress_status — маркер, по которому команда отказывается публиковать статью повторно.
-- Дубль по заголовку или slug не определяется: одинаковых заголовков в блоге хватает, а
-- цена ошибки здесь необратима — удалять записи в WordPress приложению запрещено.
--
-- Состояний три, и 'published' с 'linked' различаются намеренно:
--   not_published — в блоге статьи нет;
--   published     — запись создал наш publisher, полный набор полей уходил одним вызовом;
--   linked        — запись существовала до нас, её нашли и привязали командой mark-published.
-- Слить их в одно значило бы потерять ответ на вопрос «эта запись собрана нами или руками»,
-- а он определяет, чему верить при расхождении с артефактами статьи. От повторной
-- публикации защищают оба одинаково.
--
-- NULL как состояние не используется: у колонки NOT NULL и умолчание 'not_published'.
-- Три состояния вместо двух («не публиковалась», «опубликована», «а тут NULL, кто его
-- поставил») — это лишняя ветка в каждом предикате выборки и в каждом сравнении.
--
-- Колонки стоят на articles, а не на article_outputs, и это не вкусовщина. Строку
-- article_outputs удаляют целиком четыре пути: clear <id>, reset <id>, ResetGenerationState
-- (regenerate) и SaveResearch — то есть штатный повторный prepare после успешного Arsenkin.
-- Отметка, которую стирает обычный prepare, не защищает ни от чего: следующий publish создал
-- бы второй пост. Строка articles переживает все четыре; её сносит только полный reset без
-- ID, то есть TRUNCATE всей задачи, и там это правильно.
--
-- wordpress_post_id и wordpress_url — идентичность записи и её адрес. URL нужен сборке
-- result.md, она печатает его последним разделом; post_id нужен логам и диагностике. Обе
-- остаются NULL у статьи, отмеченной вручную через mark-published: там записи мы не знаем,
-- и придумывать её нечем.
-- =============================================================================

ALTER TABLE articles
    ADD COLUMN IF NOT EXISTS wordpress_status  TEXT NOT NULL DEFAULT 'not_published',
    ADD COLUMN IF NOT EXISTS wordpress_post_id BIGINT,
    ADD COLUMN IF NOT EXISTS wordpress_url     TEXT;

-- Констрейнт в стиле самой таблицы: у articles уже ограничены и status, и current_step.
-- Без него 'published', 'Published' и 'publish' разойдутся молча, а маркер дубля обязан
-- сравниваться точно.
--
-- ADD CONSTRAINT IF NOT EXISTS в PostgreSQL нет, поэтому пара DROP + ADD: миграция обязана
-- переживать повторное применение, её накатывают в две схемы руками.
ALTER TABLE articles
    DROP CONSTRAINT IF EXISTS articles_wordpress_status_check;

ALTER TABLE articles
    ADD CONSTRAINT articles_wordpress_status_check
    CHECK (wordpress_status IN ('not_published', 'published', 'linked'));
