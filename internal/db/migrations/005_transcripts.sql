-- Transcript metadata and chunk tracking (D22).
-- Text stays on disk; DB stores metadata and chunk coordinates only.

CREATE TABLE IF NOT EXISTS transcripts (
    id           TEXT PRIMARY KEY,
    file_path    TEXT NOT NULL UNIQUE,
    date         TEXT,
    participants TEXT NOT NULL DEFAULT '[]',
    topic        TEXT,
    chunk_count  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS transcript_chunks (
    id              TEXT PRIMARY KEY,
    transcript_id   TEXT NOT NULL REFERENCES transcripts(id),
    line_start      INTEGER NOT NULL,
    line_end        INTEGER NOT NULL,
    content_hash    TEXT NOT NULL,
    embedding_model TEXT
);

CREATE INDEX IF NOT EXISTS idx_transcripts_path ON transcripts(file_path);
CREATE INDEX IF NOT EXISTS idx_chunks_transcript ON transcript_chunks(transcript_id);
CREATE INDEX IF NOT EXISTS idx_chunks_hash ON transcript_chunks(content_hash);
