package postgres

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
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
		ID:             "sess_1",
		Channel:        "acp",
		CustomerID:     "cust_1",
		AgentID:        "agent_1",
		Mode:           "live",
		Status:         session.StatusActive,
		Title:          "Support",
		Metadata:       map[string]any{"source": "test"},
		Labels:         []string{"vip"},
		LastActivityAt: time.Unix(10, 0).UTC(),
		CreatedAt:      time.Unix(10, 0).UTC(),
	}

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO sessions (id, channel, customer_id, agent_id, mode, status, title, metadata_json, labels_json, last_activity_at, idle_checked_at, awaiting_customer_since, closed_at, close_reason, keep_reason, followup_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, TIMESTAMPTZ '0001-01-01 00:00:00+00'), NULLIF($12, TIMESTAMPTZ '0001-01-01 00:00:00+00'), NULLIF($13, TIMESTAMPTZ '0001-01-01 00:00:00+00'), $14, $15, $16, $17)
		ON CONFLICT (id) DO NOTHING
	`)).
		WithArgs(sess.ID, sess.Channel, sess.CustomerID, sess.AgentID, sess.Mode, string(sess.Status), sess.Title, pgxmock.AnyArg(), pgxmock.AnyArg(), sess.LastActivityAt, time.Time{}, time.Time{}, time.Time{}, nil, nil, 0, sess.CreatedAt).
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
		Status:     session.StatusAwaitingCustomer,
		Title:      "Support",
		Metadata: map[string]any{
			"assigned_operator_id": "op_1",
			"handoff_reason":       "requested human",
		},
		Labels:                []string{"vip"},
		LastActivityAt:        time.Unix(20, 0).UTC(),
		IdleCheckedAt:         time.Unix(21, 0).UTC(),
		AwaitingCustomerSince: time.Unix(22, 0).UTC(),
		CloseReason:           "",
		KeepReason:            "",
		FollowupCount:         1,
	}
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE sessions
		SET channel = $2,
		    customer_id = $3,
		    agent_id = $4,
		    mode = $5,
		    status = $6,
		    title = $7,
		    metadata_json = $8,
		    labels_json = $9,
		    last_activity_at = $10,
		    idle_checked_at = NULLIF($11, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    awaiting_customer_since = NULLIF($12, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    closed_at = NULLIF($13, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    close_reason = $14,
		    keep_reason = $15,
		    followup_count = $16
		WHERE id = $1
	`)).
		WithArgs(sess.ID, sess.Channel, sess.CustomerID, sess.AgentID, sess.Mode, string(sess.Status), sess.Title, pgxmock.AnyArg(), pgxmock.AnyArg(), sess.LastActivityAt, sess.IdleCheckedAt, sess.AwaitingCustomerSince, time.Time{}, nil, nil, sess.FollowupCount).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := client.UpdateSession(context.Background(), sess); err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestSaveAndGetAgentProfile(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	profile := agent.Profile{
		ID:                        "agent_1",
		Name:                      "Support",
		Description:               "Customer support",
		Status:                    "active",
		DefaultPolicyBundleID:     "bundle_1",
		DefaultKnowledgeScopeKind: "agent",
		DefaultKnowledgeScopeID:   "agent_1",
		Metadata:                  map[string]any{"team": "support"},
		CreatedAt:                 time.Unix(10, 0).UTC(),
		UpdatedAt:                 time.Unix(11, 0).UTC(),
	}
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO agent_profiles (id, name, description, status, default_policy_bundle_id, default_knowledge_scope_kind, default_knowledge_scope_id, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    status = EXCLUDED.status,
		    default_policy_bundle_id = EXCLUDED.default_policy_bundle_id,
		    default_knowledge_scope_kind = EXCLUDED.default_knowledge_scope_kind,
		    default_knowledge_scope_id = EXCLUDED.default_knowledge_scope_id,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`)).
		WithArgs(profile.ID, profile.Name, profile.Description, profile.Status, profile.DefaultPolicyBundleID, profile.DefaultKnowledgeScopeKind, profile.DefaultKnowledgeScopeID, pgxmock.AnyArg(), profile.CreatedAt, profile.UpdatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := client.SaveAgentProfile(context.Background(), profile); err != nil {
		t.Fatalf("SaveAgentProfile() error = %v", err)
	}

	metadata, _ := json.Marshal(profile.Metadata)
	rows := pgxmock.NewRows([]string{"id", "name", "description", "status", "default_policy_bundle_id", "default_knowledge_scope_kind", "default_knowledge_scope_id", "metadata_json", "created_at", "updated_at"}).
		AddRow(profile.ID, profile.Name, profile.Description, profile.Status, profile.DefaultPolicyBundleID, profile.DefaultKnowledgeScopeKind, profile.DefaultKnowledgeScopeID, metadata, profile.CreatedAt, profile.UpdatedAt)
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, name, COALESCE(description,''), status, COALESCE(default_policy_bundle_id,''), COALESCE(default_knowledge_scope_kind,''), COALESCE(default_knowledge_scope_id,''), metadata_json, created_at, updated_at
		FROM agent_profiles
		WHERE id = $1
	`)).
		WithArgs("agent_1").
		WillReturnRows(rows)

	got, err := client.GetAgentProfile(context.Background(), "agent_1")
	if err != nil {
		t.Fatalf("GetAgentProfile() error = %v", err)
	}
	if got.DefaultPolicyBundleID != "bundle_1" || got.Metadata["team"] != "support" {
		t.Fatalf("profile = %#v, want decoded profile", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestSaveCustomerPreferenceAndFeedbackRecord(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	now := time.Unix(12, 0).UTC()
	pref := customer.Preference{
		ID: "pref_1", AgentID: "agent_1", CustomerID: "cust_1", Key: "preferred_name", Value: "Alex",
		Source: "operator_feedback", Confidence: 1, Status: "active", EvidenceRefs: []string{"session:sess_1"}, Metadata: map[string]any{"compiler": "test"}, CreatedAt: now, UpdatedAt: now,
	}
	event := customer.PreferenceEvent{
		ID: "pevt_1", PreferenceID: "pref_1", AgentID: "agent_1", CustomerID: "cust_1", Key: "preferred_name", Value: "Alex",
		Action: "upsert", Source: "operator_feedback", Confidence: 1, EvidenceRefs: []string{"session:sess_1"}, Metadata: map[string]any{"compiler": "test"}, CreatedAt: now,
	}
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO customer_preferences (id, agent_id, customer_id, key, value, source, confidence, status, evidence_refs_json, metadata_json, last_confirmed_at, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (agent_id, customer_id, key) DO UPDATE
		SET value = EXCLUDED.value,
		    source = EXCLUDED.source,
		    confidence = EXCLUDED.confidence,
		    status = EXCLUDED.status,
		    evidence_refs_json = EXCLUDED.evidence_refs_json,
		    metadata_json = EXCLUDED.metadata_json,
		    last_confirmed_at = EXCLUDED.last_confirmed_at,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = EXCLUDED.updated_at
	`)).
		WithArgs(pref.ID, pref.AgentID, pref.CustomerID, pref.Key, pref.Value, pref.Source, pref.Confidence, pref.Status, pgxmock.AnyArg(), pgxmock.AnyArg(), pref.LastConfirmedAt, pref.ExpiresAt, pref.CreatedAt, pref.UpdatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO customer_preference_events (id, preference_id, agent_id, customer_id, key, value, action, source, confidence, evidence_refs_json, metadata_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`)).
		WithArgs(event.ID, event.PreferenceID, event.AgentID, event.CustomerID, event.Key, event.Value, event.Action, event.Source, event.Confidence, pgxmock.AnyArg(), pgxmock.AnyArg(), event.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := client.SaveCustomerPreference(context.Background(), pref, event); err != nil {
		t.Fatalf("SaveCustomerPreference() error = %v", err)
	}

	record := feedback.Record{
		ID: "feedback_1", SessionID: "sess_1", OperatorID: "op_1", Category: "preference", Text: "I prefer email.", Outputs: feedback.Outputs{PreferenceIDs: []string{"pref_1"}}, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO operator_feedback (id, session_id, execution_id, trace_id, operator_id, rating, category, text, labels_json, target_event_ids_json, metadata_json, outputs_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE
		SET execution_id = EXCLUDED.execution_id,
		    trace_id = EXCLUDED.trace_id,
		    operator_id = EXCLUDED.operator_id,
		    rating = EXCLUDED.rating,
		    category = EXCLUDED.category,
		    text = EXCLUDED.text,
		    labels_json = EXCLUDED.labels_json,
		    target_event_ids_json = EXCLUDED.target_event_ids_json,
		    metadata_json = EXCLUDED.metadata_json,
		    outputs_json = EXCLUDED.outputs_json,
		    updated_at = EXCLUDED.updated_at
	`)).
		WithArgs(record.ID, record.SessionID, nil, nil, record.OperatorID, record.Rating, record.Category, record.Text, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), record.CreatedAt, record.UpdatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := client.SaveFeedbackRecord(context.Background(), record); err != nil {
		t.Fatalf("SaveFeedbackRecord() error = %v", err)
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

func TestSaveKnowledgeSourcePersistsRichFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	source := knowledge.Source{
		ID:        "src_1",
		ScopeKind: "agent",
		ScopeID:   "agent_1",
		Kind:      "folder",
		URI:       "/docs",
		Checksum:  "abc",
		Status:    "compiled",
		Metadata:  map[string]any{"root": "/docs"},
		CreatedAt: time.Unix(20, 0).UTC(),
		UpdatedAt: time.Unix(21, 0).UTC(),
	}

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO knowledge_sources (id, scope_kind, scope_id, kind, uri, checksum, status, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET scope_kind = EXCLUDED.scope_kind,
		    scope_id = EXCLUDED.scope_id,
		    kind = EXCLUDED.kind,
		    uri = EXCLUDED.uri,
		    checksum = EXCLUDED.checksum,
		    status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`)).
		WithArgs(source.ID, source.ScopeKind, source.ScopeID, source.Kind, source.URI, source.Checksum, source.Status, pgxmock.AnyArg(), source.CreatedAt, source.UpdatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := client.SaveKnowledgeSource(context.Background(), source); err != nil {
		t.Fatalf("SaveKnowledgeSource() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestListKnowledgePagesBySnapshotDecodesCitations(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	citations, _ := json.Marshal([]knowledge.Citation{{SourceID: "src_1", Title: "Returns"}})
	metadata, _ := json.Marshal(map[string]any{"source_path": "returns.md"})
	rows := pgxmock.NewRows([]string{"id", "scope_kind", "scope_id", "source_id", "title", "body", "page_type", "citations_json", "metadata_json", "checksum", "created_at", "updated_at"}).
		AddRow("page_1", "agent", "agent_1", "src_1", "Returns", "Damaged orders can be refunded.", "source_summary", citations, metadata, "sum", time.Unix(22, 0).UTC(), time.Unix(23, 0).UTC())

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT p.id, p.scope_kind, p.scope_id, COALESCE(p.source_id,''), p.title, p.body, p.page_type, p.citations_json, p.metadata_json, COALESCE(p.checksum,''), p.created_at, p.updated_at
		FROM knowledge_pages p
		WHERE ($1 = '' OR p.scope_kind = $1)
		  AND ($2 = '' OR p.scope_id = $2)
		  AND ($3 = '' OR p.id IN (
		    SELECT jsonb_array_elements_text(page_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		ORDER BY p.updated_at DESC
		LIMIT $4
	`)).
		WithArgs("agent", "agent_1", "snap_1", 1000).
		WillReturnRows(rows)

	pages, err := client.ListKnowledgePages(context.Background(), knowledge.PageQuery{
		ScopeKind:  "agent",
		ScopeID:    "agent_1",
		SnapshotID: "snap_1",
	})
	if err != nil {
		t.Fatalf("ListKnowledgePages() error = %v", err)
	}
	if len(pages) != 1 || pages[0].Title != "Returns" || len(pages[0].Citations) != 1 {
		t.Fatalf("pages = %#v, want decoded cited page", pages)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestSaveMediaAssetAndListDerivedSignals(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	asset := media.Asset{
		ID:        "media_1",
		SessionID: "sess_1",
		EventID:   "evt_1",
		PartIndex: 1,
		Type:      "image",
		URL:       "https://example.test/damage.png",
		MimeType:  "image/png",
		Status:    "pending",
		Metadata:  map[string]any{"mime_type": "image/png"},
		CreatedAt: time.Unix(24, 0).UTC(),
	}

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO media_assets (id, session_id, event_id, part_index, type, url, mime_type, checksum, status, retention, metadata_json, created_at, enriched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json,
		    enriched_at = EXCLUDED.enriched_at
	`)).
		WithArgs(asset.ID, asset.SessionID, asset.EventID, asset.PartIndex, asset.Type, asset.URL, asset.MimeType, nil, asset.Status, nil, pgxmock.AnyArg(), asset.CreatedAt, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := client.SaveMediaAsset(context.Background(), asset); err != nil {
		t.Fatalf("SaveMediaAsset() error = %v", err)
	}

	signalMetadata, _ := json.Marshal(map[string]any{"language": "en"})
	rows := pgxmock.NewRows([]string{"id", "asset_id", "session_id", "event_id", "kind", "value", "confidence", "metadata_json", "extractor", "created_at"}).
		AddRow("sig_1", "media_1", "sess_1", "evt_1", "ocr_text", "ORDER-123", 0.9, signalMetadata, "noop", time.Unix(25, 0).UTC())
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, asset_id, session_id, event_id, kind, value, confidence, metadata_json, extractor, created_at
		FROM derived_signals
		WHERE ($1 = '' OR session_id = $1)
		ORDER BY created_at ASC
	`)).
		WithArgs("sess_1").
		WillReturnRows(rows)

	signals, err := client.ListDerivedSignals(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("ListDerivedSignals() error = %v", err)
	}
	if len(signals) != 1 || signals[0].Kind != "ocr_text" || signals[0].Value != "ORDER-123" {
		t.Fatalf("signals = %#v, want decoded signal", signals)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestListKnowledgeChunksDecodesEmbeddings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	vector, _ := json.Marshal([]float32{1, 0, 0.5})
	citations, _ := json.Marshal([]knowledge.Citation{{SourceID: "src_1"}})
	metadata, _ := json.Marshal(map[string]any{"page_title": "Returns"})
	rows := pgxmock.NewRows([]string{"id", "page_id", "scope_kind", "scope_id", "text", "embedding_json", "citations_json", "metadata_json", "created_at"}).
		AddRow("chunk_1", "page_1", "agent", "agent_1", "Damaged orders can be refunded.", vector, citations, metadata, time.Unix(26, 0).UTC())

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT ch.id, ch.page_id, ch.scope_kind, ch.scope_id, ch.text, ch.embedding_json, ch.citations_json, ch.metadata_json, ch.created_at
		FROM knowledge_chunks ch
		WHERE ($1 = '' OR ch.scope_kind = $1)
		  AND ($2 = '' OR ch.scope_id = $2)
		  AND ($3 = '' OR ch.id IN (
		    SELECT jsonb_array_elements_text(chunk_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		ORDER BY ch.created_at DESC
		LIMIT $4
	`)).
		WithArgs("agent", "agent_1", "snap_1", 1000).
		WillReturnRows(rows)

	chunks, err := client.ListKnowledgeChunks(context.Background(), knowledge.ChunkQuery{
		ScopeKind:  "agent",
		ScopeID:    "agent_1",
		SnapshotID: "snap_1",
	})
	if err != nil {
		t.Fatalf("ListKnowledgeChunks() error = %v", err)
	}
	if len(chunks) != 1 || len(chunks[0].Vector) != 3 {
		t.Fatalf("chunks = %#v, want decoded embedding vector", chunks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestSearchKnowledgeChunksUsesVectorRanking(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	vector, _ := json.Marshal([]float32{1, 0, 0.5})
	citations, _ := json.Marshal([]knowledge.Citation{{SourceID: "src_1"}})
	metadata, _ := json.Marshal(map[string]any{"page_title": "Returns"})
	rows := pgxmock.NewRows([]string{"id", "page_id", "scope_kind", "scope_id", "text", "embedding_json", "citations_json", "metadata_json", "created_at"}).
		AddRow("chunk_1", "page_1", "agent", "agent_1", "Damaged orders can be refunded.", vector, citations, metadata, time.Unix(27, 0).UTC())

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT ch.id, ch.page_id, ch.scope_kind, ch.scope_id, ch.text, ch.embedding_json, ch.citations_json, ch.metadata_json, ch.created_at
		FROM knowledge_chunks ch
		WHERE ($1 = '' OR ch.scope_kind = $1)
		  AND ($2 = '' OR ch.scope_id = $2)
		  AND ($3 = '' OR ch.id IN (
		    SELECT jsonb_array_elements_text(chunk_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		  AND ch.embedding IS NOT NULL
		ORDER BY ch.embedding <=> $4::vector ASC, ch.created_at DESC
		LIMIT $5
	`)).
		WithArgs("agent", "agent_1", "snap_1", "[1,0,0.5]", 2).
		WillReturnRows(rows)

	chunks, err := client.SearchKnowledgeChunks(context.Background(), knowledge.ChunkSearchQuery{
		ScopeKind:  "agent",
		ScopeID:    "agent_1",
		SnapshotID: "snap_1",
		Vector:     []float32{1, 0, 0.5},
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("SearchKnowledgeChunks() error = %v", err)
	}
	if len(chunks) != 1 || chunks[0].ID != "chunk_1" {
		t.Fatalf("chunks = %#v, want ranked chunk_1", chunks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}

func TestGetKnowledgeUpdateProposalDecodesPayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer mock.Close()

	client := &Client{querier: mock}
	evidence, _ := json.Marshal([]knowledge.Citation{{URI: "session:sess_1", Anchor: "trace_1"}})
	payload, _ := json.Marshal(map[string]any{"page": map[string]any{"title": "Returns"}})
	rows := pgxmock.NewRows([]string{"id", "scope_kind", "scope_id", "kind", "state", "rationale", "evidence_json", "payload_json", "created_at", "updated_at"}).
		AddRow("prop_1", "agent", "agent_1", "conversation_insight", "draft", "rationale", evidence, payload, time.Unix(28, 0).UTC(), time.Unix(29, 0).UTC())

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, scope_kind, scope_id, kind, state, rationale, evidence_json, payload_json, created_at, updated_at
		FROM knowledge_update_proposals
		WHERE id = $1
	`)).
		WithArgs("prop_1").
		WillReturnRows(rows)

	item, err := client.GetKnowledgeUpdateProposal(context.Background(), "prop_1")
	if err != nil {
		t.Fatalf("GetKnowledgeUpdateProposal() error = %v", err)
	}
	if item.ID != "prop_1" || item.State != "draft" {
		t.Fatalf("proposal = %#v, want decoded proposal", item)
	}
	page, ok := item.Payload["page"].(map[string]any)
	if !ok || page["title"] != "Returns" {
		t.Fatalf("payload = %#v, want page title", item.Payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ExpectationsWereMet() error = %v", err)
	}
}
