package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/acppeer"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	responsedomain "github.com/sahal/parmesan/internal/domain/response"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
	"github.com/sahal/parmesan/internal/model"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

type blockingExecutionRepo struct {
	store.Repository
	delay    time.Duration
	current  atomic.Int32
	max      atomic.Int32
	getCalls atomic.Int32
}

func (r *blockingExecutionRepo) GetExecution(ctx context.Context, executionID string) (execution.TurnExecution, []execution.ExecutionStep, error) {
	r.getCalls.Add(1)
	current := r.current.Add(1)
	for {
		max := r.max.Load()
		if current <= max || r.max.CompareAndSwap(max, current) {
			break
		}
	}
	defer r.current.Add(-1)
	time.Sleep(r.delay)
	return r.Repository.GetExecution(ctx, executionID)
}

func TestRunnerCompletesExecution(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")
	now := time.Now().UTC()
	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:         "bundle_default",
		Version:    "v1",
		ImportedAt: now,
		Soul:       policy.Soul{Identity: "Default"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_1", "ingest", false),
		step("exec_1", "resolve_policy", true),
		step("exec_1", "match_and_plan", true),
		step("exec_1", "compose_response", true),
		step("exec_1", "deliver_response", false),
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_1"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	exec, steps, err := repo.GetExecution(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != execution.StatusSucceeded {
		t.Fatalf("execution status = %s, want %s", exec.Status, execution.StatusSucceeded)
	}
	for _, st := range steps {
		if st.Status != execution.StatusSucceeded {
			t.Fatalf("step %s status = %s, want %s", st.Name, st.Status, execution.StatusSucceeded)
		}
	}

	events, err := repo.ListEvents(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("events len = %d, want at least 2", len(events))
	}
	var hasPolicyResolvedStatus bool
	var hasResponseDeliveredStatus bool
	for _, event := range events {
		if event.Kind != "status" || event.Data == nil {
			continue
		}
		if event.Data["code"] == "policy.resolved" {
			hasPolicyResolvedStatus = true
		}
		if event.Data["code"] == "response.delivered" {
			hasResponseDeliveredStatus = true
		}
	}
	if !hasPolicyResolvedStatus || !hasResponseDeliveredStatus {
		t.Fatalf("events = %#v, want persisted ACP status lifecycle events", events)
	}
}

func TestRunnerProcessesScheduledExecutionsConcurrently(t *testing.T) {
	repo := &blockingExecutionRepo{Repository: memory.New(), delay: 100 * time.Millisecond}
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 2)
	defer writes.Stop()

	r := New(repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), "test-runner").WithExecutionConcurrency(2)
	r.Start(ctx)

	now := time.Now().UTC()
	for _, id := range []string{"exec_1", "exec_2"} {
		if err := repo.CreateExecution(ctx, execution.TurnExecution{
			ID:        id,
			SessionID: "sess_" + id,
			TraceID:   "trace_" + id,
			Status:    execution.StatusSucceeded,
			CreatedAt: now,
			UpdatedAt: now,
		}, nil); err != nil {
			t.Fatalf("CreateExecution(%s) error = %v", id, err)
		}
	}

	if !r.scheduleExecution(ctx, "exec_1") {
		t.Fatal("scheduleExecution(exec_1) = false, want true")
	}
	if !r.scheduleExecution(ctx, "exec_2") {
		t.Fatal("scheduleExecution(exec_2) = false, want true")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.max.Load() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("max concurrent GetExecution calls = %d, want at least 2", repo.max.Load())
}

