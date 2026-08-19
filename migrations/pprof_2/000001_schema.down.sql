-- =============================================================================
-- Снос схемы pprof_2 целиком. Применяется только к схеме pprof_2.
--
-- Необратимо: вместе с таблицами уходят и статьи, и их состояние. Входные данные
-- восстанавливает повторный import из книги, всё остальное — новый прогон.
--
-- Порядок обратный созданию: дочерние таблицы раньше articles, иначе внешние ключи не дадут.
-- =============================================================================

DROP TABLE IF EXISTS article_errors;
DROP TABLE IF EXISTS article_outputs;
DROP TABLE IF EXISTS article_metadata;
DROP TABLE IF EXISTS article_research;
DROP TABLE IF EXISTS article_inputs;
DROP TABLE IF EXISTS articles;
