-- Phase 2 Cool Pipeline foundation: transcript platform_session_id column.

ALTER TABLE transcripts ADD COLUMN platform_session_id TEXT;
CREATE INDEX IF NOT EXISTS idx_transcripts_platform_session ON transcripts(platform_session_id);
