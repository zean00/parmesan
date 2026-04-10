package maintainer

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

const (
	ModeSharedWiki     = "shared_wiki"
	ModeCustomerMemory = "customer_memory"
)

const (
	TriggerBootstrap   = "bootstrap"
	TriggerSourceSync  = "source_sync"
	TriggerFeedback    = "feedback"
	TriggerSessionEnd  = "session_end"
	TriggerMaintenance = "maintenance"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

type Workspace struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ScopeKind    string            `json:"scope_kind"`
	ScopeID      string            `json:"scope_id"`
	Mode         string            `json:"mode"`
	Status       string            `json:"status"`
	Schema       map[string]any    `json:"schema,omitempty"`
	IndexPageID  string            `json:"index_page_id,omitempty"`
	LogPageID    string            `json:"log_page_id,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type Job struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	WorkspaceID  string            `json:"workspace_id,omitempty"`
	ScopeKind    string            `json:"scope_kind"`
	ScopeID      string            `json:"scope_id"`
	AgentID      string            `json:"agent_id,omitempty"`
	CustomerID   string            `json:"customer_id,omitempty"`
	Mode         string            `json:"mode"`
	Trigger      string            `json:"trigger"`
	Status       string            `json:"status"`
	RequestedBy  string            `json:"requested_by,omitempty"`
	SourceID     string            `json:"source_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	FeedbackID   string            `json:"feedback_id,omitempty"`
	ResponseID   string            `json:"response_id,omitempty"`
	RunID        string            `json:"run_id,omitempty"`
	Error        string            `json:"error,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
}

type Run struct {
	ID            string            `json:"id"`
	ArtifactMeta  artifactmeta.Meta `json:"artifact_meta,omitempty"`
	JobID         string            `json:"job_id"`
	WorkspaceID   string            `json:"workspace_id,omitempty"`
	ScopeKind     string            `json:"scope_kind"`
	ScopeID       string            `json:"scope_id"`
	AgentID       string            `json:"agent_id,omitempty"`
	CustomerID    string            `json:"customer_id,omitempty"`
	Mode          string            `json:"mode"`
	Trigger       string            `json:"trigger"`
	Status        string            `json:"status"`
	ResponseID    string            `json:"response_id,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	TraceID       string            `json:"trace_id,omitempty"`
	InputSummary  map[string]any    `json:"input_summary,omitempty"`
	OutputSummary map[string]any    `json:"output_summary,omitempty"`
	Metadata      map[string]any    `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	FinishedAt    *time.Time        `json:"finished_at,omitempty"`
}

type WorkspaceQuery struct {
	ScopeKind string
	ScopeID   string
	Mode      string
	Limit     int
}

type JobQuery struct {
	ScopeKind  string
	ScopeID    string
	Mode       string
	Status     string
	SourceID   string
	SessionID  string
	FeedbackID string
	Limit      int
}

type RunQuery struct {
	JobID       string
	WorkspaceID string
	ScopeKind   string
	ScopeID     string
	Status      string
	Limit       int
}
