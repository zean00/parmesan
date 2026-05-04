CREATE TABLE IF NOT EXISTS usage_quota_policies (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT,
    metric TEXT NOT NULL,
    window TEXT NOT NULL,
    limit_value BIGINT NOT NULL,
    enforcement TEXT NOT NULL,
    status TEXT NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS usage_quota_policies_lookup_idx
ON usage_quota_policies(scope_kind, scope_id, metric, status);

CREATE TABLE IF NOT EXISTS usage_buckets (
    policy_id TEXT NOT NULL REFERENCES usage_quota_policies(id),
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    metric TEXT NOT NULL,
    window TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    window_end TIMESTAMPTZ NOT NULL,
    quantity BIGINT NOT NULL DEFAULT 0,
    limit_value BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (policy_id, scope_id, window_start)
);

CREATE INDEX IF NOT EXISTS usage_buckets_summary_idx
ON usage_buckets(scope_kind, scope_id, metric, window, window_start DESC);

CREATE TABLE IF NOT EXISTS usage_events (
    id TEXT PRIMARY KEY,
    policy_id TEXT,
    decision TEXT,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    metric TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    window TEXT,
    window_start TIMESTAMPTZ,
    window_end TIMESTAMPTZ,
    used_before BIGINT NOT NULL DEFAULT 0,
    used_after BIGINT NOT NULL DEFAULT 0,
    limit_value BIGINT NOT NULL DEFAULT 0,
    resource TEXT,
    provider TEXT,
    model TEXT,
    tool_id TEXT,
    session_id TEXT,
    execution_id TEXT,
    response_id TEXT,
    trace_id TEXT,
    estimated BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT,
    error TEXT,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS usage_events_scope_metric_idx
ON usage_events(scope_kind, scope_id, metric, occurred_at DESC);

CREATE INDEX IF NOT EXISTS usage_events_session_idx
ON usage_events(session_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS usage_events_execution_idx
ON usage_events(execution_id, occurred_at DESC);
