-- BVP-352: Hot-Cool Phase 1 tables, FTS5, embeddings, query_log columns (HOT-PHASE-SPEC).

CREATE TABLE IF NOT EXISTS hot_messages (
    id              TEXT PRIMARY KEY,
    speaker         TEXT NOT NULL,
    content         TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    platform        TEXT,
    platform_session_id TEXT,
    linker_ref      TEXT,
    has_embedding   INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_hot_timestamp ON hot_messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_hot_platform_session ON hot_messages(platform_session_id);

CREATE TABLE IF NOT EXISTS cooldown_messages (
    id              TEXT PRIMARY KEY,
    speaker         TEXT NOT NULL,
    content         TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    platform        TEXT,
    platform_session_id TEXT,
    linker_ref      TEXT,
    has_embedding   INTEGER NOT NULL DEFAULT 0,
    cluster_id      TEXT,
    transcript_file TEXT,
    transcript_line INTEGER,
    processed_at    TEXT,
    moved_from_hot  TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cooldown_cluster ON cooldown_messages(cluster_id);
CREATE INDEX IF NOT EXISTS idx_cooldown_processed ON cooldown_messages(processed_at);
CREATE INDEX IF NOT EXISTS idx_cooldown_timestamp ON cooldown_messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_cooldown_platform_session ON cooldown_messages(platform_session_id);

CREATE VIRTUAL TABLE IF NOT EXISTS hot_messages_fts USING fts5(
    content,
    message_id UNINDEXED
);

CREATE VIRTUAL TABLE IF NOT EXISTS cooldown_messages_fts USING fts5(
    content,
    message_id UNINDEXED
);

ALTER TABLE hot_messages ADD COLUMN embedding BLOB;
ALTER TABLE cooldown_messages ADD COLUMN embedding BLOB;

ALTER TABLE query_log ADD COLUMN hot_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN hot_by_text INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_text INTEGER NOT NULL DEFAULT 0;
