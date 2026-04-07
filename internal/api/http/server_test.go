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
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
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

func TestACPListEventsExcludesDeletedByDefault(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "acp",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_live",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "message",
		Offset:    10,
		TraceID:   "trace_1",
		Content:   []session.ContentPart{{Type: "text", Text: "visible"}},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendEvent(live) error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_deleted",
		SessionID: "sess_1",
		Source:    "assistant",
		Kind:      "message",
		Offset:    11,
		TraceID:   "trace_1",
		Deleted:   true,
		Content:   []session.ContentPart{{Type: "text", Text: "hidden"}},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendEvent(deleted) error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/events", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list acp events status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var events []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode acp events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("acp events = %#v, want exactly one visible event", events)
	}
	if got := events[0]["id"]; got != "evt_live" {
		t.Fatalf("visible event id = %#v, want evt_live", got)
	}
}

func TestACPAppendEventRejectsInvalidTypedPayload(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "acp",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/sess_1/events", strings.NewReader(`{"id":"evt_1","source":"runtime","kind":"tool.failed","data":{"tool_id":"tool_1"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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

func TestACPStreamNormalizesLegacyApprovalResultKind(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "acp",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "gateway",
		Kind:      "approval_result",
		Offset:    1,
		TraceID:   "trace_1",
		Content:   []session.ContentPart{{Type: "text", Text: "approve"}},
		Metadata:  map[string]any{"approval_id": "appr_1", "tool_id": "tool_1"},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer reqCancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/events/stream", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: approval.resolved") || !strings.Contains(body, `"decision":"approve"`) {
		t.Fatalf("stream body = %q, want normalized ACP approval event", body)
	}
}

func TestGetSessionReturnsTypedSummary(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "web",
		Metadata:  map[string]any{"last_trace_id": "trace_1", "applied_guideline_ids": []any{"g1", "g2"}, "active_journey_id": "journey_1", "active_journey_state_id": "state_1", "composition_mode": "strict"},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.CreateExecution(context.Background(), execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess_1",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusSucceeded,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil); err != nil {
		t.Fatalf("CreateExecution() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess_1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get session status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	summary, ok := payload["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %#v, want object", payload["summary"])
	}
	if summary["last_trace_id"] != "trace_1" || summary["last_execution_id"] != "exec_1" {
		t.Fatalf("summary = %#v, want trace/execution summary", summary)
	}
}

func TestACPGetSessionReturnsTypedSummary(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "acp",
		Metadata:  map[string]any{"last_trace_id": "trace_1", "composition_mode": "strict"},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get acp session status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode acp session: %v", err)
	}
	summary, ok := payload["summary"].(map[string]any)
	if !ok || summary["last_trace_id"] != "trace_1" {
		t.Fatalf("summary = %#v, want typed ACP summary", payload["summary"])
	}
}

func TestGetSessionFallsBackToLatestExecutionWhenLastTraceIsStale(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_1",
		Channel:   "web",
		Metadata:  map[string]any{"last_trace_id": "trace_stale"},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.CreateExecution(context.Background(), execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess_1",
		TriggerEventID: "evt_1",
		TraceID:        "trace_real",
		Status:         execution.StatusSucceeded,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil); err != nil {
		t.Fatalf("CreateExecution() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess_1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get session status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	summary, ok := payload["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %#v, want object", payload["summary"])
	}
	if summary["last_execution_id"] != "exec_1" {
		t.Fatalf("summary = %#v, want fallback latest execution id", summary)
	}
}

func TestACPAndSessionStreamsIncludeResponseDelta(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	broker := sse.NewBroker()
	srv := New(":0", repo, writes, broker, model.NewRouter(config.ProviderConfig{}), nil)

	for _, path := range []string{"/v1/sessions/sess_1/events/stream", "/v1/acp/sessions/sess_1/events/stream"} {
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(reqCtx)
		rec := httptest.NewRecorder()
		go func() {
			time.Sleep(20 * time.Millisecond)
			broker.Publish("sess_1", sse.Envelope{
				EventID:     "stream_1",
				SessionID:   "sess_1",
				ExecutionID: "exec_1",
				TraceID:     "trace_1",
				Type:        "runtime.response.delta",
				Payload:     map[string]any{"text": "hello"},
				CreatedAt:   time.Now().UTC(),
			})
			time.Sleep(10 * time.Millisecond)
			reqCancel()
		}()
		srv.httpServer.Handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, "event: response.delta") || !strings.Contains(body, `"text":"hello"`) {
			t.Fatalf("%s body = %q, want streamed response delta", path, body)
		}
	}
}

