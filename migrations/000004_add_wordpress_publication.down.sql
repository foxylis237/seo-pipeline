-- =============================================================================
-- Откат отметки о публикации в WordPress.
--
-- Данные колонок теряются, а сами записи в блоге остаются на месте: удалять их приложение
-- не умеет и не будет. Значит после отката отметки придётся проставить заново командой
-- mark-published — иначе первая же публикация создаст дубли тех статей, что уже вышли.
-- =============================================================================

ALTER TABLE articles
    DROP CONSTRAINT IF EXISTS articles_wordpress_status_check;

ALTER TABLE articles
    DROP COLUMN IF EXISTS wordpress_status,
    DROP COLUMN IF EXISTS wordpress_post_id,
    DROP COLUMN IF EXISTS wordpress_url;
