-- Quality signals and fact citations for self-tuning extraction (BVP-279).
-- Signals are collected by SQL-only queries -- no LLM cost.

CREATE TABLE IF NOT EXISTS quality_signals (
    id          TEXT PRIMARY KEY,
    signal_type TEXT NOT NULL,        -- "supersede_rate", "citation_rate", "volume_anomaly", "entity_collision_rate", "confidence_calibration", "confidence_citation_calibration"
    category    TEXT NOT NULL,        -- fact_type name, or "global" for system-wide signals
    value       REAL NOT NULL,        -- the metric value (rate, ECE, etc.)
    details     TEXT NOT NULL DEFAULT '{}',  -- JSON: extra context (sample_size, threshold, etc.)
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_quality_signals_type ON quality_signals(signal_type);
CREATE INDEX IF NOT EXISTS idx_quality_signals_created ON quality_signals(created_at);

-- Fact citations: which facts were cited in which query responses (D-Q1).
-- One row per citation. Enables co-citation analysis and temporal trends.

CREATE TABLE IF NOT EXISTS fact_citations (
    fact_id  TEXT NOT NULL,
    query_id TEXT NOT NULL,
    cited_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_fact_citations_fact ON fact_citations(fact_id);
CREATE INDEX IF NOT EXISTS idx_fact_citations_query ON fact_citations(query_id);
CREATE INDEX IF NOT EXISTS idx_fact_citations_cited ON fact_citations(cited_at);

-- Entity collision tracking in extraction_log (D-Q5).

ALTER TABLE extraction_log ADD COLUMN entity_collisions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE extraction_log ADD COLUMN entity_creations INTEGER NOT NULL DEFAULT 0;
