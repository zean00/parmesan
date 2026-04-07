package acp

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestDomainRoundTripPreservesFields(t *testing.T) {
	src := session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "status",
		Offset:    42,
		TraceID:   "trace_1",
		Data:      map[string]any{"status": "working"},
		Metadata:  map[string]any{"channel": "web"},
		Deleted:   true,
		CreatedAt: time.Now().UTC(),
	}
	got := EventToDomain(EventFromDomain(src))
	if got.ID != src.ID || got.Offset != src.Offset || got.TraceID != src.TraceID || !got.Deleted {
		t.Fatalf("round trip = %#v, want preserve fields from %#v", got, src)
	}
}

func TestServiceOpenSessionAndListEvents(t *testing.T) {
	repo := memory.New()
	svc := NewService(sessionsvc.New(repo, nil))
	ctx := context.Background()
	_, err := svc.OpenSession(ctx, Session{ID: "sess_1", Channel: "web", Metadata: map[string]any{"x": "y"}, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	_, err = svc.CreateEvent(ctx, Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		TraceID:   "trace_1",
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
		CreatedAt: time.Now().UTC(),
	}, false)
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}

	events, err := svc.ListEvents(ctx, "sess_1", 0)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_1" {
		t.Fatalf("events = %#v, want evt_1", events)
	}
}
