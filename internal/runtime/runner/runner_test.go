package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	"github.com/sahal/parmesan/internal/model"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

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

	if err := repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := writes.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: time.Now().UTC(),
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
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
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
}

func TestDecisionForPlannedCallPreservesFinalizedArguments(t *testing.T) {
	view := resolvedView{
		ToolPlan: policyruntime.ToolCallPlan{
			Candidates: []policyruntime.ToolCandidate{
				{
					ToolID:     "send_confirmation_email",
					Arguments:  map[string]any{"session_id": "sess_1", "locale": "en"},
					ShouldRun:  true,
					Grounded:   true,
					DecisionState: "selected",
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
