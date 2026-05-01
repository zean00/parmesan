ALTER TABLE session_watches
ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS session_watches_lease_idx
ON session_watches(status, next_run_at ASC, lease_expires_at ASC);

ALTER TABLE eval_runs
ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS eval_runs_lease_idx
ON eval_runs(status, lease_expires_at ASC, created_at ASC);

ALTER TABLE knowledge_maintainer_jobs
ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS knowledge_maintainer_jobs_lease_idx
ON knowledge_maintainer_jobs(status, lease_expires_at ASC, created_at ASC);

ALTER TABLE knowledge_source_sync_jobs
ADD COLUMN IF NOT EXISTS lease_owner TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS knowledge_source_sync_jobs_lease_idx
ON knowledge_source_sync_jobs(status, lease_expires_at ASC, created_at ASC);
