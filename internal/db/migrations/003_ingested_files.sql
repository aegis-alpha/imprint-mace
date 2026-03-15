-- Track which files have been ingested and their content hash.
-- Used by batch adapter to skip already-processed files and detect changes.

CREATE TABLE IF NOT EXISTS ingested_files (
    path         TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,       -- SHA-256 hex
    chunks       INTEGER NOT NULL DEFAULT 1,
    facts_count  INTEGER NOT NULL DEFAULT 0,
    processed_at TEXT NOT NULL        -- RFC 3339
);

CREATE INDEX IF NOT EXISTS idx_ingested_files_hash ON ingested_files(content_hash);
