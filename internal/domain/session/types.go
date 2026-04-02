package session

import "time"

type ContentPart struct {
	Type    string                 `json:"type"`
	Text    string                 `json:"text,omitempty"`
	URL     string                 `json:"url,omitempty"`
	Meta    map[string]any         `json:"meta,omitempty"`
}

type Event struct {
	ID          string        `json:"id"`
	SessionID   string        `json:"session_id"`
	Source      string        `json:"source"`
	Kind        string        `json:"kind"`
	CreatedAt   time.Time     `json:"created_at"`
	Content     []ContentPart `json:"content"`
	ExecutionID string        `json:"execution_id,omitempty"`
}

type Session struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel"`
	CreatedAt time.Time `json:"created_at"`
}
