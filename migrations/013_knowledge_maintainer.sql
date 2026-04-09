CREATE TABLE IF NOT EXISTS knowledge_workspaces (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    status TEXT NOT NULL,
    schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    index_page_id TEXT NOT NULL DEFAULT '',
    log_page_id TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_workspaces_scope_idx
ON knowledge_workspaces(scope_kind, scope_id, mode, updated_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_maintainer_jobs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL DEFAULT '',
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    customer_id TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    requested_by TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    feedback_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS knowledge_maintainer_jobs_scope_idx
ON knowledge_maintainer_jobs(scope_kind, scope_id, mode, status, created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_maintainer_runs (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT '',
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    customer_id TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    trace_id TEXT NOT NULL DEFAULT '',
    input_summary_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_summary_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS knowledge_maintainer_runs_job_idx
ON knowledge_maintainer_runs(job_id, created_at DESC);