func TestRunnerDoesNotScheduleSameExecutionTwice(t *testing.T) {
	repo := &blockingExecutionRepo{Repository: memory.New(), delay: 100 * time.Millisecond}
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 2)
	defer writes.Stop()

	r := New(repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), "test-runner").WithExecutionConcurrency(2)
	r.Start(ctx)

	now := time.Now().UTC()
	if err := repo.CreateExecution(ctx, execution.TurnExecution{
		ID:        "exec_1",
		SessionID: "sess_1",
		TraceID:   "trace_1",
		Status:    execution.StatusSucceeded,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil); err != nil {
		t.Fatalf("CreateExecution() error = %v", err)
	}

	if !r.scheduleExecution(ctx, "exec_1") {
		t.Fatal("first scheduleExecution(exec_1) = false, want true")
	}
	if r.scheduleExecution(ctx, "exec_1") {
		t.Fatal("second scheduleExecution(exec_1) = true, want false while first run is in flight")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.getCalls.Load() == 1 && repo.current.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GetExecution calls = %d current=%d, want exactly one completed execution", repo.getCalls.Load(), repo.current.Load())
}

func TestResolveViewUsesPolicySnapshot(t *testing.T) {
	repo := memory.New()
	router := model.NewRouter(config.ProviderConfig{})
	r := New(repo, nil, sse.NewBroker(), router, "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:         "bundle_snapshot_runtime",
		Version:    "v1",
		ImportedAt: now,
		Soul:       policy.Soul{Identity: "Snapshot Agent"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgentProfile(ctx, agent.Profile{
		ID:                    "agent_snapshot_runtime",
		Name:                  "Snapshot Agent",
		Status:                "active",
		DefaultPolicyBundleID: "bundle_snapshot_runtime",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session.Session{
		ID:        "sess_snapshot_runtime",
		Channel:   "web",
		AgentID:   "agent_snapshot_runtime",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_snapshot_runtime",
		SessionID: "sess_snapshot_runtime",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_snapshot_runtime",
		SessionID:      "sess_snapshot_runtime",
		TriggerEventID: "evt_snapshot_runtime",
		TraceID:        "trace_snapshot_runtime",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil); err != nil {
		t.Fatal(err)
	}

	exec, _, err := repo.GetExecution(ctx, "exec_snapshot_runtime")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.resolveView(ctx, exec); err != nil {
		t.Fatalf("resolveView() error = %v", err)
	}
	updated, _, err := repo.GetExecution(ctx, "exec_snapshot_runtime")
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := repo.ListPolicySnapshots(ctx, policy.SnapshotQuery{BundleID: "bundle_snapshot_runtime", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	if updated.PolicySnapshotID != snapshots[0].ID {
		t.Fatalf("execution policy snapshot = %q, want %q", updated.PolicySnapshotID, snapshots[0].ID)
	}
}

func TestSelectPolicySnapshotBundleRequiresSnapshot(t *testing.T) {
	snapshot, bundles, snapshotID := selectPolicySnapshotBundle(nil, "bundle_missing", "")
	if snapshot.ID != "" || len(bundles) != 0 || snapshotID != "" {
		t.Fatalf("snapshot=%#v bundles=%#v snapshotID=%q, want empty result without snapshot fallback", snapshot, bundles, snapshotID)
	}
}

func TestRunnerBlocksForApprovalRequiredTool(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: "http://example.invalid", RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{{ID: "commerce_get_order", ProviderID: "commerce", Name: "get_order", RuntimeProtocol: "mcp", ImportedAt: now}})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "lookup",
			When: "order",
			Then: "Check the order first",
			MCP:  &policy.MCPRef{Server: "commerce", Tool: "get_order"},
		}},
		ToolPolicies: []policy.ToolPolicy{{
			ID:       "commerce_approval",
			ToolIDs:  []string{"commerce.get_order", "commerce_get_order", "get_order"},
			Approval: "required",
		}},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "check my order"}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_1", "ingest", false),
		step("exec_1", "resolve_policy", true),
		step("exec_1", "match_and_plan", true),
		step("exec_1", "compose_response", true),
		step("exec_1", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_1"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	exec, steps, err := repo.GetExecution(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != execution.StatusBlocked {
		t.Fatalf("execution status = %s, want %s", exec.Status, execution.StatusBlocked)
	}
	var blocked bool
	for _, item := range steps {
		if item.Name == "compose_response" && item.Status == execution.StatusBlocked {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("compose_response step was not blocked")
	}
	approvals, err := repo.ListApprovalSessions(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Status != approval.StatusPending {
		t.Fatalf("approvals = %#v, want one pending approval", approvals)
	}
	events, err := repo.ListEvents(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	var hasApprovalRequested bool
	for _, event := range events {
		if event.Kind == "approval.requested" {
			hasApprovalRequested = true
			break
		}
	}
	if !hasApprovalRequested {
		t.Fatalf("events = %#v, want approval.requested session event", events)
	}
}

func TestReuseToolRunReturnsCompletedOutput(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.SaveToolRun(ctx, toolrun.Run{
		ID:             "toolrun_existing",
		ExecutionID:    "exec_1",
		ToolID:         "commerce_get_order",
		Status:         "succeeded",
		IdempotencyKey: "exec_1_commerce_get_order",
		OutputJSON:     `{"order_id":"ord_1","status":"processing"}`,
		CreatedAt:      now,
	})

	output, ok := r.reuseToolRun(ctx, "exec_1", "commerce_get_order", "exec_1_commerce_get_order")
	if !ok {
		t.Fatal("reuseToolRun() = false, want true")
	}
	if output["order_id"] != "ord_1" || output["status"] != "processing" {
		t.Fatalf("output = %#v, want reused tool payload", output)
	}
}

func TestRunnerDoesNotRetryNonRetryableToolFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: server.URL, RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{{ID: "commerce_get_order", ProviderID: "commerce", Name: "get_order", RuntimeProtocol: "mcp", ImportedAt: now}})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "lookup",
			When: "order",
			MCP:  &policy.MCPRef{Server: "commerce", Tool: "get_order"},
		}},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "check my order"}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_1", "ingest", false),
		step("exec_1", "resolve_policy", true),
		step("exec_1", "match_and_plan", true),
		step("exec_1", "compose_response", true),
		step("exec_1", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	err := r.processExecution(ctx, "exec_1")
	if err == nil {
		t.Fatal("processExecution() error = nil, want failure")
	}

	exec, steps, err := repo.GetExecution(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != execution.StatusFailed {
		t.Fatalf("execution status = %s, want %s", exec.Status, execution.StatusFailed)
	}
	for _, item := range steps {
		if item.Name == "compose_response" {
			if item.Attempt != 1 {
				t.Fatalf("compose_response attempts = %d, want 1", item.Attempt)
			}
			if item.Status != execution.StatusFailed {
				t.Fatalf("compose_response status = %s, want %s", item.Status, execution.StatusFailed)
			}
		}
	}
	runs, err := repo.ListToolRuns(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("tool runs = %#v, want one failed run", runs)
	}
}

func TestRunnerSchedulesRetryableToolFailureAsDurableWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"temporary upstream failure"}`))
	}))
	defer server.Close()

	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess_retry", Channel: "web", CreatedAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: server.URL, RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{{ID: "commerce_get_order", ProviderID: "commerce", Name: "get_order", RuntimeProtocol: "mcp", ImportedAt: now}})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "lookup",
			When: "order",
			MCP:  &policy.MCPRef{Server: "commerce", Tool: "get_order"},
		}},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_retry",
		SessionID: "sess_retry",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "check my order"}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_retry",
		SessionID:      "sess_retry",
		TriggerEventID: "evt_retry",
		TraceID:        "trace_retry",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_retry", "ingest", false),
		step("exec_retry", "resolve_policy", true),
		step("exec_retry", "match_and_plan", true),
		step("exec_retry", "compose_response", true),
		step("exec_retry", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_retry"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	exec, steps, err := repo.GetExecution(ctx, "exec_retry")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != execution.StatusWaiting {
		t.Fatalf("execution status = %s, want %s", exec.Status, execution.StatusWaiting)
	}
	if exec.LeaseExpiresAt.IsZero() || !exec.LeaseExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("execution lease_expires_at = %s, want future retry cursor", exec.LeaseExpiresAt)
	}
	for _, item := range steps {
		if item.Name != "compose_response" {
			continue
		}
		if item.Status != execution.StatusWaiting || item.NextAttemptAt.IsZero() || item.RetryReason == "" {
			t.Fatalf("compose step = %#v, want waiting retry metadata", item)
		}
		if item.Attempt != 1 {
			t.Fatalf("compose attempts = %d, want 1", item.Attempt)
		}
		return
	}
	t.Fatal("compose_response step not found")
}

func TestRunnerBlocksToolInvocationWhenArgumentsMissing(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: "http://example.invalid", RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{{
		ID:              "commerce_get_return_status",
		ProviderID:      "commerce",
		Name:            "get_return_status",
		RuntimeProtocol: "mcp",
		Schema:          `{"type":"object","properties":{"return_id":{"type":"string"}},"required":["return_id"]}`,
		ImportedAt:      now,
	}})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "lookup",
			When: "return status",
			Then: "Check the return status first",
		}},
		GuidelineToolAssociations: []policy.GuidelineToolAssociation{
			{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
		},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "what is my return status"}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_1", "ingest", false),
		step("exec_1", "resolve_policy", true),
		step("exec_1", "match_and_plan", true),
		step("exec_1", "compose_response", true),
		step("exec_1", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_1"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	runs, err := repo.ListToolRuns(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("tool runs = %#v, want no invocation because args are missing", runs)
	}
	events, err := repo.ListEvents(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("events len = %d, want assistant response event", len(events))
	}
}

func TestRunnerExecutesMultipleSelectedTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := New(repo, writes, sse.NewBroker(), router, "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: server.URL, RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{
		{ID: "commerce_schedule_appointment", ProviderID: "commerce", Name: "schedule_appointment", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"date":{"type":"string"}}}`, ImportedAt: now},
		{ID: "commerce_send_confirmation_email", ProviderID: "commerce", Name: "send_confirmation_email", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`, ImportedAt: now},
	})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{
			{ID: "schedule_visit", When: "appointment", Then: "schedule the appointment"},
			{ID: "send_confirmation", When: "confirmation email", Then: "send a confirmation email"},
		},
		Relationships: []policy.Relationship{
			{Source: "send_confirmation_email", Kind: "reference", Target: "schedule_appointment"},
		},
		GuidelineToolAssociations: []policy.GuidelineToolAssociation{
			{GuidelineID: "schedule_visit", ToolID: "commerce.schedule_appointment"},
			{GuidelineID: "send_confirmation", ToolID: "commerce.send_confirmation_email"},
		},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "Please schedule an appointment tomorrow at 6pm and send me a confirmation email."}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_1", "ingest", false),
		step("exec_1", "resolve_policy", true),
		step("exec_1", "match_and_plan", true),
		step("exec_1", "compose_response", true),
		step("exec_1", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_1"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	runs, err := repo.ListToolRuns(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("tool runs = %#v, want two tool invocations", runs)
	}
	events, err := repo.ListEvents(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	var started, completed int
	for _, event := range events {
		switch event.Kind {
		case "tool.started":
			started++
		case "tool.completed":
			completed++
		}
	}
	if started != 2 || completed != 2 {
		t.Fatalf("events tool lifecycle = %#v, want two tool.started and two tool.completed", events)
	}
}

func TestDecisionForPlannedCallPreservesFinalizedArguments(t *testing.T) {
	view := resolvedView{
		ToolPlanStage: policyruntime.ToolPlanStageResult{
			Plan: policyruntime.ToolCallPlan{
				Candidates: []policyruntime.ToolCandidate{
					{
						ToolID:        "send_confirmation_email",
						Arguments:     map[string]any{"session_id": "sess_1", "locale": "en"},
						ShouldRun:     true,
						Grounded:      true,
						DecisionState: "selected",
					},
				},
			},
		},
	}
	call := policyruntime.ToolPlannedCall{
		ToolID:    "send_confirmation_email",
		Arguments: map[string]any{},
	}
	decision := decisionForPlannedCall(view, call)
	if decision.Arguments["session_id"] != "sess_1" || decision.Arguments["locale"] != "en" {
		t.Fatalf("planned-call decision args = %#v, want finalized candidate args preserved", decision.Arguments)
	}
}

func TestProcessExecutionCreatesRuntimeAppointmentReminderWatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	r := New(repo, writes, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), "test-runner")

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess_watch", Channel: "web", Status: session.StatusActive, CreatedAt: now, LastActivityAt: now})
	_ = repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: server.URL, RegisteredAt: now, Healthy: true})
	_ = repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{
		{ID: "commerce_schedule_appointment", ProviderID: "commerce", Name: "schedule_appointment", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"date":{"type":"string"}}}`, ImportedAt: now},
	})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_watch",
		Version: "v1",
		Templates: []policy.Template{{
			ID:   "watch_reply",
			Mode: "strict",
			Text: "I can remind you before the appointment.",
		}},
		Guidelines: []policy.Guideline{
			{ID: "schedule_visit", When: "appointment", Then: "schedule the appointment"},
			{ID: "send_reminder", When: "remind me", Then: "set a reminder"},
		},
		GuidelineToolAssociations: []policy.GuidelineToolAssociation{
			{GuidelineID: "schedule_visit", ToolID: "commerce.schedule_appointment"},
		},
	})
	_ = writes.AppendEvent(ctx, session.Event{
		ID:        "evt_watch",
		SessionID: "sess_watch",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "Please schedule an appointment tomorrow at 6pm and remind me about it."}},
	})
	_ = writes.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_watch",
		SessionID:      "sess_watch",
		TriggerEventID: "evt_watch",
		TraceID:        "trace_watch",
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, []execution.ExecutionStep{
		step("exec_watch", "ingest", false),
		step("exec_watch", "resolve_policy", true),
		step("exec_watch", "match_and_plan", true),
		step("exec_watch", "compose_response", true),
		step("exec_watch", "deliver_response", false),
	})

	time.Sleep(50 * time.Millisecond)
	if err := r.processExecution(ctx, "exec_watch"); err != nil {
		t.Fatalf("processExecution() error = %v", err)
	}

	watches, err := repo.ListSessionWatches(ctx, session.WatchQuery{SessionID: "sess_watch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("watches = %#v, want one runtime-created watch", watches)
	}
	if watches[0].Kind != "appointment_reminder" || watches[0].Source != "runtime" {
		t.Fatalf("watch = %#v, want runtime appointment reminder watch", watches[0])
	}
	sess, err := repo.GetSession(ctx, "sess_watch")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != session.StatusSessionKeep {
		t.Fatalf("session status = %s, want session_keep", sess.Status)
	}
}

