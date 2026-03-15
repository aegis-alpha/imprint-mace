-- Embedding model metadata (D21) + FTS5 for facts (D22).

ALTER TABLE facts ADD COLUMN embedding_model TEXT;

CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
    content,
    fact_id UNINDEXED
);
