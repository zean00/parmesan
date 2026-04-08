CREATE TABLE IF NOT EXISTS responses (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    trace_id TEXT,
    trigger_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL,
    reason TEXT,
    iteration_count INTEGER NOT NULL DEFAULT 0,
    max_iterations INTEGER NOT NULL DEFAULT 0,
    stability_reached BOOLEAN NOT NULL DEFAULT FALSE,
    generation_mode TEXT,
    preamble_event_id TEXT,
    message_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    tool_insights JSONB NOT NULL DEFAULT '[]'::jsonb,
    glossary_terms JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    canceled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS response_trace_spans (
    id TEXT PRIMARY KEY,
    response_id TEXT REFERENCES responses(id),
    session_id TEXT REFERENCES sessions(id),
    execution_id TEXT REFERENCES turn_executions(id),
    trace_id TEXT,
    parent_id TEXT,
    kind TEXT NOT NULL,
    name TEXT,
    iteration INTEGER NOT NULL DEFAULT 0,
    status TEXT,
    fields JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
