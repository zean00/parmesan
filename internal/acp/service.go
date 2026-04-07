package acp

import (
	"context"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/sessionsvc"
)

type Service struct {
	sessions *sessionsvc.Service
}

func NewService(sessions *sessionsvc.Service) *Service {
	return &Service{sessions: sessions}
}

func (s *Service) OpenSession(ctx context.Context, req Session) (Session, error) {
	created, err := s.sessions.CreateSession(ctx, SessionToDomain(req))
	if err != nil {
		return Session{}, err
	}
	return SessionFromDomain(created), nil
}

func (s *Service) CreateEvent(ctx context.Context, req Event, async bool) (Event, error) {
	if err := ValidateEvent(req); err != nil {
		return Event{}, err
	}
	created, err := s.sessions.CreateEvent(ctx, sessionsvc.CreateEventParams{
		ID:          req.ID,
		SessionID:   req.SessionID,
		Source:      req.Source,
		Kind:        req.Kind,
		Content:     req.Content,
		Data:        req.Data,
		Metadata:    req.Metadata,
		ExecutionID: req.ExecutionID,
		TraceID:     req.TraceID,
		CreatedAt:   req.CreatedAt,
		Async:       async,
	})
	if err != nil {
		return Event{}, err
	}
	return NormalizeEvent(created), nil
}

func (s *Service) ListEvents(ctx context.Context, sessionID string, minOffset int64) ([]Event, error) {
	items, err := s.sessions.ListEvents(ctx, session.EventQuery{
		SessionID:      sessionID,
		MinOffset:      minOffset,
		ExcludeDeleted: true,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizeEvent(item))
	}
	return out, nil
}
