CREATE TABLE IF NOT EXISTS customer_preferences (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    evidence_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_confirmed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, customer_id, key)
);

CREATE INDEX IF NOT EXISTS customer_preferences_lookup_idx
ON customer_preferences(agent_id, customer_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS customer_preference_events (
    id TEXT PRIMARY KEY,
    preference_id TEXT,
    agent_id TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    key TEXT,
    value TEXT,
    action TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    evidence_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS customer_preference_events_lookup_idx
ON customer_preference_events(agent_id, customer_id, created_at DESC);

CREATE TABLE IF NOT EXISTS operator_feedback (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    execution_id TEXT,
    trace_id TEXT,
    operator_id TEXT,
    rating INTEGER NOT NULL DEFAULT 0,
    category TEXT,
    text TEXT NOT NULL,
    labels_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    target_event_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    outputs_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS operator_feedback_session_idx
ON operator_feedback(session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS operator_feedback_operator_idx
ON operator_feedback(operator_id, created_at DESC);
