package acp

import (
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
)

const (
	EventKindMessage           = "message"
	EventKindStatus            = "status"
	EventKindApprovalRequested = "approval.requested"
	EventKindApprovalResolved  = "approval.resolved"
	EventKindToolStarted       = "tool.started"
	EventKindToolCompleted     = "tool.completed"
	EventKindToolFailed        = "tool.failed"
	EventKindToolBlocked       = "tool.blocked"
)

type Session struct {
	ID         string          `json:"id"`
	Channel    string          `json:"channel"`
	CustomerID string          `json:"customer_id,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	Mode       string          `json:"mode,omitempty"`
	Title      string          `json:"title,omitempty"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
	Labels     []string        `json:"labels,omitempty"`
	Summary    *SessionSummary `json:"summary,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type SessionSummary struct {
	LastTraceID           string   `json:"last_trace_id,omitempty"`
	LastExecutionID       string   `json:"last_execution_id,omitempty"`
	AppliedGuidelineIDs   []string `json:"applied_guideline_ids,omitempty"`
	ActiveJourneyID       string   `json:"active_journey_id,omitempty"`
	ActiveJourneyStateID  string   `json:"active_journey_state_id,omitempty"`
	CompositionMode       string   `json:"composition_mode,omitempty"`
	KnowledgeSnapshotID   string   `json:"knowledge_snapshot_id,omitempty"`
	RetrieverResultHashes []string `json:"retriever_result_hashes,omitempty"`
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

func NormalizeEvent(src session.Event) Event {
	out := EventFromDomain(src)
	switch strings.TrimSpace(out.Kind) {
	case "approval_result":
		out.Kind = EventKindApprovalResolved
		if out.Data == nil {
			out.Data = map[string]any{}
		}
		if out.Metadata != nil {
			if out.Data["approval_id"] == nil {
				out.Data["approval_id"] = out.Metadata["approval_id"]
			}
			if out.Data["tool_id"] == nil {
				out.Data["tool_id"] = out.Metadata["tool_id"]
			}
		}
		if out.Data["decision"] == nil {
			for _, part := range out.Content {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					out.Data["decision"] = strings.TrimSpace(part.Text)
					break
				}
			}
		}
	}
	return out
}

func IsInternalEvent(src session.Event) bool {
	if src.Kind == "operator.note" {
		return true
	}
	if src.Metadata == nil {
		return false
	}
	value, ok := src.Metadata["internal_only"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func ValidateEvent(event Event) error {
	kind := strings.TrimSpace(event.Kind)
	if kind == "" {
		return fmt.Errorf("kind is required")
	}
	switch kind {
	case EventKindMessage:
		if len(event.Content) == 0 {
			return fmt.Errorf("message events require content")
		}
		var hasContent bool
		for _, part := range event.Content {
			if strings.TrimSpace(part.Text) != "" || strings.TrimSpace(part.URL) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			return fmt.Errorf("message events require non-empty content")
		}
	case EventKindStatus:
		if err := requireDataStrings(event.Data, "code", "state"); err != nil {
			return fmt.Errorf("status event: %w", err)
		}
	case EventKindApprovalRequested:
		if err := requireDataStrings(event.Data, "approval_id", "tool_id", "message", "expires_at"); err != nil {
			return fmt.Errorf("approval.requested event: %w", err)
		}
	case EventKindApprovalResolved:
		if err := requireDataStrings(event.Data, "approval_id", "tool_id", "decision"); err != nil {
			return fmt.Errorf("approval.resolved event: %w", err)
		}
	case EventKindToolStarted:
		if err := requireDataStrings(event.Data, "tool_id", "provider_id"); err != nil {
			return fmt.Errorf("tool.started event: %w", err)
		}
	case EventKindToolCompleted:
		if err := requireDataStrings(event.Data, "tool_id"); err != nil {
			return fmt.Errorf("tool.completed event: %w", err)
		}
	case EventKindToolFailed:
		if err := requireDataStrings(event.Data, "tool_id", "error"); err != nil {
			return fmt.Errorf("tool.failed event: %w", err)
		}
	case EventKindToolBlocked:
		if err := requireDataStrings(event.Data, "tool_id", "reason"); err != nil {
			return fmt.Errorf("tool.blocked event: %w", err)
		}
	default:
		return fmt.Errorf("unsupported ACP event kind %q", kind)
	}
	return nil
}

func requireDataStrings(data map[string]any, keys ...string) error {
	if data == nil {
		return fmt.Errorf("data is required")
	}
	for _, key := range keys {
		value, ok := data[key]
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
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
		Summary:    nil,
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
