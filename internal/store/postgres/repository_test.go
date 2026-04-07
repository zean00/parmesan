package postgres

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/sahal/parmesan/internal/domain/session"
)

func TestCreateSessionPersistsRichFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	sess := session.Session{
		ID:         "sess_1",
		Channel:    "acp",
		CustomerID: "cust_1",
		AgentID:    "agent_1",
		Mode:       "live",
		Title:      "Support",
		Metadata:   map[string]any{"source": "test"},
		Labels:     []string{"vip"},
		CreatedAt:  time.Unix(10, 0).UTC(),
	}

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO sessions (id, channel, customer_id, agent_id, mode, title, metadata_json, labels_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING
	`)).
		WithArgs(sess.ID, sess.Channel, sess.CustomerID, sess.AgentID, sess.Mode, sess.Title, pgxmock.AnyArg(), pgxmock.AnyArg(), sess.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := client.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestUpdateSessionPersistsOperatorModeAndMetadata(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	sess := session.Session{
		ID:         "sess_1",
		Channel:    "acp",
		CustomerID: "cust_1",
		AgentID:    "agent_1",
		Mode:       "manual",
		Title:      "Support",
		Metadata: map[string]any{
			"assigned_operator_id": "op_1",
			"handoff_reason":       "requested human",
		},
		Labels: []string{"vip"},
	}
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE sessions
		SET channel = $2,
		    customer_id = $3,
		    agent_id = $4,
		    mode = $5,
		    title = $6,
		    metadata_json = $7,
		    labels_json = $8
		WHERE id = $1
	`)).
		WithArgs(sess.ID, sess.Channel, sess.CustomerID, sess.AgentID, sess.Mode, sess.Title, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := client.UpdateSession(context.Background(), sess); err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestReadEventDecodesOffsetTraceAndMetadata(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	payload, _ := json.Marshal(session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
		CreatedAt: time.Unix(12, 0).UTC(),
	})
	metadata, _ := json.Marshal(map[string]any{"channel": "web"})
	rows := pgxmock.NewRows([]string{"payload", "offset", "trace_id", "metadata_json", "deleted"}).
		AddRow(payload, int64(123), "trace_1", metadata, false)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT payload, COALESCE(offset,0), COALESCE(trace_id,''), metadata_json, deleted
		FROM session_events
		WHERE session_id = $1 AND id = $2
	`)).
		WithArgs("sess_1", "evt_1").
		WillReturnRows(rows)

	event, err := client.ReadEvent(context.Background(), "sess_1", "evt_1")
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if event.Offset != 123 || event.TraceID != "trace_1" {
		t.Fatalf("event = %#v, want offset/trace decoded", event)
	}
	if got := event.Metadata["channel"]; got != "web" {
		t.Fatalf("event metadata = %#v, want channel=web", event.Metadata)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestListEventsFilteredBuildsExpectedFilters(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	payload, _ := json.Marshal(session.Event{
		ID:        "evt_2",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "status",
		CreatedAt: time.Unix(13, 0).UTC(),
	})
	rows := pgxmock.NewRows([]string{"payload", "offset", "trace_id", "metadata_json", "deleted"}).
		AddRow(payload, int64(200), "trace_2", []byte(`{}`), false)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT payload, COALESCE(offset,0), COALESCE(trace_id,''), metadata_json, deleted
		FROM session_events
		WHERE session_id = $1
	 AND source = $2 AND trace_id = $3 AND COALESCE(offset,0) >= $4 AND deleted = FALSE AND kind = ANY($5) ORDER BY COALESCE(offset,0) ASC, created_at ASC LIMIT $6`)).
		WithArgs("sess_1", "assistant", "trace_2", int64(150), []string{"status"}, 10).
		WillReturnRows(rows)

	events, err := client.ListEventsFiltered(context.Background(), session.EventQuery{
		SessionID:      "sess_1",
		Source:         "assistant",
		TraceID:        "trace_2",
		MinOffset:      150,
		Limit:          10,
		ExcludeDeleted: true,
		Kinds:          []string{"status"},
	})
	if err != nil {
		t.Fatalf("ListEventsFiltered() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_2" || events[0].Offset != 200 {
		t.Fatalf("events = %#v, want filtered evt_2", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}
