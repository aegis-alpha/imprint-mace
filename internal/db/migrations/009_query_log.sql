CREATE TABLE IF NOT EXISTS query_log (
    id TEXT PRIMARY KEY,
    endpoint TEXT NOT NULL,
    question TEXT,
    total_latency_ms INTEGER,
    retrieval_latency_ms INTEGER,
    synthesis_latency_ms INTEGER,
    facts_found INTEGER,
    facts_by_vector INTEGER,
    facts_by_text INTEGER,
    facts_by_graph INTEGER,
    chunks_by_vector INTEGER,
    chunks_by_text INTEGER,
    citations_count INTEGER,
    embedder_available INTEGER,
    error TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
