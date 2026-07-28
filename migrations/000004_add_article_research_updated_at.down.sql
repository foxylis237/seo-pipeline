BEGIN;

ALTER TABLE article_research
    DROP COLUMN IF EXISTS updated_at;

COMMIT;
