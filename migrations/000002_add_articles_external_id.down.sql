BEGIN;

DROP INDEX IF EXISTS idx_articles_external_id;

ALTER TABLE articles
    DROP COLUMN IF EXISTS external_id;

COMMIT;
