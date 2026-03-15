-- Taxonomy evolution tables (D17).
-- Signals are collected by consolidation (SQL, no LLM cost).
-- Proposals are created by LLM taxonomy review when signals accumulate.

CREATE TABLE IF NOT EXISTS taxonomy_signals (
    id            TEXT PRIMARY KEY,
    signal_type   TEXT NOT NULL,       -- "custom_frequency", "type_unused", "low_confidence", "type_overlap"
    type_category TEXT NOT NULL,       -- "fact", "entity", "relation", "connection"
    type_name     TEXT NOT NULL,       -- the type this signal is about
    count         INTEGER NOT NULL DEFAULT 1,
    details       TEXT NOT NULL DEFAULT '{}',  -- JSON: extra context
    created_at    TEXT NOT NULL,       -- RFC 3339
    resolved_by   TEXT                 -- proposal ID that addressed this signal, or NULL
);

CREATE TABLE IF NOT EXISTS taxonomy_proposals (
    id             TEXT PRIMARY KEY,
    action         TEXT NOT NULL,     -- "add", "remove", "merge", "rename"
    type_category  TEXT NOT NULL,     -- "fact", "entity", "relation", "connection"
    type_name      TEXT NOT NULL,     -- target type name
    definition     TEXT NOT NULL DEFAULT '{}',  -- JSON: {name, description, example} for adds
    rationale      TEXT NOT NULL,     -- why this change was proposed
    status         TEXT NOT NULL DEFAULT 'proposed',  -- proposed, shadow_testing, validated, applied, rejected
    shadow_results TEXT NOT NULL DEFAULT '{}',  -- JSON: validation metrics
    signal_ids     TEXT NOT NULL DEFAULT '[]',  -- JSON array of signal IDs that triggered this
    created_at     TEXT NOT NULL,     -- RFC 3339
    resolved_at    TEXT               -- when applied or rejected
);

CREATE INDEX IF NOT EXISTS idx_signals_type ON taxonomy_signals(type_category, type_name);
CREATE INDEX IF NOT EXISTS idx_signals_unresolved ON taxonomy_signals(resolved_by) WHERE resolved_by IS NULL;
CREATE INDEX IF NOT EXISTS idx_proposals_status ON taxonomy_proposals(status);

-- Extraction log (D19).
-- Every extraction call is logged for observability and D17 signal collection.

CREATE TABLE IF NOT EXISTS extraction_log (
    id                  TEXT PRIMARY KEY,
    provider_name       TEXT NOT NULL,
    model               TEXT NOT NULL,
    input_length        INTEGER NOT NULL,     -- chars of input text
    tokens_used         INTEGER NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL,
    success             INTEGER NOT NULL,     -- 1 or 0
    facts_count         INTEGER NOT NULL DEFAULT 0,
    entities_count      INTEGER NOT NULL DEFAULT 0,
    relationships_count INTEGER NOT NULL DEFAULT 0,
    error_type          TEXT,                 -- "parse_error", "provider_error", NULL if success
    error_message       TEXT,
    created_at          TEXT NOT NULL          -- RFC 3339
);

CREATE INDEX IF NOT EXISTS idx_extraction_log_created ON extraction_log(created_at);
CREATE INDEX IF NOT EXISTS idx_extraction_log_success ON extraction_log(success);
