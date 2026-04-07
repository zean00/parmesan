package session

import "time"

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
	ID         string         `json:"id"`
	Channel    string         `json:"channel"`
	CustomerID string         `json:"customer_id,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	Title      string         `json:"title,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
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
