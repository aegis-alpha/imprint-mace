CREATE TABLE IF NOT EXISTS eval_runs (
    id TEXT PRIMARY KEY,
    eval_type TEXT NOT NULL,          -- 'extraction' or 'retrieval'
    score REAL NOT NULL,              -- composite (extraction) or recall_at_10 (retrieval)
    score2 REAL,                      -- mrr (retrieval only)
    report TEXT NOT NULL,             -- full JSON report
    prompt_hash TEXT,                 -- SHA-256 of extraction prompt used (extraction only)
    examples_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
