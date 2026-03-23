ALTER TABLE eval_runs ADD COLUMN is_baseline INTEGER NOT NULL DEFAULT 0;
ALTER TABLE eval_runs ADD COLUMN git_commit TEXT;
CREATE INDEX IF NOT EXISTS idx_eval_runs_baseline ON eval_runs (eval_type, is_baseline);
