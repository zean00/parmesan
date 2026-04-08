package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
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
		!strings.Contains(prompt, "Response quality plan:") ||
		!strings.Contains(prompt, `"preference_hints":["preferred_name: Alex"]`) ||
		!strings.Contains(prompt, "High-risk response blueprint:") ||
		!strings.Contains(prompt, "Do not promise eligibility, approval, or timing before verification is complete.") ||
		!strings.Contains(prompt, "High-risk response contract:") ||
		!strings.Contains(prompt, "cite the supporting source identifier or URI") {
		t.Fatalf("prompt = %q, want SOUL style guidance", prompt)
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
