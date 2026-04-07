CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS knowledge_sources (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    uri TEXT NOT NULL,
    checksum TEXT,
    status TEXT NOT NULL,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_sources_scope_idx
ON knowledge_sources(scope_kind, scope_id, created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_pages (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    source_id TEXT,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    page_type TEXT NOT NULL DEFAULT '',
    citations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    checksum TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_pages_scope_idx
ON knowledge_pages(scope_kind, scope_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id TEXT PRIMARY KEY,
    page_id TEXT NOT NULL REFERENCES knowledge_pages(id) ON DELETE CASCADE,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    text TEXT NOT NULL,
    embedding vector,
    embedding_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    citations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_chunks_scope_idx
ON knowledge_chunks(scope_kind, scope_id, created_at DESC);

CREATE INDEX IF NOT EXISTS knowledge_chunks_embedding_idx
ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS knowledge_snapshots (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    page_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    chunk_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_snapshots_scope_idx
ON knowledge_snapshots(scope_kind, scope_id, created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_update_proposals (
    id TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    rationale TEXT NOT NULL DEFAULT '',
    evidence_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS knowledge_update_proposals_scope_idx
ON knowledge_update_proposals(scope_kind, scope_id, created_at DESC);

CREATE TABLE IF NOT EXISTS media_assets (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    event_id TEXT NOT NULL,
    part_index INTEGER NOT NULL,
    type TEXT NOT NULL,
    url TEXT,
    mime_type TEXT,
    checksum TEXT,
    status TEXT NOT NULL,
    retention TEXT,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    enriched_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS media_assets_session_idx
ON media_assets(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS derived_signals (
    id TEXT PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    event_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    extractor TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS derived_signals_session_idx
ON derived_signals(session_id, created_at DESC);
