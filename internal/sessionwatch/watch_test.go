package sessionwatch

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestEnsureSessionWatchDedupesEquivalentIntent(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	sess := session.Session{ID: "sess_1", Channel: "web", CreatedAt: now}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	intent, ok := BuildDeliveryIntent(SourceRuntime, "commerce.get_delivery_status", "ord_123", map[string]any{"order_id": "ord_123"}, now)
	if !ok {
		t.Fatal("expected delivery intent")
	}
	first, created, err := EnsureSessionWatch(ctx, repo, sess, intent, now)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected first watch creation")
	}
	second, created, err := EnsureSessionWatch(ctx, repo, sess, intent, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected equivalent watch to be deduped")
	}
	if second.ID != first.ID {
		t.Fatalf("watch id = %q, want %q", second.ID, first.ID)
	}
}

func TestParseAppointmentTimeFromText(t *testing.T) {
	now := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	parsed, ok := ParseAppointmentTimeFromText("Please remind me about my appointment tomorrow at 6pm", now)
	if !ok {
		t.Fatal("expected appointment time parse")
	}
	want := time.Date(2026, 4, 10, 18, 0, 0, 0, time.UTC)
	if !parsed.Equal(want) {
		t.Fatalf("parsed = %v, want %v", parsed, want)
	}
}