func TestACPMessageIngressCreatesExecutionAndTriggerEvent(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/sess_1/messages", strings.NewReader(`{"id":"evt_1","text":"hello from acp"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 1 && events[0].ID == "evt_1"
	})

	execs, err := repo.ListExecutions(context.Background())
	if err != nil {
		t.Fatalf("ListExecutions() error = %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("executions = %#v, want exactly one execution", execs)
	}
	if execs[0].TriggerEventID != "evt_1" {
		t.Fatalf("TriggerEventID = %q, want %q", execs[0].TriggerEventID, "evt_1")
	}
}

func TestACPMessageIngressMissingSessionReturnsNotFound(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/missing/messages", strings.NewReader(`{"text":"hello from acp"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestACPMessageIngressManualModeStoresEventWithoutExecution(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", Mode: "manual", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/sess_1/messages", strings.NewReader(`{"id":"evt_1","text":"hold for operator"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 1 && events[0].ID == "evt_1" && events[0].Metadata["automation_skipped"] == true
	})
	execs, err := repo.ListExecutions(context.Background())
	if err != nil {
		t.Fatalf("ListExecutions() error = %v", err)
	}
	if len(execs) != 0 {
		t.Fatalf("executions = %#v, want none while session is manual", execs)
	}
}

func TestOperatorEndpointsRequireTokenWhenConfigured(t *testing.T) {
	t.Setenv("OPERATOR_API_KEY", "secret-operator-token")
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/operator/sessions", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth operator status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/operator/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret-operator-token")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth operator status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ACP status = %d, want %d without operator token body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestOperatorSessionListFiltersByOperatorAndActiveState(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	now := time.Now().UTC()
	sessions := []session.Session{
		{
			ID: "sess_1", Channel: "acp", CustomerID: "cust_1", AgentID: "agent_1", Mode: "manual", Labels: []string{"vip"}, CreatedAt: now,
			Metadata: map[string]any{"assigned_operator_id": "op_1"},
		},
		{
			ID: "sess_2", Channel: "acp", CustomerID: "cust_2", AgentID: "agent_1", Mode: "auto", CreatedAt: now.Add(time.Second),
			Metadata: map[string]any{},
		},
	}
	for _, sess := range sessions {
		if err := repo.CreateSession(context.Background(), sess); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", sess.ID, err)
		}
	}
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "operator", path: "/v1/operator/sessions?operator_id=op_1", want: "sess_1"},
		{name: "active", path: "/v1/operator/sessions?active=true", want: "sess_1"},
		{name: "limit", path: "/v1/operator/sessions?limit=1", want: "sess_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var got []sessionView
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode sessions: %v", err)
			}
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("sessions = %#v, want only %s", got, tc.want)
			}
		})
	}
}

