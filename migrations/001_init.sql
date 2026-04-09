CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS policy_bundles (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    source_yaml TEXT NOT NULL,
    bundle_json JSONB NOT NULL,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS policy_artifacts (
    id TEXT PRIMARY KEY,
    bundle_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    version TEXT NOT NULL,
    source_yaml TEXT NOT NULL,
    artifact_json JSONB NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS policy_snapshots (
    id TEXT PRIMARY KEY,
    bundle_id TEXT NOT NULL,
    snapshot_json JSONB NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS policy_edges (
    id TEXT PRIMARY KEY,
    bundle_id TEXT NOT NULL,
    snapshot_id TEXT,
    source_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    target_id TEXT NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_policy_edges_bundle_id ON policy_edges(bundle_id);
CREATE INDEX IF NOT EXISTS idx_policy_edges_snapshot_id ON policy_edges(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_policy_edges_source_id ON policy_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_policy_edges_target_id ON policy_edges(target_id);

CREATE TABLE IF NOT EXISTS policy_proposals (
    id TEXT PRIMARY KEY,
    source_bundle_id TEXT NOT NULL,
    candidate_bundle_id TEXT NOT NULL,
    state TEXT NOT NULL,
    rationale TEXT NOT NULL DEFAULT '',
    evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    replay_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    safety_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    risk_flags JSONB NOT NULL DEFAULT '[]'::jsonb,
    requires_manual_approval BOOLEAN NOT NULL DEFAULT FALSE,
    eval_summary_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS policy_rollouts (
    id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES policy_proposals(id),
    status TEXT NOT NULL,
    channel TEXT NOT NULL,
    percentage INTEGER NOT NULL DEFAULT 0,
    include_session_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    previous_bundle_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    channel TEXT NOT NULL,
    customer_id TEXT,
    agent_id TEXT,
    mode TEXT NOT NULL DEFAULT 'auto',
    status TEXT NOT NULL DEFAULT 'active',
    title TEXT,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    labels_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_activity_at TIMESTAMPTZ,
    idle_checked_at TIMESTAMPTZ,
    awaiting_customer_since TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    close_reason TEXT,
    keep_reason TEXT,
    followup_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS session_watches_session_idx
ON session_watches(session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS session_watches_status_next_run_idx
ON session_watches(status, next_run_at ASC, created_at ASC);

CREATE TABLE IF NOT EXISTS conversation_bindings (
    id TEXT PRIMARY KEY,
    channel TEXT NOT NULL,
    external_conversation_id TEXT NOT NULL,
    external_user_id TEXT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    capability_profile JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS conversation_bindings_channel_external_idx
ON conversation_bindings(channel, external_conversation_id);

CREATE TABLE IF NOT EXISTS session_events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    source TEXT NOT NULL,
    kind TEXT NOT NULL,
    execution_id TEXT,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS turn_executions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    trigger_event_id TEXT NOT NULL,
    trigger_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    policy_bundle_id TEXT,
    proposal_id TEXT,
    rollout_id TEXT,
    selection_reason TEXT,
    status TEXT NOT NULL,
    blocked_reason TEXT,
    resume_signal TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS execution_steps (
    id TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    recomputable BOOLEAN NOT NULL DEFAULT FALSE,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    last_error TEXT,
    next_attempt_at TIMESTAMPTZ,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    max_elapsed_seconds INTEGER NOT NULL DEFAULT 0,
    backoff_seconds INTEGER NOT NULL DEFAULT 1,
    retry_reason TEXT,
    blocked_reason TEXT,
    resume_signal TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE turn_executions
    ADD COLUMN IF NOT EXISTS trace_id TEXT,
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS blocked_reason TEXT,
    ADD COLUMN IF NOT EXISTS resume_signal TEXT,
    ADD COLUMN IF NOT EXISTS trigger_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS tool_provider_bindings (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    uri TEXT NOT NULL,
    healthy BOOLEAN NOT NULL DEFAULT TRUE,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tool_auth_bindings (
    provider_id TEXT PRIMARY KEY REFERENCES tool_provider_bindings(id),
    auth_type TEXT NOT NULL,
    header_name TEXT,
    secret_ciphertext TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tool_catalog (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES tool_provider_bindings(id),
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    schema_json JSONB NOT NULL,
    runtime_protocol TEXT NOT NULL DEFAULT 'mcp',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS journey_instances (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    journey_id TEXT NOT NULL,
    state_id TEXT NOT NULL,
    path TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    session_id TEXT,
    execution_id TEXT,
    trace_id TEXT,
    message TEXT NOT NULL,
    fields JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tool_runs (
    id TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    tool_id TEXT NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    event_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS approval_sessions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    tool_id TEXT NOT NULL,
    status TEXT NOT NULL,
    request_text TEXT NOT NULL,
    decision TEXT,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS eval_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    source_execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    proposal_id TEXT,
    active_bundle_id TEXT,
    shadow_bundle_id TEXT,
    status TEXT NOT NULL,
    result_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    diff_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS responses (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    execution_id TEXT NOT NULL REFERENCES turn_executions(id),
    trace_id TEXT,
    trigger_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    trigger_source TEXT,
    trigger_reason TEXT,
    dedupe_key TEXT,
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
