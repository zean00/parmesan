package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestInboundMessagePersistsTriggerEventID(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	srv := New(":0", repo, writes)
	req := httptest.NewRequest(http.MethodPost, "/v1/web/messages", strings.NewReader(`{"conversation_id":"conv_1","user_id":"user_1","text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var payload struct {
		SessionID string `json:"session_id"`
		EventID   string `json:"event_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), payload.SessionID)
		if err != nil || len(events) == 0 {
			return false
		}
		return events[0].ID == payload.EventID
	})

	execs, err := repo.ListExecutions(context.Background())
	if err != nil {
		t.Fatalf("ListExecutions() error = %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("executions = %#v, want exactly one execution", execs)
	}
	if execs[0].TriggerEventID != payload.EventID {
		t.Fatalf("TriggerEventID = %q, want %q", execs[0].TriggerEventID, payload.EventID)
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %s", timeout)
}