func TestOperatorListEventsSupportsCursorFiltersAndLimit(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	for _, event := range []session.Event{
		{ID: "evt_1", SessionID: "sess_1", Source: "customer", Kind: "message", TraceID: "trace_1", Offset: 1, CreatedAt: now},
		{ID: "evt_2", SessionID: "sess_1", Source: "operator", Kind: "operator.note", TraceID: "trace_2", Offset: 2, CreatedAt: now.Add(time.Second)},
		{ID: "evt_3", SessionID: "sess_1", Source: "operator", Kind: "operator.note", TraceID: "trace_2", Offset: 3, CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := repo.AppendEvent(context.Background(), event); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", event.ID, err)
		}
	}
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/operator/sessions/sess_1/events?min_offset=2&source=operator&kind=operator.note&trace_id=trace_2&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var events []session.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_2" {
		t.Fatalf("events = %#v, want limited filtered evt_2", events)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/operator/sessions/sess_1/events?limit=0", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestOperatorTakeoverResumeAndExplicitProcess(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: now, Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1", Source: "customer", Kind: "message", TraceID: "trace_1", Offset: 1, CreatedAt: now,
		Content: []session.ContentPart{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/takeover", strings.NewReader(`{"operator_id":"op_1","reason":"customer requested human"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("takeover status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	sess, err := repo.GetSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if sess.Mode != "manual" || sess.Metadata["assigned_operator_id"] != "op_1" {
		t.Fatalf("session after takeover = %#v, want manual with assignment", sess)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/process", strings.NewReader(`{"event_id":"evt_1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("process status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, time.Second, func() bool {
		execs, err := repo.ListExecutions(context.Background())
		return err == nil && len(execs) == 1 && execs[0].TriggerEventID == "evt_1"
	})

	req = httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/resume", strings.NewReader(`{"operator_id":"op_1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	sess, err = repo.GetSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if sess.Mode != "auto" || sess.Metadata["assigned_operator_id"] != nil {
		t.Fatalf("session after resume = %#v, want auto without assignment", sess)
	}
}

func TestOperatorTakeoverAppearsAsOperatorTraceEntry(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: time.Now().UTC(), Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/takeover", strings.NewReader(`{"operator_id":"op_1","reason":"customer requested human"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("takeover status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var traceID string
	waitFor(t, time.Second, func() bool {
		records, err := repo.ListAuditRecords(context.Background())
		if err != nil {
			return false
		}
		for _, record := range records {
			if record.Kind == "operator.takeover.started" {
				traceID = record.TraceID
				return traceID != ""
			}
		}
		return false
	})
	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 1 && events[0].Kind == "operator.takeover.started" && events[0].TraceID == traceID
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/traces/"+traceID, nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace timeline status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload traceTimelineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode trace timeline: %v", err)
	}
	foundAudit := false
	foundOperatorEvent := false
	for _, entry := range payload.Entries {
		if entry.Kind == "audit.operator.takeover.started" {
			foundAudit = true
		}
		if entry.Kind == "operator.takeover.started" {
			foundOperatorEvent = true
		}
	}
	if !foundAudit || !foundOperatorEvent {
		t.Fatalf("timeline entries = %#v, want audit and operator takeover entries", payload.Entries)
	}
}

func TestOperatorNotesAreHiddenFromACPButVisibleToOperator(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/notes", strings.NewReader(`{"operator_id":"op_1","text":"internal note"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("note status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 1
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/events", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ACP list status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var acpEvents []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &acpEvents); err != nil {
		t.Fatalf("decode ACP events: %v", err)
	}
	if len(acpEvents) != 0 {
		t.Fatalf("ACP events = %#v, want operator note hidden", acpEvents)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/operator/sessions/sess_1/messages", strings.NewReader(`{"operator_id":"op_1","display_name":"Operator","text":"I can help from here"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("operator message status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		return err == nil && len(events) == 2
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/events", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ACP list status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &acpEvents); err != nil {
		t.Fatalf("decode ACP events after operator message: %v", err)
	}
	if len(acpEvents) != 1 || acpEvents[0]["source"] != "human_agent" {
		t.Fatalf("ACP events = %#v, want visible human_agent message only", acpEvents)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/operator/sessions/sess_1/events", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator list status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var operatorEvents []session.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &operatorEvents); err != nil {
		t.Fatalf("decode operator events: %v", err)
	}
	if len(operatorEvents) != 2 || operatorEvents[0].Kind != "operator.note" || operatorEvents[1].Source != "human_agent" {
		t.Fatalf("operator events = %#v, want operator note and human-agent message visible", operatorEvents)
	}
}

