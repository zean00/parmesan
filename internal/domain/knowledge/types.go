package knowledge

import "time"

type Scope struct {
	Kind string `json:"scope_kind"`
	ID   string `json:"scope_id"`
}

type Source struct {
	ID        string         `json:"id"`
	ScopeKind string         `json:"scope_kind"`
	ScopeID   string         `json:"scope_id"`
	Kind      string         `json:"kind"`
	URI       string         `json:"uri"`
	Checksum  string         `json:"checksum,omitempty"`
	Status    string         `json:"status"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Citation struct {
	SourceID string `json:"source_id,omitempty"`
	URI      string `json:"uri,omitempty"`
	Title    string `json:"title,omitempty"`
	Anchor   string `json:"anchor,omitempty"`
}

type Page struct {
	ID        string         `json:"id"`
	ScopeKind string         `json:"scope_kind"`
	ScopeID   string         `json:"scope_id"`
	SourceID  string         `json:"source_id,omitempty"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	PageType  string         `json:"page_type,omitempty"`
	Citations []Citation     `json:"citations,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Checksum  string         `json:"checksum,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Chunk struct {
	ID        string         `json:"id"`
	PageID    string         `json:"page_id"`
	ScopeKind string         `json:"scope_kind"`
	ScopeID   string         `json:"scope_id"`
	Text      string         `json:"text"`
	Vector    []float32      `json:"vector,omitempty"`
	Citations []Citation     `json:"citations,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Snapshot struct {
	ID        string         `json:"id"`
	ScopeKind string         `json:"scope_kind"`
	ScopeID   string         `json:"scope_id"`
	PageIDs   []string       `json:"page_ids"`
	ChunkIDs  []string       `json:"chunk_ids"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type UpdateProposal struct {
	ID        string         `json:"id"`
	ScopeKind string         `json:"scope_kind"`
	ScopeID   string         `json:"scope_id"`
	Kind      string         `json:"kind"`
	State     string         `json:"state"`
	Rationale string         `json:"rationale,omitempty"`
	Evidence  []Citation     `json:"evidence,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type LintFinding struct {
	ID         string         `json:"id"`
	ScopeKind  string         `json:"scope_kind"`
	ScopeID    string         `json:"scope_id"`
	ProposalID string         `json:"proposal_id,omitempty"`
	PageID     string         `json:"page_id,omitempty"`
	SourceID   string         `json:"source_id,omitempty"`
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Evidence   []Citation     `json:"evidence,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type SyncJob struct {
	ID         string         `json:"id"`
	SourceID   string         `json:"source_id"`
	Status     string         `json:"status"`
	Force      bool           `json:"force,omitempty"`
	RequestedBy string        `json:"requested_by,omitempty"`
	Error      string         `json:"error,omitempty"`
	OldChecksum string        `json:"old_checksum,omitempty"`
	NewChecksum string        `json:"new_checksum,omitempty"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Changed    bool           `json:"changed,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
}

type SnapshotQuery struct {
	ScopeKind string
	ScopeID   string
	Limit     int
}

type PageQuery struct {
	ScopeKind  string
	ScopeID    string
	SnapshotID string
	Limit      int
}

type ChunkQuery struct {
	ScopeKind  string
	ScopeID    string
	SnapshotID string
	Limit      int
}

type ChunkSearchQuery struct {
	ScopeKind  string
	ScopeID    string
	SnapshotID string
	Vector     []float32
	Limit      int
}

type LintQuery struct {
	ScopeKind  string
	ScopeID    string
	ProposalID string
	PageID     string
	Kind       string
	Severity   string
	Status     string
	Limit      int
}

type SyncJobQuery struct {
	SourceID string
	Status   string
	Limit    int
}
