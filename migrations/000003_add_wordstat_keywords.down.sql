BEGIN;

ALTER TABLE article_research
    DROP COLUMN IF EXISTS wordstat_keywords;

COMMIT;
