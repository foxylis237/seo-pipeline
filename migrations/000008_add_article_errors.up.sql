CREATE TABLE article_errors (
    id BIGSERIAL PRIMARY KEY,
    article_id BIGINT NOT NULL
        REFERENCES articles(id)
        ON DELETE CASCADE,
    external_id TEXT NOT NULL,
    step TEXT,
    operation TEXT,
    error_message TEXT NOT NULL,
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_article_errors_article_id_created_at
    ON article_errors(article_id, created_at DESC);

CREATE INDEX idx_article_errors_external_id_created_at
    ON article_errors(external_id, created_at DESC);

CREATE INDEX idx_article_errors_created_at
    ON article_errors(created_at DESC);
