ALTER TABLE policy_proposals
ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS knowledge_source_sync_jobs (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES knowledge_sources(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    force BOOLEAN NOT NULL DEFAULT FALSE,
    requested_by TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    old_checksum TEXT NOT NULL DEFAULT '',
    new_checksum TEXT NOT NULL DEFAULT '',
    snapshot_id TEXT NOT NULL DEFAULT '',
    changed BOOLEAN NOT NULL DEFAULT FALSE,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS knowledge_source_sync_jobs_source_idx
ON knowledge_source_sync_jobs(source_id, created_at DESC);

CREATE INDEX IF NOT EXISTS knowledge_source_sync_jobs_status_idx
ON knowledge_source_sync_jobs(status, created_at ASC);
