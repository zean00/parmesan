package sessionsvc

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestCreateEventAssignsTraceAndOffset(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, session.Session{ID: "sess_1", Channel: "web", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	event, err := svc.CreateEvent(ctx, CreateEventParams{
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if event.TraceID == "" {
		t.Fatal("TraceID = empty, want assigned trace")
	}
	if event.Offset == 0 {
		t.Fatal("Offset = 0, want assigned offset")
	}
}

func TestUpsertSessionMetadataMergesValues(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, session.Session{
		ID:        "sess_1",
		Channel:   "web",
		Metadata:  map[string]any{"existing": "value"},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	updated, err := svc.UpsertSessionMetadata(ctx, "sess_1", map[string]any{"new": "field"})
	if err != nil {
		t.Fatalf("UpsertSessionMetadata() error = %v", err)
	}
	if got := updated.Metadata["existing"]; got != "value" {
		t.Fatalf("existing metadata = %#v, want value", got)
	}
	if got := updated.Metadata["new"]; got != "field" {
		t.Fatalf("new metadata = %#v, want field", got)
	}
}

func TestListEventsUsesQueryFilters(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, session.Session{ID: "sess_1", Channel: "web", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	_, _ = svc.CreateEvent(ctx, CreateEventParams{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		TraceID:   "trace_1",
		CreatedAt: time.Unix(0, 100),
	})
	_, _ = svc.CreateEvent(ctx, CreateEventParams{
		ID:        "evt_2",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "status",
		TraceID:   "trace_2",
		CreatedAt: time.Unix(0, 200),
	})

	events, err := svc.ListEvents(ctx, session.EventQuery{
		SessionID: "sess_1",
		Source:    "assistant",
		MinOffset: 150,
	})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_2" {
		t.Fatalf("events = %#v, want filtered evt_2", events)
	}
}

func TestCustomerEventReactivatesKeptSession(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := svc.CreateSession(ctx, session.Session{
		ID:                    "sess_keep",
		Channel:               "web",
		Status:                session.StatusSessionKeep,
		KeepReason:            "delivery_watch",
		LastActivityAt:        now.Add(-time.Hour),
		AwaitingCustomerSince: now.Add(-30 * time.Minute),
		IdleCheckedAt:         now.Add(-10 * time.Minute),
		CreatedAt:             now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	_, err = svc.CreateEvent(ctx, CreateEventParams{
		SessionID: "sess_keep",
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "Any update?"}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	sess, err := repo.GetSession(ctx, "sess_keep")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if sess.Status != session.StatusActive {
		t.Fatalf("status = %s, want active", sess.Status)
	}
	if sess.KeepReason != "" || !sess.IdleCheckedAt.IsZero() || !sess.AwaitingCustomerSince.IsZero() {
		t.Fatalf("session = %#v, want keep/idle/awaiting cleared on customer reply", sess)
	}
}
