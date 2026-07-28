BEGIN;

ALTER TABLE article_research
    ADD COLUMN IF NOT EXISTS wordstat_keywords JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMIT;
