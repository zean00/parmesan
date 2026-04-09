ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS customer_id TEXT,
    ADD COLUMN IF NOT EXISTS agent_id TEXT,
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS labels_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS last_activity_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS idle_checked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS awaiting_customer_since TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS close_reason TEXT,
    ADD COLUMN IF NOT EXISTS keep_reason TEXT,
    ADD COLUMN IF NOT EXISTS followup_count INTEGER NOT NULL DEFAULT 0;

UPDATE sessions
SET last_activity_at = COALESCE(last_activity_at, created_at)
WHERE last_activity_at IS NULL;

CREATE TABLE IF NOT EXISTS session_watches (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    source TEXT,
    subject_ref TEXT,
    tool_id TEXT,
    arguments_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    poll_interval_seconds INTEGER NOT NULL DEFAULT 0,
    next_run_at TIMESTAMPTZ,
    stop_condition TEXT,
    dedupe_key TEXT,
    last_result_hash TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS session_watches_session_idx
ON session_watches(session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS session_watches_status_next_run_idx
ON session_watches(status, next_run_at ASC, created_at ASC);
