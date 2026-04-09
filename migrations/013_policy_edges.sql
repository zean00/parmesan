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
