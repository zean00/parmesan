package runner

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestIngestMediaAssetsPersistsNonTextContentParts(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test")
	event := session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		Content: []session.ContentPart{
			{Type: "text", Text: "look at this"},
			{Type: "image", URL: "https://example.test/damage.png", Meta: map[string]any{"mime_type": "image/png"}},
		},
	}
	if err := r.ingestMediaAssets(context.Background(), []session.Event{event}); err != nil {
		t.Fatal(err)
	}
	assets, err := repo.ListMediaAssets(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Type != "image" || assets[0].MimeType != "image/png" {
		t.Fatalf("assets = %#v, want one image asset", assets)
	}
	signals, err := repo.ListDerivedSignals(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].Kind != "image_summary" {
		t.Fatalf("signals = %#v, want inferred image summary only", signals)
	}
}

func TestIngestMediaAssetsSkipsAlreadyIngestedAssets(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test")
	now := time.Now().UTC()
	event := session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content: []session.ContentPart{{
			Type: "image",
			URL:  "https://example.test/damage.png",
			Meta: map[string]any{"summary": "original summary"},
		}},
	}
	if err := r.ingestMediaAssets(context.Background(), []session.Event{event}); err != nil {
		t.Fatal(err)
	}
	assets, err := repo.ListMediaAssets(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 {
		t.Fatalf("assets = %#v, want one asset", assets)
	}
	assets[0].Status = "failed"
	assets[0].Metadata["retry_count"] = 2
	assets[0].Metadata["next_retry_at"] = now.Add(time.Hour).Format(time.RFC3339Nano)
	if err := repo.SaveMediaAsset(context.Background(), assets[0]); err != nil {
		t.Fatal(err)
	}

	if err := r.ingestMediaAssets(context.Background(), []session.Event{event}); err != nil {
		t.Fatal(err)
	}
	assets, err = repo.ListMediaAssets(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 {
		t.Fatalf("assets = %#v, want one idempotent asset", assets)
	}
	if assets[0].Status != "failed" || mediaRetryCount(assets[0].Metadata) != 2 {
		t.Fatalf("asset = %#v, want failed asset retry metadata preserved", assets[0])
	}
	signals, err := repo.ListDerivedSignals(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %#v, want no duplicate enrichment signals", signals)
	}
}

func TestIngestMediaAssetsExtractsImageAndAudioSignals(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test")
	event := session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		Content: []session.ContentPart{
			{Type: "image", URL: "https://example.test/cracked-phone.png", Meta: map[string]any{"mime_type": "image/png", "ocr_text": "ORDER-123", "summary": "Photo of a cracked phone screen"}},
			{Type: "audio", URL: "https://example.test/note.mp3", Meta: map[string]any{"mime_type": "audio/mpeg", "transcript": "I prefer email updates.", "language": "en"}},
		},
	}
	if err := r.ingestMediaAssets(context.Background(), []session.Event{event}); err != nil {
		t.Fatal(err)
	}
	signals, err := repo.ListDerivedSignals(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) < 4 {
		t.Fatalf("signals = %#v, want extracted image and audio signals", signals)
	}
	assets, err := repo.ListMediaAssets(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Fatalf("assets = %#v, want two enriched assets", assets)
	}
	if assets[0].Metadata["enrichment_status"] != "succeeded" || assets[1].Metadata["enrichment_status"] != "succeeded" {
		t.Fatalf("asset metadata = %#v, want enrichment_status=succeeded", assets)
	}
	extractors0, _ := assets[0].Metadata["extractors"].([]string)
	extractors1, _ := assets[1].Metadata["extractors"].([]string)
	if len(extractors0) == 0 && len(extractors1) == 0 {
		t.Fatalf("asset metadata = %#v, want extractors recorded", assets)
	}
	providers0, _ := assets[0].Metadata["providers"].([]string)
	providers1, _ := assets[1].Metadata["providers"].([]string)
	if len(providers0) == 0 && len(providers1) == 0 {
		t.Fatalf("asset metadata = %#v, want providers recorded", assets)
	}
}

func TestLearnFromExecutionCreatesCustomerSnapshotAndProposal(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test")
	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", AgentID: "agent_1", CustomerID: "cust_1", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "I prefer email updates. My name is Alex."}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_2",
		SessionID: "sess_1",
		Source:    "operator",
		Kind:      "operator.note",
		CreatedAt: now.Add(time.Second),
		Content:   []session.ContentPart{{Type: "text", Text: "This return exception should become shared knowledge."}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveDerivedSignal(context.Background(), media.DerivedSignal{
		ID: "sig_1", SessionID: "sess_1", EventID: "evt_1", AssetID: "asset_1", Kind: "ocr_text", Value: "ORDER-123", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.learnFromExecution(context.Background(), execution.TurnExecution{
		ID: "exec_1", SessionID: "sess_1", TraceID: "trace_1",
	}); err != nil {
		t.Fatal(err)
	}
	prefs, err := repo.ListCustomerPreferences(context.Background(), customer.PreferenceQuery{AgentID: "agent_1", CustomerID: "cust_1", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) < 2 {
		t.Fatalf("preferences = %#v, want learned customer preferences", prefs)
	}
	proposals, err := repo.ListKnowledgeUpdateProposals(context.Background(), "agent", "agent_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) == 0 || proposals[0].State != "draft" {
		t.Fatalf("proposals = %#v, want draft shared knowledge proposal", proposals)
	}
}

func TestRunOnceRetriesEligibleFailedMediaAssets(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test")
	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_retry", Channel: "acp", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_retry",
		SessionID: "sess_retry",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content: []session.ContentPart{{
			Type: "image",
			URL:  "https://example.test/retry.png",
			Meta: map[string]any{"summary": "Retryable image"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMediaAsset(context.Background(), media.Asset{
		ID:        "asset_retry",
		SessionID: "sess_retry",
		EventID:   "evt_retry",
		PartIndex: 0,
		Type:      "image",
		Status:    "failed",
		Metadata: map[string]any{
			"retry_count":   1,
			"next_retry_at": now.Add(-time.Minute).Format(time.RFC3339Nano),
		},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	r.runOnce(context.Background())

	assets, err := repo.ListMediaAssets(context.Background(), "sess_retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Status != "succeeded" {
		t.Fatalf("assets = %#v, want retried asset to succeed", assets)
	}
	signals, err := repo.ListDerivedSignals(context.Background(), "sess_retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) == 0 {
		t.Fatalf("signals = %#v, want retry-produced signals", signals)
	}
	records, err := repo.ListAuditRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var foundStart, foundSuccess bool
	for _, record := range records {
		switch record.Kind {
		case "media.retry.started":
			foundStart = true
		case "media.enrichment.succeeded":
			foundSuccess = true
		}
	}
	if !foundStart || !foundSuccess {
		t.Fatalf("audit records = %#v, want retry start and enrichment success", records)
	}
}
