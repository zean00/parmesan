package audit

import "time"

type Record struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	SessionID   string         `json:"session_id,omitempty"`
	ExecutionID string         `json:"execution_id,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	Message     string         `json:"message"`
	Fields      map[string]any `json:"fields,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}
