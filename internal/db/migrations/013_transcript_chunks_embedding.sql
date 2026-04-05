-- BVP-365: store chunk embeddings in SQLite BLOB (source of truth for USearch rebuild).
-- vec0 table chunks_vec remains until a later migration drops it.

ALTER TABLE transcript_chunks ADD COLUMN embedding BLOB;
