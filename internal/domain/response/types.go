package response

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Status string

const (
	StatusPreparing  Status = "preparing"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusBlocked    Status = "blocked"
	StatusCanceled   Status = "canceled"
	StatusFailed     Status = "failed"
)

type Response struct {
	ID               string            `json:"id"`
	ArtifactMeta     artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SessionID        string            `json:"session_id"`
	ExecutionID      string            `json:"execution_id"`
	TraceID          string            `json:"trace_id,omitempty"`
	TriggerEventIDs  []string          `json:"trigger_event_ids,omitempty"`
	TriggerSource    string            `json:"trigger_source,omitempty"`
	TriggerReason    string            `json:"trigger_reason,omitempty"`
	DedupeKey        string            `json:"dedupe_key,omitempty"`
	Status           Status            `json:"status"`
	Reason           string            `json:"reason,omitempty"`
	IterationCount   int               `json:"iteration_count,omitempty"`
	MaxIterations    int               `json:"max_iterations,omitempty"`
	StabilityReached bool              `json:"stability_reached,omitempty"`
	GenerationMode   string            `json:"generation_mode,omitempty"`
	PreambleEventID  string            `json:"preamble_event_id,omitempty"`
	MessageEventIDs  []string          `json:"message_event_ids,omitempty"`
	ToolInsights     []string          `json:"tool_insights,omitempty"`
	GlossaryTerms    []string          `json:"glossary_terms,omitempty"`
	StartedAt        time.Time         `json:"started_at,omitempty"`
	CompletedAt      time.Time         `json:"completed_at,omitempty"`
	CanceledAt       time.Time         `json:"canceled_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type Query struct {
	SessionID   string
	ExecutionID string
	Status      string
	Limit       int
}

type TraceSpan struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ResponseID   string            `json:"response_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	ExecutionID  string            `json:"execution_id,omitempty"`
	TraceID      string            `json:"trace_id,omitempty"`
	ParentID     string            `json:"parent_id,omitempty"`
	Kind         string            `json:"kind"`
	Name         string            `json:"name,omitempty"`
	Iteration    int               `json:"iteration,omitempty"`
	Status       string            `json:"status,omitempty"`
	Fields       map[string]any    `json:"fields,omitempty"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at,omitempty"`
}

type TraceSpanQuery struct {
	ResponseID  string
	SessionID   string
	ExecutionID string
	TraceID     string
}
