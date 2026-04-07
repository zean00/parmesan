package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestCreateRolloutPromotesProposalAndEnforcesSingleActivePerChannel(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	for _, proposal := range []rollout.Proposal{
		{
			ID:                "proposal_1",
			SourceBundleID:    "bundle_active",
			CandidateBundleID: "bundle_candidate_1",
			State:             rollout.StateShadow,
			CreatedAt:         now,
			UpdatedAt:         now,
		},
		{
			ID:                "proposal_2",
			SourceBundleID:    "bundle_active",
			CandidateBundleID: "bundle_candidate_2",
			State:             rollout.StateShadow,
			CreatedAt:         now,
			UpdatedAt:         now,
		},
	} {
		if err := repo.SaveProposal(context.Background(), proposal); err != nil {
			t.Fatalf("SaveProposal() error = %v", err)
		}
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/rollouts", strings.NewReader(`{"proposal_id":"proposal_1","channel":"web","percentage":25}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first rollout status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetProposal(context.Background(), "proposal_1")
		return err == nil && item.State == rollout.StateCanary
	})

	req = httptest.NewRequest(http.MethodPost, "/v1/rollouts", strings.NewReader(`{"proposal_id":"proposal_2","channel":"web","percentage":50}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second rollout status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRollbackRolloutMovesProposalBackToShadow(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.SaveProposal(context.Background(), rollout.Proposal{
		ID:                "proposal_1",
		SourceBundleID:    "bundle_active",
		CandidateBundleID: "bundle_candidate",
		State:             rollout.StateCanary,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("SaveProposal() error = %v", err)
	}
	if err := repo.SaveRollout(context.Background(), rollout.Record{
		ID:         "rollout_1",
		ProposalID: "proposal_1",
		Status:     rollout.RolloutActive,
		Channel:    "web",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveRollout() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/rollouts/rollout_1/rollback", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rollback status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetRollout(context.Background(), "rollout_1")
		return err == nil && item.Status == rollout.RolloutRolledBack
	})
	waitFor(t, time.Second, func() bool {
		item, err := repo.GetProposal(context.Background(), "proposal_1")
		return err == nil && item.State == rollout.StateShadow
	})
}

func TestDisableRolloutMarksRecordDisabled(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.SaveRollout(context.Background(), rollout.Record{
		ID:         "rollout_1",
		ProposalID: "proposal_1",
		Status:     rollout.RolloutActive,
		Channel:    "web",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveRollout() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/rollouts/rollout_1/disable", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("disable rollout status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetRollout(context.Background(), "rollout_1")
		return err == nil && item.Status == rollout.RolloutDisabled
	})
}

func TestRegisterProviderPersistsImmediatelyBeforeAsyncSync(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tools/providers/register", strings.NewReader(`{"id":"provider_1","kind":"mcp_remote","name":"demo","uri":"http://example.invalid"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("register provider status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	item, err := repo.GetProvider(context.Background(), "provider_1")
	if err != nil {
		t.Fatalf("GetProvider() error = %v", err)
	}
	if item.ID != "provider_1" {
		t.Fatalf("provider = %#v, want provider_1", item)
	}
}

func TestListCatalogIncludesParsedModulePathAndDocumentID(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	err := repo.SaveCatalogEntries(context.Background(), []tool.CatalogEntry{{
		ID:              "tool_1",
		ProviderID:      "provider_1",
		Name:            "lookup_doc",
		Description:     "lookup",
		Schema:          `{}`,
		RuntimeProtocol: "mcp",
		MetadataJSON:    `{"document_id":"doc_123","module_path":"tests.tool_utilities"}`,
		ImportedAt:      time.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("SaveCatalogEntries() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/tools/catalog", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list catalog status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var decoded []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("catalog length = %d, want 1", len(decoded))
	}
	if got := decoded[0]["document_id"]; got != "doc_123" {
		t.Fatalf("document_id = %#v, want %q", got, "doc_123")
	}
	if got := decoded[0]["module_path"]; got != "tests.tool_utilities" {
		t.Fatalf("module_path = %#v, want %q", got, "tests.tool_utilities")
	}
	metadata, ok := decoded[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want object", decoded[0]["metadata"])
	}
	if got := metadata["module_path"]; got != "tests.tool_utilities" {
		t.Fatalf("metadata.module_path = %#v, want %q", got, "tests.tool_utilities")
	}
}

func TestPromoteProposalActiveRetiresExistingActiveAndDisablesCanary(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	for _, item := range []rollout.Proposal{
		{
			ID:                "proposal_old",
			SourceBundleID:    "bundle_a",
			CandidateBundleID: "bundle_b",
			State:             rollout.StateActive,
			CreatedAt:         now,
			UpdatedAt:         now,
		},
		{
			ID:                     "proposal_new",
			SourceBundleID:         "bundle_b",
			CandidateBundleID:      "bundle_c",
			State:                  rollout.StateCanary,
			RequiresManualApproval: true,
			CreatedAt:              now,
			UpdatedAt:              now,
		},
	} {
		if err := repo.SaveProposal(context.Background(), item); err != nil {
			t.Fatalf("SaveProposal() error = %v", err)
		}
	}
	if err := repo.SaveRollout(context.Background(), rollout.Record{
		ID:         "rollout_new",
		ProposalID: "proposal_new",
		Status:     rollout.RolloutActive,
		Channel:    "web",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveRollout() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/proposal_new/state", strings.NewReader(`{"state":"active","approved_high_risk":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("promote proposal status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetProposal(context.Background(), "proposal_old")
		return err == nil && item.State == rollout.StateRetired
	})
	waitFor(t, time.Second, func() bool {
		item, err := repo.GetProposal(context.Background(), "proposal_new")
		return err == nil && item.State == rollout.StateActive
	})
	waitFor(t, time.Second, func() bool {
		item, err := repo.GetRollout(context.Background(), "rollout_new")
		return err == nil && item.Status == rollout.RolloutDisabled
	})
}

func TestProposalSummaryIncludesRolloutsAndEvalRuns(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.SaveProposal(context.Background(), rollout.Proposal{
		ID:                "proposal_1",
		SourceBundleID:    "bundle_a",
		CandidateBundleID: "bundle_b",
		State:             rollout.StateShadow,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("SaveProposal() error = %v", err)
	}
	if err := repo.SaveRollout(context.Background(), rollout.Record{
		ID:         "rollout_1",
		ProposalID: "proposal_1",
		Status:     rollout.RolloutActive,
		Channel:    "web",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveRollout() error = %v", err)
	}
	if err := repo.CreateEvalRun(context.Background(), replay.Run{
		ID:                "eval_1",
		Type:              replay.TypeShadow,
		ProposalID:        "proposal_1",
		SourceExecutionID: "exec_1",
		Status:            replay.StatusSucceeded,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("CreateEvalRun() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/proposals/proposal_1/summary", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proposal summary status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Proposal rollout.Proposal `json:"proposal"`
		Rollouts []rollout.Record `json:"rollouts"`
		EvalRuns []replay.Run     `json:"eval_runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if payload.Proposal.ID != "proposal_1" || len(payload.Rollouts) != 1 || len(payload.EvalRuns) != 1 {
		t.Fatalf("summary = %#v, want linked proposal, rollout, and eval run", payload)
	}
}

func TestListReplayExecutionsReturnsStoredRuns(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateEvalRun(context.Background(), replay.Run{
		ID:                "eval_1",
		Type:              replay.TypeReplay,
		SourceExecutionID: "exec_1",
		Status:            replay.StatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("CreateEvalRun() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/replays", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list replays status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var runs []replay.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "eval_1" {
		t.Fatalf("runs = %#v, want eval_1", runs)
	}
}

func TestAdminEventsStreamReturnsAuditRecords(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	record := audit.Record{
		ID:        "trace_1",
		Kind:      "proposal.created",
		TraceID:   "trace_1",
		Message:   "queued proposal creation",
		CreatedAt: time.Now().UTC(),
	}
	if err := repo.AppendAuditRecord(context.Background(), record); err != nil {
		t.Fatalf("AppendAuditRecord() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer reqCancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/events/stream", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: proposal.created") {
		t.Fatalf("stream body = %q, want proposal.created event", body)
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

func TestCreateProposalEmitsAuditRecord(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/proposals", strings.NewReader(`{"id":"proposal_1","source_bundle_id":"bundle_a","candidate_bundle_id":"bundle_b"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create proposal status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		records, err := repo.ListAuditRecords(context.Background())
		if err != nil {
			return false
		}
		for _, item := range records {
			if item.Kind == "proposal.created" {
				return true
			}
		}
		return false
	})

	var proposal rollout.Proposal
	if err := json.Unmarshal(rec.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal response: %v", err)
	}
	if proposal.ID != "proposal_1" {
		t.Fatalf("proposal id = %q, want proposal_1", proposal.ID)
	}
}

func TestListModelProvidersReturnsRouterStats(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{})
	_, _ = router.Generate(context.Background(), model.CapabilityReasoning, model.Request{Prompt: "hello"})
	srv := New(":0", repo, writes, sse.NewBroker(), router, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models/providers", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("providers status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Providers []model.ProviderStats `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(payload.Providers) == 0 {
		t.Fatal("providers = empty, want stats")
	}
	var hasSuccess bool
	for _, item := range payload.Providers {
		if item.SuccessCount > 0 {
			hasSuccess = true
			break
		}
	}
	if !hasSuccess {
		t.Fatalf("provider stats = %#v, want at least one provider success after generation", payload.Providers)
	}
}

func TestGetReplayDiffReturnsDecodedStructuredPayload(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateEvalRun(context.Background(), replay.Run{
		ID:                "eval_1",
		Type:              replay.TypeShadow,
		SourceExecutionID: "exec_1",
		Status:            replay.StatusSucceeded,
		ResultJSON:        `{"active":{"bundle_id":"bundle_a"},"shadow":{"bundle_id":"bundle_b"}}`,
		DiffJSON:          `{"tools":{"only_left":["a"],"only_right":["b"]},"composition_mode":{"left":"strict","right":"fluid"}}`,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("CreateEvalRun() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/replays/eval_1/diff", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay diff status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode replay diff: %v", err)
	}
	diff, ok := payload["diff"].(map[string]any)
	if !ok {
		t.Fatalf("payload diff = %#v, want decoded object", payload["diff"])
	}
	if _, ok := diff["composition_mode"]; !ok {
		t.Fatalf("diff = %#v, want composition_mode change", diff)
	}
}

func TestProviderAuthEndpointsRedactSecret(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.RegisterProvider(context.Background(), tool.ProviderBinding{
		ID:           "provider_1",
		Kind:         tool.ProviderMCP,
		Name:         "demo",
		URI:          "http://example.com",
		RegisteredAt: now,
		Healthy:      true,
	}); err != nil {
		t.Fatalf("RegisterProvider() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tools/providers/provider_1/auth", strings.NewReader(`{"type":"bearer","secret":"top-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("save provider auth status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetProviderAuthBinding(context.Background(), "provider_1")
		return err == nil && item.Secret == "top-secret"
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/tools/providers/provider_1/auth", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get provider auth status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode auth payload: %v", err)
	}
	if _, ok := payload["secret"]; ok {
		t.Fatalf("payload = %#v, secret should be redacted", payload)
	}
	if hasSecret, _ := payload["has_secret"].(bool); !hasSecret {
		t.Fatalf("payload = %#v, want has_secret=true", payload)
	}
}

func TestACPSessionEndpointsRoundTrip(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/acp/sessions", strings.NewReader(`{"id":"sess_1","channel":"acp","customer_id":"cust_1","metadata":{"source":"test"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create acp session status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/sess_1/events", strings.NewReader(`{"id":"evt_1","source":"customer","kind":"message","content":[{"type":"text","text":"hello"}],"trace_id":"trace_1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create acp event status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 1
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/events?min_offset=1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list acp events status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var events []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode acp events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("acp events = %#v, want one event", events)
	}
}

func TestSessionEventStreamIncludesPersistedEvent(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "web",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		Offset:    1,
		TraceID:   "trace_1",
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer reqCancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess_1/events/stream", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: session.event.created") || !strings.Contains(body, `"id":"evt_1"`) {
		t.Fatalf("stream body = %q, want persisted session event", body)
	}
}
