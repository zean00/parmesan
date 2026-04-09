package session

import "time"

type Status string

const (
	StatusActive           Status = "active"
	StatusIdleCandidate    Status = "idle_candidate"
	StatusAwaitingCustomer Status = "awaiting_customer"
	StatusSessionKeep      Status = "session_keep"
	StatusClosed           Status = "closed"
)

type ContentPart struct {
	Type string         `json:"type"`
	Text string         `json:"text,omitempty"`
	URL  string         `json:"url,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}

type Event struct {
	ID          string         `json:"id"`
	SessionID   string         `json:"session_id"`
	Source      string         `json:"source"`
	Kind        string         `json:"kind"`
	Offset      int64          `json:"offset,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	Content     []ContentPart  `json:"content"`
	Data        map[string]any `json:"data,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Deleted     bool           `json:"deleted,omitempty"`
	ExecutionID string         `json:"execution_id,omitempty"`
}

type Session struct {
	ID                    string         `json:"id"`
	Channel               string         `json:"channel"`
	CustomerID            string         `json:"customer_id,omitempty"`
	AgentID               string         `json:"agent_id,omitempty"`
	Mode                  string         `json:"mode,omitempty"`
	Status                Status         `json:"status,omitempty"`
	Title                 string         `json:"title,omitempty"`
	Metadata              map[string]any `json:"metadata,omitempty"`
	Labels                []string       `json:"labels,omitempty"`
	LastActivityAt        time.Time      `json:"last_activity_at,omitempty"`
	IdleCheckedAt         time.Time      `json:"idle_checked_at,omitempty"`
	AwaitingCustomerSince time.Time      `json:"awaiting_customer_since,omitempty"`
	ClosedAt              time.Time      `json:"closed_at,omitempty"`
	CloseReason           string         `json:"close_reason,omitempty"`
	KeepReason            string         `json:"keep_reason,omitempty"`
	FollowupCount         int            `json:"followup_count,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
}

type EventQuery struct {
	SessionID      string
	Source         string
	TraceID        string
	Kinds          []string
	MinOffset      int64
	Limit          int
	ExcludeDeleted bool
}

type WatchStatus string

const (
	WatchStatusActive  WatchStatus = "active"
	WatchStatusStopped WatchStatus = "stopped"
	WatchStatusFailed  WatchStatus = "failed"
)

type Watch struct {
	ID             string         `json:"id"`
	SessionID      string         `json:"session_id"`
	Kind           string         `json:"kind"`
	Status         WatchStatus    `json:"status"`
	ToolID         string         `json:"tool_id,omitempty"`
	Arguments      map[string]any `json:"arguments,omitempty"`
	PollInterval   time.Duration  `json:"poll_interval,omitempty"`
	NextRunAt      time.Time      `json:"next_run_at,omitempty"`
	StopCondition  string         `json:"stop_condition,omitempty"`
	DedupeKey      string         `json:"dedupe_key,omitempty"`
	LastResultHash string         `json:"last_result_hash,omitempty"`
	LastCheckedAt  time.Time      `json:"last_checked_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type WatchQuery struct {
	SessionID string
	Status    string
}
