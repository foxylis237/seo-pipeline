BEGIN;

ALTER TABLE articles
    ADD COLUMN IF NOT EXISTS external_id TEXT;

UPDATE articles
SET external_id = 'legacy-' || id::text
WHERE external_id IS NULL;

ALTER TABLE articles
    ALTER COLUMN external_id SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_articles_external_id
    ON articles(external_id);

COMMIT;