func TestACPApprovalEndpointsListPendingAndResolve(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: "sess_1", Channel: "acp", CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.CreateExecution(context.Background(), execution.TurnExecution{
		ID: "exec_1", SessionID: "sess_1", TriggerEventID: "evt_1", TraceID: "trace_1", Status: execution.StatusBlocked, CreatedAt: now, UpdatedAt: now,
	}, []execution.ExecutionStep{}); err != nil {
		t.Fatalf("CreateExecution() error = %v", err)
	}
	for _, item := range []approval.Session{
		{ID: "approval_1", SessionID: "sess_1", ExecutionID: "exec_1", ToolID: "tool_1", Status: approval.StatusPending, RequestText: "approve", CreatedAt: now, UpdatedAt: now},
		{ID: "approval_2", SessionID: "sess_1", ExecutionID: "exec_1", ToolID: "tool_2", Status: approval.StatusApproved, RequestText: "approve", Decision: "approve", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repo.SaveApprovalSession(context.Background(), item); err != nil {
			t.Fatalf("SaveApprovalSession() error = %v", err)
		}
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/approvals", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list approvals status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var listed []approval.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed approvals = %#v, want both session approvals", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/acp/sessions/sess_1/approvals?status=pending", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered approvals status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var pending []approval.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode filtered approvals: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "approval_1" {
		t.Fatalf("pending approvals = %#v, want only pending item", pending)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/acp/sessions/sess_1/approvals/approval_1", strings.NewReader(`{"decision":"approve"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve approval status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	waitFor(t, time.Second, func() bool {
		item, err := repo.GetApprovalSession(context.Background(), "approval_1")
		return err == nil && item.Status == approval.StatusApproved
	})
	exec, _, err := repo.GetExecution(context.Background(), "exec_1")
	if err != nil {
		t.Fatalf("GetExecution() error = %v", err)
	}
	if exec.Status != execution.StatusPending {
		t.Fatalf("execution status = %q, want %q", exec.Status, execution.StatusPending)
	}
	waitFor(t, time.Second, func() bool {
		events, err := repo.ListEvents(context.Background(), "sess_1")
		if err != nil {
			return false
		}
		for _, event := range events {
			if event.Kind == "approval.resolved" {
				return true
			}
		}
		return false
	})
}

func TestTraceTimelineIncludesCrossArtifactEntries(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{ID: "sess_1", Channel: "web", CreatedAt: now}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID: "evt_1", SessionID: "sess_1", Source: "customer", Kind: "message", Offset: 1, TraceID: "trace_1", CreatedAt: now,
		Content: []session.ContentPart{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := repo.CreateExecution(context.Background(), execution.TurnExecution{
		ID: "exec_1", SessionID: "sess_1", TriggerEventID: "evt_1", TraceID: "trace_1", Status: execution.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}, []execution.ExecutionStep{{
		ID: "step_1", ExecutionID: "exec_1", Name: "resolve_policy", Status: execution.StatusSucceeded, CreatedAt: now, UpdatedAt: now,
	}}); err != nil {
		t.Fatalf("CreateExecution() error = %v", err)
	}
	if err := repo.SaveApprovalSession(context.Background(), approval.Session{
		ID: "approval_1", SessionID: "sess_1", ExecutionID: "exec_1", ToolID: "tool_1", Status: approval.StatusPending, RequestText: "approve", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveApprovalSession() error = %v", err)
	}
	if err := repo.SaveToolRun(context.Background(), toolrun.Run{
		ID: "run_1", ExecutionID: "exec_1", ToolID: "tool_1", Status: "succeeded", CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveToolRun() error = %v", err)
	}
	if err := repo.SaveDeliveryAttempt(context.Background(), delivery.Attempt{
		ID: "delivery_1", SessionID: "sess_1", ExecutionID: "exec_1", EventID: "evt_ai", Channel: "web", Status: "queued", CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDeliveryAttempt() error = %v", err)
	}
	if err := repo.AppendAuditRecord(context.Background(), audit.Record{
		ID: "audit_1", Kind: "policy.resolved", SessionID: "sess_1", ExecutionID: "exec_1", TraceID: "trace_1", Message: "resolved", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendAuditRecord() error = %v", err)
	}

	srv := New(":0", repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/traces/trace_1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace timeline status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode trace timeline: %v", err)
	}
	entries, ok := payload["entries"].([]any)
	if !ok || len(entries) < 5 {
		t.Fatalf("entries = %#v, want timeline entries", payload["entries"])
	}
}
