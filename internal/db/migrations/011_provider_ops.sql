CREATE TABLE IF NOT EXISTS provider_ops (
    provider_name  TEXT NOT NULL PRIMARY KEY,
    status         TEXT NOT NULL DEFAULT 'ok',
    retry_count    INTEGER NOT NULL DEFAULT 0,
    max_retries    INTEGER NOT NULL DEFAULT 5,
    last_error     TEXT,
    error_type     TEXT,
    next_check_at  TEXT,
    last_success   TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS retry_queue (
    id          TEXT PRIMARY KEY,
    task_type   TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    status      TEXT NOT NULL DEFAULT 'pending'
);
