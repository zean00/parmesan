ALTER TABLE operator_feedback
    ADD COLUMN IF NOT EXISTS response_id TEXT,
    ADD COLUMN IF NOT EXISTS score INTEGER,
    ADD COLUMN IF NOT EXISTS comment TEXT,
    ADD COLUMN IF NOT EXISTS correction TEXT;

CREATE INDEX IF NOT EXISTS operator_feedback_response_idx
ON operator_feedback(response_id, created_at DESC);

ALTER TABLE knowledge_maintainer_jobs
    ADD COLUMN IF NOT EXISTS response_id TEXT;

ALTER TABLE knowledge_maintainer_runs
    ADD COLUMN IF NOT EXISTS response_id TEXT;
