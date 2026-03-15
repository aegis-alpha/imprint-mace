-- OpenClaw-Memory initial schema.
-- Matches internal/model/types.go and spec/types.rs.

CREATE TABLE IF NOT EXISTS entities (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    aliases     TEXT NOT NULL DEFAULT '[]',  -- JSON array of strings
    created_at  TEXT NOT NULL                -- RFC 3339
);

CREATE TABLE IF NOT EXISTS facts (
    id             TEXT PRIMARY KEY,
    source_file    TEXT NOT NULL,
    source_lines   TEXT,                     -- "42-58" or NULL
    source_ts      TEXT,                     -- RFC 3339 or NULL
    fact_type      TEXT NOT NULL,
    subject        TEXT,
    content        TEXT NOT NULL,
    confidence     REAL NOT NULL DEFAULT 1.0,
    valid_from     TEXT,                     -- RFC 3339 or NULL
    valid_until    TEXT,                     -- RFC 3339 or NULL
    superseded_by  TEXT REFERENCES facts(id),
    created_at     TEXT NOT NULL,            -- RFC 3339
    embedding      BLOB                      -- float32 array, NULL until embeddings enabled
);

CREATE TABLE IF NOT EXISTS relationships (
    id            TEXT PRIMARY KEY,
    from_entity   TEXT NOT NULL REFERENCES entities(id),
    to_entity     TEXT NOT NULL REFERENCES entities(id),
    relation_type TEXT NOT NULL,
    properties    TEXT NOT NULL DEFAULT '{}', -- JSON object
    source_fact   TEXT REFERENCES facts(id),
    created_at    TEXT NOT NULL               -- RFC 3339
);

CREATE TABLE IF NOT EXISTS consolidations (
    id              TEXT PRIMARY KEY,
    source_fact_ids TEXT NOT NULL DEFAULT '[]', -- JSON array of fact IDs
    summary         TEXT NOT NULL,
    insight         TEXT NOT NULL,
    importance      REAL NOT NULL DEFAULT 0.5,
    created_at      TEXT NOT NULL               -- RFC 3339
);

CREATE TABLE IF NOT EXISTS fact_connections (
    id               TEXT PRIMARY KEY,
    fact_a           TEXT NOT NULL REFERENCES facts(id),
    fact_b           TEXT NOT NULL REFERENCES facts(id),
    connection_type  TEXT NOT NULL,
    strength         REAL NOT NULL DEFAULT 0.5,
    consolidation_id TEXT REFERENCES consolidations(id),
    created_at       TEXT NOT NULL              -- RFC 3339
);

-- Indexes for common queries.

CREATE INDEX IF NOT EXISTS idx_facts_type ON facts(fact_type);
CREATE INDEX IF NOT EXISTS idx_facts_subject ON facts(subject);
CREATE INDEX IF NOT EXISTS idx_facts_created ON facts(created_at);
CREATE INDEX IF NOT EXISTS idx_facts_superseded ON facts(superseded_by);

CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(entity_type);
CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);

CREATE INDEX IF NOT EXISTS idx_relationships_from ON relationships(from_entity);
CREATE INDEX IF NOT EXISTS idx_relationships_to ON relationships(to_entity);
CREATE INDEX IF NOT EXISTS idx_relationships_type ON relationships(relation_type);

CREATE INDEX IF NOT EXISTS idx_fact_connections_a ON fact_connections(fact_a);
CREATE INDEX IF NOT EXISTS idx_fact_connections_b ON fact_connections(fact_b);

-- Migration tracking.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
