CREATE INDEX IF NOT EXISTS customer_preferences_key_source_idx
ON customer_preferences(agent_id, customer_id, key, source, updated_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_lint_findings (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    proposal_id TEXT,
    page_id TEXT,
    source_id TEXT,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_lint_findings_scope_idx
ON knowledge_lint_findings(scope_kind, scope_id, status, severity, created_at DESC);

CREATE INDEX IF NOT EXISTS knowledge_lint_findings_proposal_idx
ON knowledge_lint_findings(proposal_id, status, severity, created_at DESC);
