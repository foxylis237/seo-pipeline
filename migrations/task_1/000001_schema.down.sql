-- =============================================================================
-- Снос схемы task_1 целиком. Применяется к схеме public.
--
-- Необратимо: вместе с таблицами уходят и статьи, и их состояние. В public лежат настоящие
-- статьи task_1 — запускать этот файл на рабочей базе нельзя.
--
-- Порядок обратный созданию: дочерние таблицы раньше articles, иначе внешние ключи не дадут.
-- =============================================================================

DROP TABLE IF EXISTS article_errors;
DROP TABLE IF EXISTS article_outputs;
DROP TABLE IF EXISTS article_metadata;
DROP TABLE IF EXISTS article_research;
DROP TABLE IF EXISTS article_inputs;
DROP TABLE IF EXISTS articles;
