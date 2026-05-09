package builtintools

import (
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func TestCurrentTimeUsesIanaTimezone(t *testing.T) {
	now := time.Date(2026, 5, 6, 10, 30, 0, 0, time.UTC)
	out, err := Invoke(tool.CatalogEntry{Name: CurrentTimeName}, map[string]any{"timezone": "Asia/Jakarta"}, now)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out["timezone"] != "Asia/Jakarta" || out["utc_offset"] != "+07:00" || out["local_clock"] != "17:30:00" {
		t.Fatalf("output = %#v, want Jakarta local time", out)
	}
}

func TestCurrentTimeResolvesKnownLocation(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out, err := Invoke(tool.CatalogEntry{Name: CurrentTimeName}, map[string]any{"location": "Jakarta"}, now)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out["timezone"] != "Asia/Jakarta" || out["location"] != "Jakarta" {
		t.Fatalf("output = %#v, want Jakarta timezone and location", out)
	}
}

func TestCurrentTimeRejectsUnknownLocation(t *testing.T) {
	_, err := Invoke(tool.CatalogEntry{Name: CurrentTimeName}, map[string]any{"location": "Atlantis"}, time.Now().UTC())
	if err == nil {
		t.Fatal("Invoke() error = nil, want unknown location error")
	}
}

func TestAskUserReturnsAwaitingCustomerPayload(t *testing.T) {
	out, err := Invoke(tool.CatalogEntry{Name: AskUserName}, map[string]any{
		"question":          "What order number should I check?",
		"reason":            "missing order number",
		"expected_response": "order number",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out["tool_id"] != "builtin.ask_user" || out["status"] != "awaiting_customer" || out["question"] != "What order number should I check?" {
		t.Fatalf("output = %#v, want ask_user awaiting payload", out)
	}
}

func TestAskUserRequiresQuestion(t *testing.T) {
	_, err := Invoke(tool.CatalogEntry{Name: AskUserName}, map[string]any{"reason": "missing order number"}, time.Now().UTC())
	if err == nil {
		t.Fatal("Invoke() error = nil, want missing question error")
	}
}
