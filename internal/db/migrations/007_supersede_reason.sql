-- Migration 007: Add supersede_reason column to facts table.
-- Replaces the 'batch-replaced' sentinel in superseded_by with a proper
-- supersede_reason column, restoring FK integrity on superseded_by.

ALTER TABLE facts ADD COLUMN supersede_reason TEXT;

UPDATE facts SET supersede_reason = 'batch-replaced', superseded_by = NULL
  WHERE superseded_by = 'batch-replaced';
