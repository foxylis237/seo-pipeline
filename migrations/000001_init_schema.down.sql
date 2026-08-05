-- =============================================================================
-- Откат baseline-схемы SEO-пайплайна.
-- Таблицы удаляются в обратном порядке зависимостей; индексы и constraints
-- уходят вместе с таблицами.
-- =============================================================================

DROP TABLE IF EXISTS article_errors;
DROP TABLE IF EXISTS article_outputs;
DROP TABLE IF EXISTS article_metadata;
DROP TABLE IF EXISTS article_research;
DROP TABLE IF EXISTS article_inputs;
DROP TABLE IF EXISTS articles;
