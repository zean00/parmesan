ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS customer_id TEXT,
    ADD COLUMN IF NOT EXISTS agent_id TEXT,
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS labels_json JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE session_events
    ADD COLUMN IF NOT EXISTS "offset" BIGINT,
    ADD COLUMN IF NOT EXISTS trace_id TEXT,
    ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS deleted BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE session_events
SET "offset" = EXTRACT(EPOCH FROM created_at) * 1000000000
WHERE "offset" IS NULL;

CREATE INDEX IF NOT EXISTS session_events_session_offset_idx
ON session_events(session_id, "offset" ASC, created_at ASC);

CREATE INDEX IF NOT EXISTS session_events_session_trace_idx
ON session_events(session_id, trace_id, "offset" ASC, created_at ASC);