func TestResolveViewUsesAgentProfileDefaultBundle(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_default",
		Version: "v1",
		Soul: policy.Soul{
			Brand:      "Parmesan",
			Tone:       "calm",
			StyleRules: []string{"use short paragraphs"},
		},
		Guidelines: []policy.Guideline{{ID: "g_default", When: "hello", Then: "reply helpfully"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:         "bundle_other",
		Version:    "v1",
		Guidelines: []policy.Guideline{{ID: "g_other", When: "hello", Then: "reply differently"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgentProfile(ctx, agent.Profile{
		ID:                    "agent_1",
		Name:                  "Support",
		Status:                "active",
		DefaultPolicyBundleID: "bundle_default",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session.Session{
		ID: "sess_1", Channel: "acp", AgentID: "agent_1", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	exec := execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess_1",
		TriggerEventID: "evt_1",
		TraceID:        "trace_1",
		Status:         execution.StatusRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.CreateExecution(ctx, exec, nil); err != nil {
		t.Fatal(err)
	}

	view, _, err := r.resolveView(ctx, exec)
	if err != nil {
		t.Fatalf("resolveView() error = %v", err)
	}
	if view.Bundle.ID != "bundle_default" {
		t.Fatalf("bundle id = %q, want profile default bundle", view.Bundle.ID)
	}
	updated, _, err := repo.GetExecution(ctx, "exec_1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.PolicyBundleID != "bundle_default" {
		t.Fatalf("execution policy bundle = %q, want persisted profile default bundle", updated.PolicyBundleID)
	}
}

func TestComposePromptIncludesSoulGuidance(t *testing.T) {
	prompt := composePrompt(resolvedView{
		Bundle: &policy.Bundle{Soul: policy.Soul{
			Brand:           "Parmesan",
			DefaultLanguage: "en",
			Tone:            "calm",
			StyleRules:      []string{"ask one question at a time"},
			AvoidRules:      []string{"unsupported promises"},
		}},
		CustomerPreferences: []customer.Preference{{
			Key:   "preferred_name",
			Value: "Alex",
		}},
		CustomerContext: map[string]any{
			"name":  "Ada",
			"tier":  "vip",
			"email": "ada@example.com",
		},
		CustomerContextPromptSafeFields: []string{"name", "tier"},
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "Refund and replacement responses must cite kb://returns after verification.",
			ResultHash:  "hash_returns",
			Citations:   []knowledge.Citation{{URI: "kb://returns"}},
		}}},
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Please share the order number before I review refund or replacement options.",
		},
	}, []session.Event{{
		Source:  "customer",
		Kind:    "message",
		Content: []session.ContentPart{{Type: "text", Text: "I need help"}},
	}}, nil)

	if !strings.Contains(prompt, "Agent SOUL style and brand rules:") ||
		!strings.Contains(prompt, "Brand: Parmesan") ||
		!strings.Contains(prompt, "ask one question at a time") ||
		!strings.Contains(prompt, "Avoid rules: unsupported promises") ||
		!strings.Contains(prompt, "Customer preferences (soft constraints):\npreferred_name: Alex") ||
		!strings.Contains(prompt, "Customer context:\nname: Ada\ntier: vip") ||
		strings.Contains(prompt, "ada@example.com") ||
		!strings.Contains(prompt, "Response quality plan:") ||
		!strings.Contains(prompt, `"preference_hints":["preferred_name: Alex"]`) ||
		!strings.Contains(prompt, "High-risk response blueprint:") ||
		!strings.Contains(prompt, "Do not promise eligibility, approval, or timing before verification is complete.") ||
		!strings.Contains(prompt, "High-risk response contract:") ||
		!strings.Contains(prompt, "cite the supporting source identifier or URI") {
		t.Fatalf("prompt = %q, want SOUL style guidance", prompt)
	}
}

func TestResolveViewFiltersCatalogByCapabilityIsolation(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:         "bundle_capability_isolation",
		Version:    "v1",
		ImportedAt: now,
		CapabilityIsolation: policy.CapabilityIsolation{
			AllowedProviderIDs: []string{"commerce"},
			AllowedToolIDs:     []string{"commerce_schedule_appointment"},
		},
		Guidelines: []policy.Guideline{
			{ID: "schedule_visit", When: "appointment", Then: "schedule the appointment"},
			{ID: "check_delivery", When: "delivery", Then: "check the delivery status"},
		},
		GuidelineToolAssociations: []policy.GuidelineToolAssociation{
			{GuidelineID: "schedule_visit", ToolID: "commerce.schedule_appointment"},
			{GuidelineID: "check_delivery", ToolID: "logistics.get_delivery_status"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgentProfile(ctx, agent.Profile{
		ID:                    "agent_capability_isolation",
		Name:                  "Support",
		Status:                "active",
		DefaultPolicyBundleID: "bundle_capability_isolation",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session.Session{
		ID:        "sess_capability_isolation",
		Channel:   "web",
		AgentID:   "agent_capability_isolation",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_capability_isolation",
		SessionID: "sess_capability_isolation",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "Please schedule my appointment and check the delivery status."}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{
		{ID: "commerce_schedule_appointment", ProviderID: "commerce", Name: "schedule_appointment", RuntimeProtocol: "mcp", ImportedAt: now},
		{ID: "logistics_get_delivery_status", ProviderID: "logistics", Name: "get_delivery_status", RuntimeProtocol: "mcp", ImportedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	exec := execution.TurnExecution{
		ID:             "exec_capability_isolation",
		SessionID:      "sess_capability_isolation",
		TriggerEventID: "evt_capability_isolation",
		TraceID:        "trace_capability_isolation",
		Status:         execution.StatusRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.CreateExecution(ctx, exec, nil); err != nil {
		t.Fatal(err)
	}

	view, _, err := r.resolveView(ctx, exec)
	if err != nil {
		t.Fatalf("resolveView() error = %v", err)
	}
	if len(view.ToolExposureStage.ExposedTools) != 1 || view.ToolExposureStage.ExposedTools[0] != "schedule_appointment" {
		t.Fatalf("exposed tools = %#v, want only allowed schedule_appointment", view.ToolExposureStage.ExposedTools)
	}
}

func TestResolveKnowledgeSnapshotSkipsDisallowedProfileDefaultScope(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, sse.NewBroker(), model.NewRouter(config.ProviderConfig{}), "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.SaveKnowledgeSnapshot(ctx, knowledge.Snapshot{
		ID:        "snap_profile_default",
		ScopeKind: "agent",
		ScopeID:   "agent_disallowed_scope",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, chunks := r.resolveKnowledgeSnapshot(ctx, session.Session{
		ID:      "sess_knowledge_isolation",
		Channel: "web",
		AgentID: "agent_knowledge_isolation",
	}, agent.Profile{
		ID:                        "agent_knowledge_isolation",
		DefaultKnowledgeScopeKind: "agent",
		DefaultKnowledgeScopeID:   "agent_disallowed_scope",
	}, []policy.Bundle{{
		ID:      "bundle_knowledge_isolation",
		Version: "v1",
		CapabilityIsolation: policy.CapabilityIsolation{
			AllowedKnowledgeScopes: []policy.KnowledgeScopeRef{{Kind: "agent", ID: "agent_allowed_scope"}},
		},
	}})
	if snapshot != nil || chunks != nil {
		t.Fatalf("snapshot=%#v chunks=%#v, want disallowed profile default scope skipped", snapshot, chunks)
	}
}

func TestCreateAssistantMessageSequenceAddsBatchMetadata(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.CreateSession(ctx, session.Session{ID: "sess_batch", Channel: "acp", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	events, err := r.createAssistantMessageSequence(ctx, execution.TurnExecution{
		ID:        "exec_batch",
		SessionID: "sess_batch",
		TraceID:   "trace_batch",
	}, []string{"First message.", "Second message."})
	if err != nil {
		t.Fatalf("createAssistantMessageSequence() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	batchID, _ := events[0].Metadata["response_batch_id"].(string)
	if strings.TrimSpace(batchID) == "" {
		t.Fatalf("first event metadata = %#v, want response_batch_id", events[0].Metadata)
	}
	for i, event := range events {
		if event.Source != "ai_agent" || event.ExecutionID != "exec_batch" || event.TraceID != "trace_batch" {
			t.Fatalf("event[%d] = %#v, want assistant event tied to execution", i, event)
		}
		if event.Metadata["response_batch_id"] != batchID || event.Metadata["message_index"] != i || event.Metadata["message_count"] != 2 {
			t.Fatalf("event[%d] metadata = %#v", i, event.Metadata)
		}
	}
}

func TestUpdateResponseStatePreservesPriorFields(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()
	record := responsedomain.Response{
		ID:               "resp_1",
		SessionID:        "sess_1",
		ExecutionID:      "exec_1",
		TraceID:          "trace_1",
		Status:           responsedomain.StatusPreparing,
		StartedAt:        now,
		StabilityReached: true,
		GenerationMode:   "canned_composited",
		MessageEventIDs:  []string{"evt_agent_1", "evt_agent_2"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.SaveResponse(ctx, record); err != nil {
		t.Fatal(err)
	}
	r.cacheResponse(record)

	if err := r.updateResponseState(ctx, responsedomain.Response{ID: "resp_1", ExecutionID: "exec_1"}, responsedomain.StatusReady, "", nil); err != nil {
		t.Fatal(err)
	}

	updated, err := repo.GetResponse(ctx, "resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.StartedAt.IsZero() || !updated.StartedAt.Equal(now) {
		t.Fatalf("started_at = %v, want %v", updated.StartedAt, now)
	}
	if !updated.StabilityReached {
		t.Fatal("stability_reached = false, want true")
	}
	if updated.GenerationMode != "canned_composited" {
		t.Fatalf("generation_mode = %q, want canned_composited", updated.GenerationMode)
	}
	if len(updated.MessageEventIDs) != 2 {
		t.Fatalf("message_event_ids = %#v, want preserved ids", updated.MessageEventIDs)
	}
}

func TestEnsureResponseRecordUsesCachedResponseWhenAsyncWritesBuffered(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	r := New(repo, writes, nil, nil, "test-runner")
	ctx := context.Background()
	exec := execution.TurnExecution{
		ID:              "exec_1",
		SessionID:       "sess_1",
		TraceID:         "trace_1",
		TriggerEventIDs: []string{"evt_1"},
	}

	first, err := r.ensureResponseRecord(ctx, exec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.ensureResponseRecord(ctx, exec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("response ids = %q and %q, want same cached response id", first.ID, second.ID)
	}
	items, err := repo.ListResponses(ctx, responsedomain.Query{ExecutionID: exec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("repo responses before async flush = %d, want 0", len(items))
	}
}

func TestMaybeEmitPerceivedPerformanceEmitsPreambleAndStatus(t *testing.T) {
	repo := memory.New()
	r := New(repo, nil, nil, nil, "test-runner")
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.CreateSession(ctx, session.Session{ID: "sess_perf", Channel: "acp", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	record := responsedomain.Response{
		ID:          "resp_perf",
		SessionID:   "sess_perf",
		ExecutionID: "exec_perf",
		TraceID:     "trace_perf",
		Status:      responsedomain.StatusProcessing,
		StartedAt:   now.Add(-time.Second),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.SaveResponse(ctx, record); err != nil {
		t.Fatal(err)
	}
	r.cacheResponse(record)
	view := resolvedView{
		Bundle: &policy.Bundle{
			PerceivedPerformance: policy.PerceivedPerformancePolicy{
				Mode:                    "smart",
				ProcessingIndicator:     true,
				PreambleEnabled:         true,
				PreambleDelayMS:         0,
				ProcessingUpdateDelayMS: 0,
				Preambles:               []string{"Checking that now."},
			},
		},
	}
	exec := execution.TurnExecution{
		ID:        "exec_perf",
		SessionID: "sess_perf",
		TraceID:   "trace_perf",
	}

	if err := r.maybeEmitPerceivedPerformance(ctx, exec, record, view); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetResponse(ctx, "resp_perf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(updated.PreambleEventID) == "" {
		t.Fatal("preamble_event_id is empty")
	}
	events, err := repo.ListEvents(ctx, "sess_perf")
	if err != nil {
		t.Fatal(err)
	}
	var hasPreamble bool
	var hasProcessingStatus bool
	for _, event := range events {
		if event.Source == "ai_agent" && event.ID == updated.PreambleEventID {
			hasPreamble = true
		}
		if event.Kind == "status" && event.Data["code"] == "response.processing" {
			hasProcessingStatus = true
		}
	}
	if !hasPreamble || !hasProcessingStatus {
		t.Fatalf("events = %#v, want preamble and processing status", events)
	}
}

func TestRunnerDelegatesToExternalAgentPeer(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_RUNNER_ACP_HELPER") == "1" {
		runRunnerACPHelperProcess()
		return
	}

	repo := memory.New()
	r := New(repo, nil, nil, nil, "test-runner").WithAgentPeers(acppeer.NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestRunnerDelegatesToExternalAgentPeer"},
			Env: map[string]string{
				"PARMESAN_TEST_RUNNER_ACP_HELPER": "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				Model:        "anthropic/claude-3.7-sonnet",
				PromptPrefix: "Prefix instruction.",
				PromptSuffix: "Suffix instruction.",
				MCPServers: []config.ACPMCPServerConfig{
					{Type: "stdio", Name: "Repo Tools", Command: "npx", Args: []string{"-y", "@acme/repo-mcp"}, Env: map[string]string{"REPO_TOKEN": "secret"}},
				},
			},
		},
	}))

	now := time.Now().UTC()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID:        "sess_delegate",
		Channel:   "acp",
		AgentID:   "parent_agent",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(context.Background(), session.Event{
		ID:        "evt_1",
		SessionID: "sess_delegate",
		Source:    "customer",
		Kind:      "message",
		TraceID:   "trace_1",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "Please orchestrate the workflow."}},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	output, err := r.maybeRunCapability(context.Background(), execution.TurnExecution{
		ID:        "exec_delegate",
		SessionID: "sess_delegate",
		TraceID:   "trace_1",
	}, resolvedView{
		AgentDecisionStage: policyruntime.AgentDecisionStageResult{
			Decision: policyruntime.AgentDecision{
				SelectedAgent: "OpenCode",
				CanRun:        true,
				Rationale:     "delegate",
				Grounded:      true,
			},
		},
		CapabilityDecisionStage: policyruntime.CapabilityDecisionStageResult{
			Decision: policyruntime.CapabilityDecision{
				Kind:     "agent",
				TargetID: "OpenCode",
			},
		},
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{ID: "delegate", Then: "delegate the workflow"}},
		},
	})
	if err != nil {
		t.Fatalf("maybeRunCapability() error = %v", err)
	}
	delegated, ok := output["delegated_agent"].(map[string]any)
	if !ok {
		t.Fatalf("output = %#v, want delegated_agent payload", output)
	}
	if delegated["result_text"] != "Delegated runner answer" {
		t.Fatalf("delegated output = %#v, want delegated answer text", delegated)
	}
	if delegated["model"] != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("delegated output = %#v, want delegated model", delegated)
	}
	if mcp, ok := delegated["mcp_servers"].([]string); !ok || len(mcp) != 1 || mcp[0] != "Repo Tools" {
		t.Fatalf("delegated output = %#v, want delegated MCP server names", delegated)
	}
	if delegated["prompt_prefix_applied"] != true || delegated["prompt_suffix_applied"] != true {
		t.Fatalf("delegated output = %#v, want prompt flags", delegated)
	}
}

func runRunnerACPHelperProcess() {
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id, _ := msg["id"].(float64)
		switch method {
		case "initialize":
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "id": int(id), "result": map[string]any{
				"agentCapabilities": map[string]any{
					"mcpCapabilities": map[string]any{"http": true, "sse": true},
				},
			}})
		case "session/new":
			params, _ := msg["params"].(map[string]any)
			mcpServers, _ := params["mcpServers"].([]any)
			if len(mcpServers) != 1 {
				panic("expected one MCP server")
			}
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "id": int(id), "result": map[string]any{
				"ok": true,
				"configOptions": []map[string]any{
					{
						"configId": "model",
						"category": "model",
						"options": []map[string]any{
							{"value": "anthropic/claude-3.7-sonnet", "label": "anthropic/claude-3.7-sonnet"},
						},
					},
				},
			}})
		case "session/set_config_option":
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "id": int(id), "result": map[string]any{"ok": true}})
		case "session/prompt":
			params, _ := msg["params"].(map[string]any)
			sessionID, _ := params["sessionId"].(string)
			prompt, _ := params["prompt"].([]any)
			if len(prompt) != 1 {
				panic("expected one prompt block")
			}
			block, _ := prompt[0].(map[string]any)
			text, _ := block["text"].(string)
			if !strings.Contains(text, "Prefix instruction.") || !strings.Contains(text, "Suffix instruction.") {
				panic("expected injected prompt text")
			}
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "id": int(id), "result": map[string]any{"ok": true}})
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]any{"type": "agent_message_chunk", "text": "Delegated runner answer"}}})
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]any{"type": "agent_turn_complete"}}})
		default:
			writeRunnerHelperJSON(writer, map[string]any{"jsonrpc": "2.0", "id": int(id), "error": map[string]any{"code": -32601, "message": "unsupported"}})
		}
	}
}

func writeRunnerHelperJSON(writer *bufio.Writer, value map[string]any) {
	raw, _ := json.Marshal(value)
	_, _ = writer.Write(append(raw, '\n'))
	_ = writer.Flush()
}

func step(execID, name string, recomputable bool) execution.ExecutionStep {
	now := time.Now().UTC()
	return execution.ExecutionStep{
		ID:             execID + "_" + name,
		ExecutionID:    execID,
		Name:           name,
		Status:         execution.StatusPending,
		Recomputable:   recomputable,
		IdempotencyKey: execID + "_" + name,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}
