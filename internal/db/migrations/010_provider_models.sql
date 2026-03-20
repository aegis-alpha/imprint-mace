CREATE TABLE IF NOT EXISTS provider_models (
    provider_name TEXT NOT NULL,
    model_id      TEXT NOT NULL,
    context_window INTEGER,
    available     INTEGER NOT NULL DEFAULT 1,
    last_checked  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (provider_name, model_id)
);

CREATE TABLE IF NOT EXISTS provider_health (
    provider_name    TEXT NOT NULL,
    task_type        TEXT NOT NULL,
    configured_model TEXT NOT NULL,
    active_model     TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'unknown',
    last_error       TEXT,
    last_checked     TEXT NOT NULL DEFAULT (datetime('now')),
    switched_at      TEXT,
    PRIMARY KEY (provider_name, task_type)
);
