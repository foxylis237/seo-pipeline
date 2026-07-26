-- =============================================================================
-- Откат схемы SEO-пайплайна.
-- Таблицы удаляются в обратном порядке зависимостей.
-- =============================================================================

DROP TABLE IF EXISTS article_outputs;
DROP TABLE IF EXISTS article_metadata;
DROP TABLE IF EXISTS article_research;
DROP TABLE IF EXISTS article_inputs;
DROP TABLE IF EXISTS articles;