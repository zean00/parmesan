package sessionsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
)

type Service struct {
	repo   store.Repository
	writes *asyncwrite.Queue
}

type CreateEventParams struct {
	ID          string
	SessionID   string
	Source      string
	Kind        string
	Content     []session.ContentPart
	Data        map[string]any
	Metadata    map[string]any
	ExecutionID string
	TraceID     string
	CreatedAt   time.Time
	Async       bool
}

func New(repo store.Repository, writes *asyncwrite.Queue) *Service {
	return &Service{repo: repo, writes: writes}
}

func (s *Service) CreateSession(ctx context.Context, sess session.Session) (session.Session, error) {
	if strings.TrimSpace(sess.ID) == "" {
		sess.ID = fmt.Sprintf("sess_%d", time.Now().UTC().UnixNano())
	}
	if strings.TrimSpace(sess.Channel) == "" {
		sess.Channel = "web"
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	if sess.Labels == nil {
		sess.Labels = []string{}
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return session.Session{}, err
	}
	return sess, nil
}

func (s *Service) UpdateSession(ctx context.Context, sess session.Session) (session.Session, error) {
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	if sess.Labels == nil {
		sess.Labels = []string{}
	}
	if err := s.repo.UpdateSession(ctx, sess); err != nil {
		return session.Session{}, err
	}
	return sess, nil
}

func (s *Service) UpsertSessionMetadata(ctx context.Context, sessionID string, values map[string]any) (session.Session, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	for k, v := range values {
		sess.Metadata[k] = v
	}
	return s.UpdateSession(ctx, sess)
}

func (s *Service) CreateMessageEvent(ctx context.Context, sessionID, source, text, executionID, traceID string, metadata map[string]any, async bool) (session.Event, error) {
	return s.CreateEvent(ctx, CreateEventParams{
		SessionID:   sessionID,
		Source:      source,
		Kind:        "message",
		Content:     []session.ContentPart{{Type: "text", Text: text}},
		ExecutionID: executionID,
		TraceID:     traceID,
		Metadata:    metadata,
		Async:       async,
	})
}

func (s *Service) CreateStatusEvent(ctx context.Context, sessionID, source, status, executionID, traceID string, data, metadata map[string]any, async bool) (session.Event, error) {
	payload := map[string]any{"status": status}
	for k, v := range data {
		payload[k] = v
	}
	return s.CreateEvent(ctx, CreateEventParams{
		SessionID:   sessionID,
		Source:      source,
		Kind:        "status",
		Data:        payload,
		ExecutionID: executionID,
		TraceID:     traceID,
		Metadata:    metadata,
		Async:       async,
	})
}

func (s *Service) CreateEvent(ctx context.Context, params CreateEventParams) (session.Event, error) {
	if _, err := s.repo.GetSession(ctx, params.SessionID); err != nil {
		return session.Event{}, err
	}
	now := time.Now().UTC()
	if strings.TrimSpace(params.ID) == "" {
		params.ID = fmt.Sprintf("evt_%d", now.UnixNano())
	}
	if strings.TrimSpace(params.Source) == "" {
		params.Source = "customer"
	}
	if strings.TrimSpace(params.Kind) == "" {
		params.Kind = "message"
	}
	if strings.TrimSpace(params.TraceID) == "" {
		params.TraceID = fmt.Sprintf("trace_%d", now.UnixNano())
	}
	if params.CreatedAt.IsZero() {
		params.CreatedAt = now
	}
	if params.Metadata == nil {
		params.Metadata = map[string]any{}
	}
	event := session.Event{
		ID:          params.ID,
		SessionID:   params.SessionID,
		Source:      params.Source,
		Kind:        params.Kind,
		Offset:      params.CreatedAt.UnixNano(),
		TraceID:     params.TraceID,
		CreatedAt:   params.CreatedAt,
		Content:     append([]session.ContentPart(nil), params.Content...),
		Data:        params.Data,
		Metadata:    params.Metadata,
		ExecutionID: params.ExecutionID,
	}
	if params.Async && s.writes != nil {
		return event, s.writes.AppendEvent(ctx, event)
	}
	return event, s.repo.AppendEvent(ctx, event)
}

func (s *Service) ReadEvent(ctx context.Context, sessionID, eventID string) (session.Event, error) {
	return s.repo.ReadEvent(ctx, sessionID, eventID)
}

func (s *Service) UpdateEvent(ctx context.Context, event session.Event) (session.Event, error) {
	return event, s.repo.UpdateEvent(ctx, event)
}

func (s *Service) ListEvents(ctx context.Context, query session.EventQuery) ([]session.Event, error) {
	return s.repo.ListEventsFiltered(ctx, query)
}
