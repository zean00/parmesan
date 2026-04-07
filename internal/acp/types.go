package acp

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
)

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

type Event struct {
	ID          string                `json:"id"`
	SessionID   string                `json:"session_id"`
	Source      string                `json:"source"`
	Kind        string                `json:"kind"`
	Offset      int64                 `json:"offset,omitempty"`
	TraceID     string                `json:"trace_id,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
	Content     []session.ContentPart `json:"content,omitempty"`
	Data        map[string]any        `json:"data,omitempty"`
	Metadata    map[string]any        `json:"metadata,omitempty"`
	Deleted     bool                  `json:"deleted,omitempty"`
	ExecutionID string                `json:"execution_id,omitempty"`
}

func SessionFromDomain(src session.Session) Session {
	return Session{
		ID:         src.ID,
		Channel:    src.Channel,
		CustomerID: src.CustomerID,
		AgentID:    src.AgentID,
		Mode:       src.Mode,
		Title:      src.Title,
		Metadata:   src.Metadata,
		Labels:     append([]string(nil), src.Labels...),
		CreatedAt:  src.CreatedAt,
	}
}

func SessionToDomain(src Session) session.Session {
	return session.Session{
		ID:         src.ID,
		Channel:    src.Channel,
		CustomerID: src.CustomerID,
		AgentID:    src.AgentID,
		Mode:       src.Mode,
		Title:      src.Title,
		Metadata:   src.Metadata,
		Labels:     append([]string(nil), src.Labels...),
		CreatedAt:  src.CreatedAt,
	}
}

func EventFromDomain(src session.Event) Event {
	return Event{
		ID:          src.ID,
		SessionID:   src.SessionID,
		Source:      src.Source,
		Kind:        src.Kind,
		Offset:      src.Offset,
		TraceID:     src.TraceID,
		CreatedAt:   src.CreatedAt,
		Content:     append([]session.ContentPart(nil), src.Content...),
		Data:        src.Data,
		Metadata:    src.Metadata,
		Deleted:     src.Deleted,
		ExecutionID: src.ExecutionID,
	}
}

func EventToDomain(src Event) session.Event {
	return session.Event{
		ID:          src.ID,
		SessionID:   src.SessionID,
		Source:      src.Source,
		Kind:        src.Kind,
		Offset:      src.Offset,
		TraceID:     src.TraceID,
		CreatedAt:   src.CreatedAt,
		Content:     append([]session.ContentPart(nil), src.Content...),
		Data:        src.Data,
		Metadata:    src.Metadata,
		Deleted:     src.Deleted,
		ExecutionID: src.ExecutionID,
	}
}
