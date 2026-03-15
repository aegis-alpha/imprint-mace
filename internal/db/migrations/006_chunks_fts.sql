-- FTS5 index over transcript chunk text for keyword search (BVP-214).
-- chunk_id is UNINDEXED: stored for joining, not searchable.

CREATE VIRTUAL TABLE IF NOT EXISTS transcript_chunks_fts USING fts5(
    content,
    chunk_id UNINDEXED
);
