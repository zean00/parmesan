CREATE TABLE IF NOT EXISTS customer_memory_items (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    category TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    sensitivity TEXT NOT NULL DEFAULT 'low',
    prompt_safe BOOLEAN NOT NULL DEFAULT false,
    evidence_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    observed_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    last_confirmed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, customer_id, category, key)
);

CREATE INDEX IF NOT EXISTS customer_memory_items_lookup_idx
ON customer_memory_items(agent_id, customer_id, category, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS customer_memory_items_prompt_idx
ON customer_memory_items(agent_id, customer_id, prompt_safe, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS customer_memory_events (
    id TEXT PRIMARY KEY,
    memory_id TEXT,
    agent_id TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    category TEXT,
    key TEXT,
    value TEXT,
    action TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    evidence_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS customer_memory_events_lookup_idx
ON customer_memory_events(agent_id, customer_id, category, key, created_at DESC);
