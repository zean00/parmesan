package sessionsvc

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestWaitForMoreEventsSeesAppendedEvent(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	listener := NewListener(repo)
	listener.interval = 5 * time.Millisecond
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, session.Session{ID: "sess_1", Channel: "web", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = svc.CreateEvent(ctx, CreateEventParams{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
		})
		close(done)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ok, err := listener.WaitForMoreEvents(waitCtx, session.EventQuery{SessionID: "sess_1", ExcludeDeleted: true})
	if err != nil {
		t.Fatalf("WaitForMoreEvents() error = %v", err)
	}
	if !ok {
		t.Fatal("WaitForMoreEvents() = false, want true")
	}
	<-done
}

func TestWaitForNewStreamingChunksDetectsGrowth(t *testing.T) {
	repo := memory.New()
	svc := New(repo, nil)
	listener := NewListener(repo)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, session.Session{ID: "sess_1", Channel: "web", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	event, err := svc.CreateEvent(ctx, CreateEventParams{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "message",
		Data:      map[string]any{"chunks": []any{"a"}},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		event.Data = map[string]any{"chunks": []any{"a", "b"}}
		_, _ = svc.UpdateEvent(ctx, event)
		close(done)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ok, err := listener.WaitForNewStreamingChunks(waitCtx, "sess_1", "evt_1", 1)
	if err != nil {
		t.Fatalf("WaitForNewStreamingChunks() error = %v", err)
	}
	if !ok {
		t.Fatal("WaitForNewStreamingChunks() = false, want true")
	}
	<-done
}
