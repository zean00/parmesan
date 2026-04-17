package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	maintainerdomain "github.com/sahal/parmesan/internal/domain/maintainer"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/lifecycle"
	maintainerworker "github.com/sahal/parmesan/internal/maintainer"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/quality"
	"github.com/sahal/parmesan/internal/engine/runner"
	"github.com/sahal/parmesan/internal/sessionwatch"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestPlatformValidationDurableApprovalResumeFlow(t *testing.T) {
	var toolCallCount int
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		toolCallCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"order_id":"ord_123","status":"approved_for_review"}}`))
	}))
	defer toolServer.Close()

	repo := memory.New()
	writes := asyncwrite.New(repo, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	broker := sse.NewBroker()
	router := model.NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		OpenRouterBase:    "https://openrouter.ai/api/v1",
	})
	r := runner.New(repo, writes, broker, router, "durable-approval-test-worker")
	r.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)

	now := time.Now().UTC()
	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:      "approval_bundle",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "order_lookup",
			When: "customer asks to check an order",
			Then: "Check the order before responding.",
			MCP:  &policy.MCPRef{Server: "commerce", Tool: "get_order"},
		}},
		ToolPolicies: []policy.ToolPolicy{{
			ID:       "commerce_approval",
			ToolIDs:  []string{"commerce.get_order", "commerce_get_order", "get_order"},
			Approval: "required",
		}},
		ImportedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgentProfile(ctx, agent.Profile{
		ID:                    "agent_approval",
		Name:                  "Approval Agent",
		Status:                "active",
		DefaultPolicyBundleID: "approval_bundle",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterProvider(ctx, tool.ProviderBinding{ID: "commerce", Kind: tool.ProviderMCP, Name: "commerce", URI: toolServer.URL, Healthy: true, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCatalogEntries(ctx, []tool.CatalogEntry{{
		ID:              "commerce_get_order",
		ProviderID:      "commerce",
		Name:            "get_order",
		Description:     "Get order details.",
		RuntimeProtocol: "mcp",
		ImportedAt:      now,
	}}); err != nil {
		t.Fatal(err)
	}

	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_durable_approval",
		"channel":"acp",
		"agent_id":"agent_approval",
		"customer_id":"cust_approval"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_durable_approval/messages", `{
		"id":"evt_durable_approval",
		"text":"Please check my order ord_123"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	exec := waitForExecutionStatusAllowBlocked(t, repo, "sess_durable_approval", execution.StatusBlocked, validationTimeout(4*time.Second))
	if exec.BlockedReason != execution.BlockedReasonApprovalRequired || exec.ResumeSignal != execution.ResumeSignalApproval {
		t.Fatalf("blocked execution = %#v, want approval resume metadata", exec)
	}
	if toolCallCount != 0 {
		t.Fatalf("toolCallCount = %d, want no tool invocation before approval", toolCallCount)
	}
	approvals := waitForPendingApprovals(t, repo, "sess_durable_approval", validationTimeout(2*time.Second))
	if len(approvals) != 1 {
		t.Fatalf("approvals = %#v, want one pending approval", approvals)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/acp/sessions/sess_durable_approval/approvals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list approvals status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var listed []approval.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != approvals[0].ID || listed[0].Status != approval.StatusPending {
		t.Fatalf("listed approvals = %#v, want pending approval %s", listed, approvals[0].ID)
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_durable_approval/approvals/"+approvals[0].ID, `{"decision":"approve"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	exec = waitForExecutionStatus(t, repo, "sess_durable_approval", execution.StatusSucceeded, validationTimeout(6*time.Second))
	if exec.ID == "" {
		t.Fatal("missing resumed execution")
	}
	if toolCallCount != 1 {
		t.Fatalf("toolCallCount = %d, want one tool invocation after approval", toolCallCount)
	}
	runs, err := repo.ListToolRuns(context.Background(), exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("tool runs = %#v, want one succeeded tool run", runs)
	}
	_ = waitForAssistantText(t, repo, "sess_durable_approval", validationTimeout(2*time.Second))
	events, err := repo.ListEvents(context.Background(), "sess_durable_approval")
	if err != nil {
		t.Fatal(err)
	}
	var hasApprovalResolved, hasToolCompleted bool
	for _, event := range events {
		switch event.Kind {
		case "approval.resolved":
			hasApprovalResolved = true
		case "tool.completed":
			hasToolCompleted = true
		}
	}
	if !hasApprovalResolved || !hasToolCompleted {
		t.Fatalf("events = %#v, want approval.resolved and tool.completed", events)
	}
}

func TestPlatformValidationRuntimeWatchBeatsLifecycleFallback(t *testing.T) {
	t.Setenv("SESSION_IDLE_CANDIDATE_AFTER", "100ms")
	t.Setenv("SESSION_KEEP_RECHECK_AFTER", "100ms")

	repo := memory.New()
	writes := asyncwrite.New(repo, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	lr := lifecycle.New(repo, writes, nil)

	now := time.Now().UTC().Add(-2 * time.Second)
	if err := repo.CreateSession(ctx, session.Session{
		ID:             "sess_runtime_watch",
		Channel:        "acp",
		CustomerID:     "cust_watch",
		Status:         session.StatusActive,
		CreatedAt:      now,
		LastActivityAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_runtime_watch",
		SessionID: "sess_runtime_watch",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content: []session.ContentPart{{
			Type: "text",
			Text: "Please remind me about my appointment tomorrow at 6pm.",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(ctx, "sess_runtime_watch")
	if err != nil {
		t.Fatal(err)
	}
	appointmentAt := time.Date(now.Year(), now.Month(), now.Day()+1, 18, 0, 0, 0, time.UTC)
	intent, ok := sessionwatch.BuildAppointmentReminderIntent(sessionwatch.SourceRuntime, appointmentAt.Format(time.RFC3339), sessionwatch.ReminderTimeFromAppointment(appointmentAt, now), map[string]any{
		"appointment_at": appointmentAt.Format(time.RFC3339),
	}, now)
	if !ok {
		t.Fatal("expected runtime appointment reminder intent")
	}
	if _, created, err := sessionwatch.EnsureSessionWatch(ctx, repo, sess, intent, now); err != nil {
		t.Fatal(err)
	} else if !created {
		t.Fatal("expected runtime watch creation")
	}
	sess.Status = session.StatusSessionKeep
	sess.KeepReason = "appointment_reminder"
	sess.LastActivityAt = now
	if err := repo.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	lr.Start(ctx)

	waitFor(t, validationTimeout(2*time.Second), func() bool {
		items, err := repo.ListSessionWatches(ctx, session.WatchQuery{SessionID: "sess_runtime_watch"})
		if err != nil || len(items) != 1 {
			return false
		}
		return items[0].Source == "runtime"
	})
	watches, err := repo.ListSessionWatches(ctx, session.WatchQuery{SessionID: "sess_runtime_watch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("watches = %#v, want lifecycle fallback to avoid duplicate runtime watch", watches)
	}
}

func TestPlatformValidationLifecycleCreatesFallbackWatchWhenRuntimeDidNot(t *testing.T) {
	t.Setenv("SESSION_IDLE_CANDIDATE_AFTER", "100ms")
	t.Setenv("SESSION_KEEP_RECHECK_AFTER", "100ms")

	repo := memory.New()
	writes := asyncwrite.New(repo, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	lr := lifecycle.New(repo, writes, nil)
	lr.Start(ctx)

	now := time.Now().UTC().Add(-2 * time.Second)
	if err := repo.CreateSession(ctx, session.Session{
		ID:             "sess_lifecycle_watch",
		Channel:        "acp",
		CustomerID:     "cust_watch_2",
		Status:         session.StatusActive,
		CreatedAt:      now,
		LastActivityAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_lifecycle_watch",
		SessionID: "sess_lifecycle_watch",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content: []session.ContentPart{{
			Type: "text",
			Text: "Please remind me about my appointment tomorrow at 6pm.",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	watches := waitForSessionWatchCount(t, repo, "sess_lifecycle_watch", 1, validationTimeout(2*time.Second))
	if watches[0].Kind != "appointment_reminder" || watches[0].Source != "lifecycle" {
		t.Fatalf("watch = %#v, want lifecycle-created appointment reminder watch", watches[0])
	}
	sess, err := repo.GetSession(ctx, "sess_lifecycle_watch")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != session.StatusSessionKeep {
		t.Fatalf("session status = %s, want session_keep", sess.Status)
	}
	time.Sleep(1200 * time.Millisecond)
	watches, err = repo.ListSessionWatches(ctx, session.WatchQuery{SessionID: "sess_lifecycle_watch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("watches = %#v, want lifecycle fallback to dedupe appointment reminder watch", watches)
	}
}

func TestPlatformValidationEcommerceLifecycle(t *testing.T) {
	repo := memory.New()
	var router *model.Router
	sessionIDs := []string{}
	defer func() {
		writePlatformValidationReport(t, repo, router, t.Name(), "ecommerce_supervised_learning", "agent_storefront", "cust_1", sessionIDs)
	}()
	writes := asyncwrite.New(repo, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	broker := sse.NewBroker()
	router = model.NewRouter(config.Load("api").Provider)
	r := runner.New(repo, writes, broker, router, "test-worker")
	r.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)

	root := t.TempDir()
	t.Setenv("KNOWLEDGE_SOURCE_ROOT", root)
	docsDir := filepath.Join(root, "storefront-docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeDoc := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(docsDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	writeDoc("returns.md", "# Returns\nDamaged appliances can be reviewed for refund or replacement after order verification.")
	writeDoc("damaged-electronics.md", "# Damaged Electronics\nCustomers with damaged electronics should provide the order number and describe the defect before a resolution is promised.")
	writeDoc("shipping.md", "# Notifications\nCustomers who prefer email updates should receive shipment notifications by email.")

	const policyYAML = `
id: ecommerce_support
version: v1
no_match: I need to hand this to a specialist because I do not have an approved response for this case yet.
soul:
  identity: Storefront Support
  role: Customer support agent
  brand: Acme Store
  default_language: en
  supported_languages:
    - en
    - id
  language_matching: mirror the customer's language when possible
  tone: calm
  formality: semi-formal
  verbosity: concise
  style_rules:
    - use short practical paragraphs
  avoid_rules:
    - do not promise refunds before verification
  escalation_style: explain the next step clearly
  formatting_rules:
    - ask one question at a time
guidelines:
  - id: damaged_order_ack
    when: customer wants to return a damaged item
    then: acknowledge the damaged order and explain you can review replacement or refund options after verification
journeys:
  - id: damaged_order
    when:
      - customer wants to return a damaged item
    states:
      - id: request_order_details
        type: MessageNode
        instruction: Please share the order number and tell me what arrived damaged so I can review replacement or refund options.
retrievers:
  - id: agent_wiki
    kind: knowledge
    scope: agent
    mode: eager
    max_results: 10
`
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/policy/import", policyYAML)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import policy status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		bundles, err := repo.ListBundles(context.Background())
		if err != nil {
			return false
		}
		for _, bundle := range bundles {
			if bundle.ID == "ecommerce_support" {
				return true
			}
		}
		return false
	})

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/agents", `{
		"id":"agent_storefront",
		"name":"Storefront Support",
		"description":"Handles e-commerce support conversations",
		"default_policy_bundle_id":"ecommerce_support",
		"default_knowledge_scope_kind":"agent",
		"default_knowledge_scope_id":"agent_storefront"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources", fmt.Sprintf(`{
		"id":"src_storefront",
		"scope_kind":"agent",
		"scope_id":"agent_storefront",
		"kind":"folder",
		"uri":%q
	}`, docsDir))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create knowledge source status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources/src_storefront/compile", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("compile knowledge source status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		snapshots, err := repo.ListKnowledgeSnapshots(context.Background(), knowledge.SnapshotQuery{ScopeKind: "agent", ScopeID: "agent_storefront"})
		return err == nil && len(snapshots) > 0
	})

	rec = doJSONRequest(t, srv, http.MethodPut, "/v1/operator/customers/cust_1/preferences/contact_channel", `{
		"agent_id":"agent_storefront",
		"value":"email",
		"operator_id":"op_seed"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed preference status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_validation_1",
		"channel":"acp",
		"agent_id":"agent_storefront",
		"customer_id":"cust_1"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 1 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_validation_1")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_validation_1/messages", `{
		"id":"evt_customer_1",
		"text":"customer wants to return a damaged item. Call me Alex. My toaster arrived cracked."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message 1 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	exec1 := waitForExecutionStatus(t, repo, "sess_validation_1", execution.StatusSucceeded, validationTimeout(4*time.Second))
	assistant1 := waitForAssistantText(t, repo, "sess_validation_1", validationTimeout(2*time.Second))
	if !strings.Contains(assistant1, "order number") || !strings.Contains(assistant1, "replacement or refund") {
		t.Fatalf("assistant 1 = %q, want journey response asking for order details and resolution path", assistant1)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/executions/"+exec1.ID+"/resolved-policy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resolved policy status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resolved1 resolvedPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved1); err != nil {
		t.Fatalf("decode resolved policy 1: %v", err)
	}
	if resolved1.BundleID != "ecommerce_support" || resolved1.ActiveJourney != "damaged_order" {
		t.Fatalf("resolved policy 1 = %#v, want ecommerce_support damaged_order", resolved1)
	}

	prefSeed, err := repo.GetCustomerPreference(context.Background(), "agent_storefront", "cust_1", "contact_channel")
	if err != nil || prefSeed.Value != "email" || prefSeed.Status != customer.PreferenceStatusActive {
		t.Fatalf("seeded preference = %#v err=%v, want active email", prefSeed, err)
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_validation_1/close", `{
		"reason":"resolved"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close session 1 status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_validation_1/feedback", `{
		"id":"fb_preference_validation",
		"operator_id":"op_manager",
		"text":"Call me Alex."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("preference feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForCustomerPreference(t, repo, "agent_storefront", "cust_1", "preferred_name", "Alex", validationTimeout(2*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_validation_1/feedback", `{
		"id":"fb_knowledge_validation",
		"operator_id":"op_manager",
		"category":"knowledge",
		"text":"Knowledge: damaged electronics purchased within 30 days qualify for an instant replacement before refund review."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("knowledge feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var knowledgeFeedback feedback.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &knowledgeFeedback); err != nil {
		t.Fatalf("decode knowledge feedback: %v", err)
	}
	if len(knowledgeFeedback.Outputs.KnowledgeProposalIDs) != 1 {
		t.Fatalf("knowledge feedback outputs = %#v, want one knowledge proposal", knowledgeFeedback.Outputs)
	}
	knowledgeProposalID := knowledgeFeedback.Outputs.KnowledgeProposalIDs[0]

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/operator/knowledge/proposals/"+knowledgeProposalID+"/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("knowledge preview status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/proposals/"+knowledgeProposalID+"/state", `{"state":"approved"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("knowledge proposal approve status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/proposals/"+knowledgeProposalID+"/apply", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("knowledge proposal apply status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	waitFor(t, validationTimeout(2*time.Second), func() bool {
		item, err := repo.GetKnowledgeUpdateProposal(context.Background(), knowledgeProposalID)
		return err == nil && item.State == "applied"
	})

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_validation_1/feedback", `{
		"id":"fb_soul_validation",
		"operator_id":"op_manager",
		"category":"soul",
		"text":"Tone should be warmer and more concise for this agent."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("soul feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var soulFeedback feedback.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &soulFeedback); err != nil {
		t.Fatalf("decode soul feedback: %v", err)
	}
	if len(soulFeedback.Outputs.PolicyProposalIDs) != 1 {
		t.Fatalf("soul feedback outputs = %#v, want one policy proposal", soulFeedback.Outputs)
	}
	policyProposalID := soulFeedback.Outputs.PolicyProposalIDs[0]

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/proposals/"+policyProposalID+"/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("policy preview status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var policyPreview map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &policyPreview); err != nil {
		t.Fatalf("decode policy preview: %v", err)
	}
	changes, ok := policyPreview["changes"].(map[string]any)
	if !ok || changes["soul"] == nil {
		t.Fatalf("policy preview = %#v, want soul diff", policyPreview)
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/proposals/"+policyProposalID+"/state", `{"state":"reviewed"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("policy proposal review status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitForProposalState(t, repo, policyProposalID, rollout.StateReviewed, validationTimeout(2*time.Second))
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/proposals/"+policyProposalID+"/state", `{"state":"shadow"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("policy proposal shadow status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitForProposalState(t, repo, policyProposalID, rollout.StateShadow, validationTimeout(2*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/rollouts", fmt.Sprintf(`{"proposal_id":%q,"channel":"acp","percentage":100}`, policyProposalID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create rollout status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitForProposalState(t, repo, policyProposalID, rollout.StateCanary, validationTimeout(2*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/proposals/"+policyProposalID+"/state", `{"state":"active","approved_high_risk":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("policy proposal active status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitForProposalState(t, repo, policyProposalID, rollout.StateActive, validationTimeout(2*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_validation_2",
		"channel":"acp",
		"agent_id":"agent_storefront",
		"customer_id":"cust_1"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_validation_2")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_validation_2/messages", `{
		"id":"evt_customer_2",
		"text":"Does the electronics article say purchases within 30 days qualify for an instant replacement before review?"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	exec2 := waitForExecutionStatus(t, repo, "sess_validation_2", execution.StatusSucceeded, validationTimeout(4*time.Second))
	assistant2 := waitForAssistantText(t, repo, "sess_validation_2", validationTimeout(2*time.Second))
	if hasLiveProvider() {
		lowerAssistant2 := strings.ToLower(assistant2)
		if !strings.Contains(lowerAssistant2, "alex") ||
			!strings.Contains(lowerAssistant2, "instant replacement") {
			t.Fatalf("assistant 2 = %q, want learned preference and knowledge reflected semantically", assistant2)
		}
		qualityPayload, err := srv.executionQualityPayload(context.Background(), exec2, "")
		if err != nil {
			t.Fatalf("execution 2 quality payload: %v", err)
		}
		if hardFailed, _ := qualityPayload["hard_failed"].(bool); hardFailed {
			t.Fatalf("assistant 2 failed quality gate: %#v", qualityPayload["scorecard"])
		}
	} else {
		if !strings.Contains(assistant2, "Customer preferences (soft constraints):") ||
			!strings.Contains(assistant2, "contact_channel: email") ||
			!strings.Contains(assistant2, "preferred_name: Alex") {
			t.Fatalf("assistant 2 = %q, want stored customer preferences in composed prompt", assistant2)
		}
		if !strings.Contains(assistant2, "Agent SOUL style and brand rules:") ||
			!strings.Contains(assistant2, "Tone: warm") ||
			!strings.Contains(assistant2, "Verbosity: concise") {
			t.Fatalf("assistant 2 = %q, want updated SOUL prompt", assistant2)
		}
		if !strings.Contains(assistant2, "Retrieved knowledge:") ||
			!strings.Contains(assistant2, "instant replacement before refund review") {
			t.Fatalf("assistant 2 = %q, want applied knowledge in composed prompt", assistant2)
		}
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/executions/"+exec2.ID+"/resolved-policy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resolved policy 2 status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resolved2 resolvedPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved2); err != nil {
		t.Fatalf("decode resolved policy 2: %v", err)
	}
	if resolved2.BundleID == "ecommerce_support" {
		t.Fatalf("resolved policy 2 = %#v, want active candidate bundle instead of the original bundle", resolved2)
	}
}

func TestPlatformValidationPendingPreferenceReviewFlow(t *testing.T) {
	repo := memory.New()
	var router *model.Router
	sessionIDs := []string{}
	defer func() {
		writePlatformValidationReport(t, repo, router, t.Name(), "preference_review_flow", "agent_preference", "cust_pref", sessionIDs)
	}()
	writes := asyncwrite.New(repo, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	broker := sse.NewBroker()
	router = model.NewRouter(config.Load("api").Provider)
	r := runner.New(repo, writes, broker, router, "test-worker")
	r.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)

	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/policy/import", `
id: preference_review_bundle
version: v1
no_match: I need to check the details before I answer that.
soul:
  identity: Preference Desk
  tone: calm
  verbosity: concise
retrievers:
  - id: agent_wiki
    kind: knowledge
    scope: agent
`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import policy status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		bundles, err := repo.ListBundles(context.Background())
		if err != nil {
			return false
		}
		for _, bundle := range bundles {
			if bundle.ID == "preference_review_bundle" {
				return true
			}
		}
		return false
	})
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/agents", `{
		"id":"agent_preference",
		"name":"Preference Desk",
		"default_policy_bundle_id":"preference_review_bundle",
		"default_knowledge_scope_kind":"agent",
		"default_knowledge_scope_id":"agent_preference"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_pref_review_1",
		"channel":"acp",
		"agent_id":"agent_preference",
		"customer_id":"cust_pref"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 1 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_pref_review_1")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_pref_review_1/messages", `{
		"id":"evt_pref_review_1",
		"text":"I need help with my account."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForExecutionStatus(t, repo, "sess_pref_review_1", execution.StatusSucceeded, validationTimeout(4*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_pref_review_1/close", `{
		"reason":"resolved"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close session 1 status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_pref_review_1/feedback", `{
		"id":"fb_pref_pending",
		"operator_id":"op_pref",
		"text":"Maybe the customer prefers sms updates."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var feedbackItem feedback.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &feedbackItem); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	if len(feedbackItem.Outputs.PreferenceIDs) != 1 {
		t.Fatalf("feedback outputs = %#v, want one pending preference", feedbackItem.Outputs)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/operator/customers/cust_pref/preferences/pending?agent_id=agent_preference", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list pending preferences status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var pendingViews []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &pendingViews); err != nil {
		t.Fatalf("decode pending preferences: %v", err)
	}
	if len(pendingViews) == 0 || pendingViews[0]["confirmation_prompt"] == "" {
		t.Fatalf("pending preferences = %#v, want confirmation metadata", pendingViews)
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/customers/cust_pref/preferences/inferred_preference/confirm", `{
		"agent_id":"agent_preference",
		"operator_id":"op_pref"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm preference status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	pref, err := repo.GetCustomerPreference(context.Background(), "agent_preference", "cust_pref", "inferred_preference")
	if err != nil || pref.Status != customer.PreferenceStatusActive {
		t.Fatalf("confirmed preference = %#v err=%v, want active", pref, err)
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_pref_review_2",
		"channel":"acp",
		"agent_id":"agent_preference",
		"customer_id":"cust_pref"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_pref_review_2")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_pref_review_2/messages", `{
		"id":"evt_pref_review_2",
		"text":"What updates should I expect?"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForExecutionStatus(t, repo, "sess_pref_review_2", execution.StatusSucceeded, validationTimeout(4*time.Second))
	assistant := waitForAssistantText(t, repo, "sess_pref_review_2", validationTimeout(2*time.Second))
	if strings.TrimSpace(assistant) == "" {
		t.Fatal("assistant response is empty")
	}
}

func TestPlatformValidationLanguagePreferenceLearning(t *testing.T) {
	repo := memory.New()
	var router *model.Router
	sessionIDs := []string{}
	defer func() {
		writePlatformValidationReport(t, repo, router, t.Name(), "language_preference_learning", "agent_language", "cust_lang", sessionIDs)
	}()
	writes := asyncwrite.New(repo, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	broker := sse.NewBroker()
	router = model.NewRouter(config.Load("api").Provider)
	r := runner.New(repo, writes, broker, router, "test-worker")
	r.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)

	root := t.TempDir()
	t.Setenv("KNOWLEDGE_SOURCE_ROOT", root)
	docsDir := filepath.Join(root, "language-docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "notifications.md"), []byte("# Notifications\nWe can send chat or email notifications depending on the customer's preference."), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/policy/import", `
id: language_bundle
version: v1
soul:
  identity: Language Support
  default_language: en
  supported_languages:
    - en
    - id
  tone: calm
  verbosity: concise
retrievers:
  - id: agent_wiki
    kind: knowledge
    scope: agent
`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import policy status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		bundles, err := repo.ListBundles(context.Background())
		if err != nil {
			return false
		}
		for _, bundle := range bundles {
			if bundle.ID == "language_bundle" {
				return true
			}
		}
		return false
	})
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/agents", `{
		"id":"agent_language",
		"name":"Language Support",
		"default_policy_bundle_id":"language_bundle",
		"default_knowledge_scope_kind":"agent",
		"default_knowledge_scope_id":"agent_language"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources", fmt.Sprintf(`{
		"id":"src_language",
		"scope_kind":"agent",
		"scope_id":"agent_language",
		"kind":"folder",
		"uri":%q
	}`, docsDir))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create knowledge source status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources/src_language/compile", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("compile knowledge source status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_language_1",
		"channel":"acp",
		"agent_id":"agent_language",
		"customer_id":"cust_lang"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 1 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_language_1")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_language_1/messages", `{
		"id":"evt_language_1",
		"text":"Please respond in Indonesian. Call me Rina."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message 1 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForExecutionStatus(t, repo, "sess_language_1", execution.StatusSucceeded, validationTimeout(4*time.Second))
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_language_1/close", `{
		"reason":"resolved"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close session 1 status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/sess_language_1/feedback", `{
		"id":"fb_language_learning",
		"operator_id":"op_lang",
		"text":"Respond in Indonesian. Call me Rina."
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("language feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForCustomerPreference(t, repo, "agent_language", "cust_lang", "preferred_name", "Rina", validationTimeout(2*time.Second))
	waitForCustomerPreference(t, repo, "agent_language", "cust_lang", "preferred_language", "indonesian", validationTimeout(2*time.Second))

	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_language_2",
		"channel":"acp",
		"agent_id":"agent_language",
		"customer_id":"cust_lang"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_language_2")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_language_2/messages", `{
		"id":"evt_language_2",
		"text":"What notification options are available for me?"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message 2 status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	waitForExecutionStatus(t, repo, "sess_language_2", execution.StatusSucceeded, validationTimeout(4*time.Second))
	assistant := waitForAssistantText(t, repo, "sess_language_2", validationTimeout(2*time.Second))
	if strings.TrimSpace(assistant) == "" {
		t.Fatal("assistant response is empty")
	}
}

func TestPlatformValidationPetStoreScopeQuality(t *testing.T) {
	repo := memory.New()
	var router *model.Router
	sessionIDs := []string{}
	defer func() {
		writePlatformValidationReport(t, repo, router, t.Name(), "pet_store_scope_quality", "agent_pet_store", "cust_scope", sessionIDs)
	}()
	writes := asyncwrite.New(repo, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	broker := sse.NewBroker()
	router = model.NewRouter(config.Load("api").Provider)
	r := runner.New(repo, writes, broker, router, "test-worker")
	r.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)

	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/policy/import", `
id: pet_store_scope
version: v1
domain_boundary:
  mode: hard_refuse
  allowed_topics:
    - pet food
    - pet toys
    - grooming
  adjacent_topics:
    - pet-safe ingredients
    - veterinarian
  adjacent_action: redirect
  blocked_topics:
    - cooking
    - human food
    - memasak
    - makanan manusia
  out_of_scope_reply: I can help with pet-store questions, but not cooking or human food. If you want, I can help with pet-safe food options.
soul:
  identity: Pet Store Assistant
  role: Pet-store support agent
  default_language: en
  supported_languages:
    - en
    - id
  tone: practical
  verbosity: concise
guidelines:
  - id: pet_food_help
    when: customer asks about pet food
    then: help the customer compare pet food options within the store catalog
`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import policy status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		bundles, err := repo.ListBundles(context.Background())
		if err != nil {
			return false
		}
		for _, bundle := range bundles {
			if bundle.ID == "pet_store_scope" {
				return true
			}
		}
		return false
	})
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/operator/agents", `{
		"id":"agent_pet_store",
		"name":"Pet Store Assistant",
		"default_policy_bundle_id":"pet_store_scope",
		"default_knowledge_scope_kind":"agent",
		"default_knowledge_scope_id":"agent_pet_store"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", `{
		"id":"sess_pet_scope_1",
		"channel":"acp",
		"agent_id":"agent_pet_store",
		"customer_id":"cust_scope"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	sessionIDs = append(sessionIDs, "sess_pet_scope_1")
	rec = doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/sess_pet_scope_1/messages", `{
		"id":"evt_pet_scope_1",
		"text":"Bagaimana cara memasak makanan manusia untuk makan malam?"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	exec := waitForExecutionStatus(t, repo, "sess_pet_scope_1", execution.StatusSucceeded, validationTimeout(4*time.Second))
	assistant := waitForAssistantText(t, repo, "sess_pet_scope_1", validationTimeout(2*time.Second))
	if assistant != "I can help with pet-store questions, but not cooking or human food. If you want, I can help with pet-safe food options." {
		t.Fatalf("assistant = %q, want configured out-of-scope boundary reply", assistant)
	}
	rec = doJSONRequest(t, srv, http.MethodGet, "/v1/executions/"+exec.ID+"/resolved-policy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resolved policy status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resolved resolvedPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolved policy: %v", err)
	}
	if resolved.ScopeClassification != "out_of_scope" || resolved.ScopeAction != "refuse" {
		t.Fatalf("resolved scope = %#v, want out_of_scope refuse", resolved)
	}
}

func TestPlatformValidationLiveGateCatalog(t *testing.T) {
	router := model.NewRouter(config.Load("api").Provider)
	for _, scenario := range quality.ProductionReadinessScenarios() {
		if !scenario.LiveGate {
			continue
		}
		t.Run(scenario.ID, func(t *testing.T) {
			if strings.EqualFold(strings.TrimSpace(scenario.ExecutionMode), "platform_flow") {
				runPlatformFlowLiveGateScenario(t, scenario)
				return
			}
			view, response, ok := quality.ScenarioFixture(scenario)
			if !ok {
				t.Fatalf("live-gate scenario %s has no fixture", scenario.ID)
			}
			card := quality.GradeWithLLM(context.Background(), router, view, response, nil)
			if quality.HardFailed(card) || !card.Passed || card.Overall < scenario.MinimumOverall {
				t.Fatalf("scenario %s scorecard = %#v, want release-gate pass at %.2f", scenario.ID, card, scenario.MinimumOverall)
			}
			writePlatformValidationScorecardReport(t, t.Name(), scenario.ID, router, card)
		})
	}
}

func doJSONRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.TrimSpace(body) != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	return rec
}

func waitForExecutionStatus(t *testing.T, repo *memory.Store, sessionID string, want execution.Status, timeout time.Duration) execution.TurnExecution {
	t.Helper()
	var matched execution.TurnExecution
	var latest execution.TurnExecution
	var latestSteps []execution.ExecutionStep
	waitFor(t, timeout, func() bool {
		items, err := repo.ListExecutions(context.Background())
		if err != nil {
			return false
		}
		var found execution.TurnExecution
		for _, item := range items {
			if item.SessionID != sessionID {
				continue
			}
			if found.ID == "" || item.CreatedAt.After(found.CreatedAt) {
				found = item
			}
		}
		if found.ID == "" {
			return false
		}
		matched = found
		latest = found
		_, steps, err := repo.GetExecution(context.Background(), found.ID)
		if err == nil {
			latestSteps = steps
		}
		switch found.Status {
		case execution.StatusFailed, execution.StatusBlocked, execution.StatusAbandoned:
			t.Fatalf("execution %s for session %s reached terminal status %s; steps=%#v", found.ID, sessionID, found.Status, latestSteps)
		}
		return found.Status == want
	})
	if matched.Status != want {
		t.Fatalf("latest execution for session %s = %#v; steps=%#v", sessionID, latest, latestSteps)
	}
	return matched
}

func waitForExecutionStatusAllowBlocked(t *testing.T, repo *memory.Store, sessionID string, want execution.Status, timeout time.Duration) execution.TurnExecution {
	t.Helper()
	var matched execution.TurnExecution
	var latest execution.TurnExecution
	var latestSteps []execution.ExecutionStep
	waitFor(t, timeout, func() bool {
		items, err := repo.ListExecutions(context.Background())
		if err != nil {
			return false
		}
		var found execution.TurnExecution
		for _, item := range items {
			if item.SessionID != sessionID {
				continue
			}
			if found.ID == "" || item.CreatedAt.After(found.CreatedAt) {
				found = item
			}
		}
		if found.ID == "" {
			return false
		}
		matched = found
		latest = found
		_, steps, err := repo.GetExecution(context.Background(), found.ID)
		if err == nil {
			latestSteps = steps
		}
		switch found.Status {
		case execution.StatusFailed, execution.StatusAbandoned:
			t.Fatalf("execution %s for session %s reached terminal status %s; steps=%#v", found.ID, sessionID, found.Status, latestSteps)
		}
		return found.Status == want
	})
	if matched.Status != want {
		t.Fatalf("latest execution for session %s = %#v; steps=%#v", sessionID, latest, latestSteps)
	}
	return matched
}

func waitForPendingApprovals(t *testing.T, repo *memory.Store, sessionID string, timeout time.Duration) []approval.Session {
	t.Helper()
	var out []approval.Session
	waitFor(t, timeout, func() bool {
		items, err := repo.ListApprovalSessions(context.Background(), sessionID)
		if err != nil {
			return false
		}
		out = nil
		for _, item := range items {
			if item.Status == approval.StatusPending {
				out = append(out, item)
			}
		}
		return len(out) > 0
	})
	return out
}

func waitForAssistantText(t *testing.T, repo *memory.Store, sessionID string, timeout time.Duration) string {
	t.Helper()
	var text string
	waitFor(t, timeout, func() bool {
		events, err := repo.ListEvents(context.Background(), sessionID)
		if err != nil {
			return false
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Source != "ai_agent" {
				continue
			}
			for _, part := range events[i].Content {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					text = part.Text
					return true
				}
			}
		}
		return false
	})
	return text
}

func waitForSessionWatchCount(t *testing.T, repo *memory.Store, sessionID string, want int, timeout time.Duration) []session.Watch {
	t.Helper()
	var watches []session.Watch
	waitFor(t, timeout, func() bool {
		items, err := repo.ListSessionWatches(context.Background(), session.WatchQuery{SessionID: sessionID})
		if err != nil {
			return false
		}
		watches = items
		return len(items) == want
	})
	if len(watches) != want {
		t.Fatalf("session %s watches = %#v, want %d", sessionID, watches, want)
	}
	return watches
}

func waitForProposalState(t *testing.T, repo *memory.Store, proposalID string, want rollout.ProposalState, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		item, err := repo.GetProposal(context.Background(), proposalID)
		return err == nil && item.State == want
	})
}

func waitForCustomerPreference(t *testing.T, repo *memory.Store, agentID, customerID, key, value string, timeout time.Duration) customer.Preference {
	t.Helper()
	var pref customer.Preference
	var lastErr error
	waitFor(t, timeout, func() bool {
		item, err := repo.GetCustomerPreference(context.Background(), agentID, customerID, key)
		if err != nil {
			lastErr = err
			return false
		}
		pref = item
		return item.Value == value && item.Status == customer.PreferenceStatusActive
	})
	if pref.Value != value || pref.Status != customer.PreferenceStatusActive {
		t.Fatalf("preference %s/%s/%s = %#v err=%v, want active %q", agentID, customerID, key, pref, lastErr, value)
	}
	return pref
}

type platformFlowHarness struct {
	repo   *memory.Store
	writes *asyncwrite.Queue
	router *model.Router
	srv    *Server
	ctx    context.Context
	cancel context.CancelFunc
}

func newPlatformFlowHarness(t *testing.T) *platformFlowHarness {
	t.Helper()
	repo := memory.New()
	writes := asyncwrite.New(repo, 128)
	ctx, cancel := context.WithCancel(context.Background())
	writes.Start(ctx, 1)
	broker := sse.NewBroker()
	router := model.NewRouter(config.Load("api").Provider)
	runtimeRunner := runner.New(repo, writes, broker, router, "live-gate-worker")
	runtimeRunner.Start(ctx)
	maintainerRunner := maintainerworker.New(repo, router)
	maintainerRunner.Start(ctx)
	srv := New(":0", repo, writes, broker, router, nil)
	t.Cleanup(func() {
		cancel()
		writes.Stop()
	})
	return &platformFlowHarness{repo: repo, writes: writes, router: router, srv: srv, ctx: ctx, cancel: cancel}
}

func runPlatformFlowLiveGateScenario(t *testing.T, scenario quality.ScenarioExpectation) {
	t.Helper()
	if !hasLiveProvider() {
		t.Skip("platform-flow live-gate scenarios require a real LLM provider")
	}
	h := newPlatformFlowHarness(t)
	report := platformValidationReport{
		Scenario:     scenario.ID,
		TestName:     t.Name(),
		GeneratedAt:  time.Now().UTC(),
		LiveProvider: hasLiveProvider(),
	}
	if h.router != nil {
		report.ProviderStats = h.router.Snapshot()
	}
	switch scenario.PlatformFlowKind {
	case "maintainer_shared_bootstrap_ingest_retrieve":
		runLiveMaintainerSharedBootstrapIngestRetrieve(t, h, scenario, &report)
	case "maintainer_feedback_shared_update_retrieve":
		runLiveMaintainerFeedbackSharedUpdateRetrieve(t, h, scenario, &report)
	case "maintainer_customer_memory_learn_retrieve":
		runLiveMaintainerCustomerMemoryLearnRetrieve(t, h, scenario, &report)
	default:
		t.Fatalf("unsupported platform flow kind %q", scenario.PlatformFlowKind)
	}
	if h.router != nil {
		report.ProviderStats = h.router.Snapshot()
	}
	writePlatformValidationReportFile(t, report)
}

func runLiveMaintainerSharedBootstrapIngestRetrieve(t *testing.T, h *platformFlowHarness, scenario quality.ScenarioExpectation, report *platformValidationReport) {
	t.Helper()
	agentID := "agent_live_loop_bootstrap"
	docsDir := t.TempDir()
	t.Setenv("KNOWLEDGE_SOURCE_ROOT", docsDir)
	mustWriteValidationDoc(t, docsDir, "solarblue.md", "# SolarBlue Kettle\n\nThe SolarBlue kettle qualifies for replacement review within 21 days of delivery. Cite the SolarBlue Kettle policy page when answering.")
	importValidationBundle(t, h.srv, h.repo, "closed_loop_bootstrap_bundle", "Closed Loop Support")
	createValidationAgent(t, h.srv, agentID, "closed_loop_bootstrap_bundle")
	_ = waitForMaintainerWorkspace(t, h.repo, "agent", agentID, maintainerdomain.ModeSharedWiki, validationTimeout(8*time.Second))
	job := waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(8*time.Second))
	workspace := waitForMaintainerWorkspace(t, h.repo, "agent", agentID, maintainerdomain.ModeSharedWiki, validationTimeout(8*time.Second))
	run := mustGetMaintainerRun(t, h.repo, job.RunID)
	if workspace.IndexPageID == "" || workspace.LogPageID == "" {
		t.Fatalf("workspace = %#v, want index/log pages", workspace)
	}

	createValidationKnowledgeSource(t, h.srv, "src_live_bootstrap", "agent", agentID, docsDir)
	triggerKnowledgeCompile(t, h.srv, "src_live_bootstrap")
	syncJob := waitForKnowledgeSyncJob(t, h.repo, "src_live_bootstrap", "succeeded", validationTimeout(20*time.Second))
	sourceJob := waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, SourceID: "src_live_bootstrap", Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(20*time.Second))
	sourceRun := mustGetMaintainerRun(t, h.repo, sourceJob.RunID)
	if strings.TrimSpace(sourceJob.RunID) == "" || syncJob.Metadata["maintainer_run_id"] == nil {
		t.Fatalf("sync job = %#v, want maintainer linkage", syncJob)
	}

	sessionID := "sess_live_loop_bootstrap"
	createValidationSession(t, h.srv, sessionID, agentID, "cust_live_bootstrap")
	sendValidationMessage(t, h.srv, sessionID, "evt_live_loop_bootstrap", scenario.Input)
	exec := waitForExecutionStatus(t, h.repo, sessionID, execution.StatusSucceeded, validationTimeout(12*time.Second))
	response := waitForAssistantText(t, h.repo, sessionID, validationTimeout(4*time.Second))
	if !strings.Contains(strings.ToLower(response), "21") {
		t.Fatalf("response = %q, want SolarBlue fact", response)
	}
	payload := requireExecutionQuality(t, h.srv, exec)
	if len(payload.EvidenceMatches) == 0 {
		t.Fatalf("quality payload = %#v, want retrieval evidence matches", payload)
	}

	report.AgentID = agentID
	report.CustomerID = "cust_live_bootstrap"
	report.Sessions = append(report.Sessions, platformValidationSession{
		ID:           sessionID,
		Executions:   []execution.TurnExecution{exec},
		Quality:      map[string]platformValidationQualityPayload{exec.ID: payload},
		ResponseText: response,
		UsedPageIDs:  knowledgePageIDs(t, h.repo, "agent", agentID),
	})
	report.MaintainerWorkspaces = []maintainerdomain.Workspace{workspace}
	report.MaintainerJobs = []maintainerdomain.Job{job, sourceJob}
	report.MaintainerRuns = []maintainerdomain.Run{run, sourceRun}
	report.KnowledgeSnapshots = knowledgeSnapshotsForScope(t, h.repo, "agent", agentID)
}

func runLiveMaintainerFeedbackSharedUpdateRetrieve(t *testing.T, h *platformFlowHarness, scenario quality.ScenarioExpectation, report *platformValidationReport) {
	t.Helper()
	agentID := "agent_live_loop_feedback"
	customerID := "cust_live_feedback"
	docsDir := t.TempDir()
	t.Setenv("KNOWLEDGE_SOURCE_ROOT", docsDir)
	mustWriteValidationDoc(t, docsDir, "emerald.md", "# Emerald Mixer Returns\n\nEmerald Mixer returns currently take 14 days to complete after verification.")
	importValidationBundle(t, h.srv, h.repo, "closed_loop_feedback_bundle", "Feedback Loop Support")
	createValidationAgent(t, h.srv, agentID, "closed_loop_feedback_bundle")
	_ = waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(8*time.Second))

	createValidationKnowledgeSource(t, h.srv, "src_live_feedback", "agent", agentID, docsDir)
	triggerKnowledgeCompile(t, h.srv, "src_live_feedback")
	_ = waitForKnowledgeSyncJob(t, h.repo, "src_live_feedback", "succeeded", validationTimeout(20*time.Second))
	_ = waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, SourceID: "src_live_feedback", Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(20*time.Second))

	sessionID := "sess_live_loop_feedback_1"
	createValidationSession(t, h.srv, sessionID, agentID, customerID)
	sendValidationMessage(t, h.srv, sessionID, "evt_live_loop_feedback_1", "What timeline is listed for Emerald Mixer returns?")
	preExec := waitForExecutionStatus(t, h.repo, sessionID, execution.StatusSucceeded, validationTimeout(12*time.Second))
	preResponse := waitForAssistantText(t, h.repo, sessionID, validationTimeout(4*time.Second))
	if !strings.Contains(strings.ToLower(preResponse), "day") {
		t.Fatalf("pre-feedback response = %q, want timeline language", preResponse)
	}
	closeValidationSession(t, h.srv, sessionID)

	feedbackRecord := createValidationFeedback(t, h.srv, sessionID, `{
		"id":"fb_live_loop_feedback",
		"operator_id":"op_live",
		"category":"knowledge",
		"text":"Knowledge: Emerald Mixer returns now take 30 days after verification."
	}`)
	if len(feedbackRecord.Outputs.KnowledgeProposalIDs) != 1 {
		t.Fatalf("feedback outputs = %#v, want one knowledge proposal", feedbackRecord.Outputs)
	}
	feedbackJob := waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, FeedbackID: feedbackRecord.ID, Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(12*time.Second))
	feedbackRun := mustGetMaintainerRun(t, h.repo, feedbackJob.RunID)
	proposalID := feedbackRecord.Outputs.KnowledgeProposalIDs[0]
	previewKnowledgeProposal(t, h.srv, proposalID)
	transitionKnowledgeProposal(t, h.srv, proposalID, "approved")
	applyKnowledgeProposal(t, h.srv, proposalID)
	waitFor(t, validationTimeout(4*time.Second), func() bool {
		item, err := h.repo.GetKnowledgeUpdateProposal(context.Background(), proposalID)
		return err == nil && item.State == "applied"
	})

	sessionID2 := "sess_live_loop_feedback_2"
	createValidationSession(t, h.srv, sessionID2, agentID, customerID)
	sendValidationMessage(t, h.srv, sessionID2, "evt_live_loop_feedback_2", scenario.Input)
	postExec := waitForExecutionStatus(t, h.repo, sessionID2, execution.StatusSucceeded, validationTimeout(12*time.Second))
	postResponse := waitForAssistantText(t, h.repo, sessionID2, validationTimeout(4*time.Second))
	if !strings.Contains(strings.ToLower(postResponse), "30") {
		t.Fatalf("post-feedback response = %q, want updated fact", postResponse)
	}
	if strings.EqualFold(strings.TrimSpace(preResponse), strings.TrimSpace(postResponse)) {
		t.Fatalf("post-feedback response = %q, want change from pre-feedback response %q", postResponse, preResponse)
	}
	postPayload := requireExecutionQuality(t, h.srv, postExec)

	report.AgentID = agentID
	report.CustomerID = customerID
	report.Sessions = append(report.Sessions,
		platformValidationSession{
			ID:           sessionID,
			Executions:   []execution.TurnExecution{preExec},
			ResponseText: preResponse,
		},
		platformValidationSession{
			ID:           sessionID2,
			Executions:   []execution.TurnExecution{postExec},
			Quality:      map[string]platformValidationQualityPayload{postExec.ID: postPayload},
			ResponseText: postResponse,
			UsedPageIDs:  knowledgePageIDs(t, h.repo, "agent", agentID),
		},
	)
	report.Feedback = []feedback.Record{feedbackRecord}
	report.Knowledge = knowledgeProposalsForScope(t, h.repo, "agent", agentID)
	report.MaintainerJobs = []maintainerdomain.Job{feedbackJob}
	report.MaintainerRuns = []maintainerdomain.Run{feedbackRun}
	report.KnowledgeSnapshots = knowledgeSnapshotsForScope(t, h.repo, "agent", agentID)
}

func runLiveMaintainerCustomerMemoryLearnRetrieve(t *testing.T, h *platformFlowHarness, scenario quality.ScenarioExpectation, report *platformValidationReport) {
	t.Helper()
	agentID := "agent_live_loop_memory"
	customerID := "cust_live_memory"
	importValidationBundle(t, h.srv, h.repo, "closed_loop_memory_bundle", "Memory Loop Support")
	createValidationAgent(t, h.srv, agentID, "closed_loop_memory_bundle")
	_ = waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "agent", ScopeID: agentID, Mode: maintainerdomain.ModeSharedWiki, Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(8*time.Second))

	sessionID := "sess_live_loop_memory_1"
	createValidationSession(t, h.srv, sessionID, agentID, customerID)
	sendValidationMessage(t, h.srv, sessionID, "evt_live_loop_memory_1", "Please send me updates via sms. Please remember that.")
	_ = waitForExecutionStatus(t, h.repo, sessionID, execution.StatusSucceeded, validationTimeout(12*time.Second))
	closeValidationSession(t, h.srv, sessionID)

	memoryWorkspace := waitForMaintainerWorkspace(t, h.repo, "customer_agent", agentID+":"+customerID, maintainerdomain.ModeCustomerMemory, validationTimeout(8*time.Second))
	sessionJob := waitForMaintainerJob(t, h.repo, maintainerdomain.JobQuery{ScopeKind: "customer_agent", ScopeID: agentID + ":" + customerID, Mode: maintainerdomain.ModeCustomerMemory, SessionID: sessionID, Limit: 20}, maintainerdomain.StatusSucceeded, validationTimeout(12*time.Second))
	sessionRun := mustGetMaintainerRun(t, h.repo, sessionJob.RunID)
	waitForCustomerPreference(t, h.repo, agentID, customerID, "contact_channel", "sms", validationTimeout(12*time.Second))

	sessionID2 := "sess_live_loop_memory_2"
	createValidationSession(t, h.srv, sessionID2, agentID, customerID)
	sendValidationMessage(t, h.srv, sessionID2, "evt_live_loop_memory_2", scenario.Input)
	execSame := waitForExecutionStatus(t, h.repo, sessionID2, execution.StatusSucceeded, validationTimeout(12*time.Second))
	responseSame := waitForAssistantText(t, h.repo, sessionID2, validationTimeout(4*time.Second))
	if !strings.Contains(strings.ToLower(responseSame), "sms") {
		t.Fatalf("same-customer response = %q, want learned preference", responseSame)
	}
	payloadSame := requireExecutionQuality(t, h.srv, execSame)

	sessionID3 := "sess_live_loop_memory_3"
	createValidationSession(t, h.srv, sessionID3, agentID, "cust_live_memory_other")
	sendValidationMessage(t, h.srv, sessionID3, "evt_live_loop_memory_3", scenario.Input)
	_ = waitForExecutionStatus(t, h.repo, sessionID3, execution.StatusSucceeded, validationTimeout(12*time.Second))
	responseOther := waitForAssistantText(t, h.repo, sessionID3, validationTimeout(4*time.Second))
	if strings.Contains(strings.ToLower(responseOther), "sms") {
		t.Fatalf("other-customer response = %q, want no inherited memory", responseOther)
	}

	report.AgentID = agentID
	report.CustomerID = customerID
	report.Sessions = append(report.Sessions,
		platformValidationSession{
			ID:           sessionID2,
			Executions:   []execution.TurnExecution{execSame},
			Quality:      map[string]platformValidationQualityPayload{execSame.ID: payloadSame},
			ResponseText: responseSame,
			UsedPageIDs:  knowledgePageIDs(t, h.repo, "customer_agent", agentID+":"+customerID),
		},
		platformValidationSession{
			ID:           sessionID3,
			ResponseText: responseOther,
		},
	)
	report.Preferences = preferencesForCustomer(t, h.repo, agentID, customerID)
	report.MaintainerWorkspaces = []maintainerdomain.Workspace{memoryWorkspace}
	report.MaintainerJobs = []maintainerdomain.Job{sessionJob}
	report.MaintainerRuns = []maintainerdomain.Run{sessionRun}
	report.KnowledgeSnapshots = knowledgeSnapshotsForScope(t, h.repo, "customer_agent", agentID+":"+customerID)
}

func importValidationBundle(t *testing.T, srv *Server, repo *memory.Store, bundleID, identity string) {
	t.Helper()
	body := fmt.Sprintf(`
id: %s
version: v1
no_match: I need to check the knowledge base before I answer that.
soul:
  identity: %s
  default_language: en
  tone: calm
  verbosity: concise
  style_rules:
    - cite the relevant knowledge page when you rely on retrieved knowledge
    - keep factual answers brief
retrievers:
  - id: agent_wiki
    kind: knowledge
    scope: agent
    mode: eager
    max_results: 8
`, bundleID, identity)
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/policy/import", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import policy status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	waitFor(t, validationTimeout(4*time.Second), func() bool {
		bundles, err := repo.ListBundles(context.Background())
		if err != nil {
			return false
		}
		for _, bundle := range bundles {
			if bundle.ID == bundleID {
				return true
			}
		}
		return false
	})
}

func createValidationAgent(t *testing.T, srv *Server, agentID, bundleID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/agents", fmt.Sprintf(`{
		"id":%q,
		"name":%q,
		"description":"Closed-loop validation agent",
		"default_policy_bundle_id":%q,
		"default_knowledge_scope_kind":"agent",
		"default_knowledge_scope_id":%q
	}`, agentID, agentID, bundleID, agentID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func createValidationKnowledgeSource(t *testing.T, srv *Server, sourceID, scopeKind, scopeID, uri string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources", fmt.Sprintf(`{
		"id":%q,
		"scope_kind":%q,
		"scope_id":%q,
		"kind":"folder",
		"uri":%q
	}`, sourceID, scopeKind, scopeID, uri))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create knowledge source status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func triggerKnowledgeCompile(t *testing.T, srv *Server, sourceID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/sources/"+sourceID+"/resync?force=true", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("resync knowledge source status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

func createValidationSession(t *testing.T, srv *Server, sessionID, agentID, customerID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions", fmt.Sprintf(`{
		"id":%q,
		"channel":"acp",
		"agent_id":%q,
		"customer_id":%q
	}`, sessionID, agentID, customerID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func sendValidationMessage(t *testing.T, srv *Server, sessionID, eventID, text string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/acp/sessions/"+sessionID+"/messages", fmt.Sprintf(`{
		"id":%q,
		"text":%q
	}`, eventID, text))
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func closeValidationSession(t *testing.T, srv *Server, sessionID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/"+sessionID+"/close", `{"reason":"resolved"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close session status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func createValidationFeedback(t *testing.T, srv *Server, sessionID, body string) feedback.Record {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/sessions/"+sessionID+"/feedback", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var record feedback.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &record); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	return record
}

func previewKnowledgeProposal(t *testing.T, srv *Server, proposalID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodGet, "/v1/operator/knowledge/proposals/"+proposalID+"/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview knowledge proposal status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func transitionKnowledgeProposal(t *testing.T, srv *Server, proposalID, state string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/proposals/"+proposalID+"/state", fmt.Sprintf(`{"state":%q}`, state))
	if rec.Code != http.StatusOK {
		t.Fatalf("transition knowledge proposal status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func applyKnowledgeProposal(t *testing.T, srv *Server, proposalID string) {
	t.Helper()
	rec := doJSONRequest(t, srv, http.MethodPost, "/v1/operator/knowledge/proposals/"+proposalID+"/apply", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("apply knowledge proposal status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func waitForMaintainerWorkspace(t *testing.T, repo *memory.Store, scopeKind, scopeID, mode string, timeout time.Duration) maintainerdomain.Workspace {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var workspace maintainerdomain.Workspace
	var last []maintainerdomain.Workspace
	for time.Now().Before(deadline) {
		items, err := repo.ListMaintainerWorkspaces(context.Background(), maintainerdomain.WorkspaceQuery{ScopeKind: scopeKind, ScopeID: scopeID, Mode: mode, Limit: 10})
		if err == nil {
			last = items
			if len(items) > 0 {
				workspace = items[0]
				if workspace.ID != "" {
					return workspace
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("missing maintainer workspace for %s/%s/%s; last=%#v", scopeKind, scopeID, mode, last)
	return maintainerdomain.Workspace{}
}

func waitForMaintainerJob(t *testing.T, repo *memory.Store, query maintainerdomain.JobQuery, want string, timeout time.Duration) maintainerdomain.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var job maintainerdomain.Job
	var last []maintainerdomain.Job
	for time.Now().Before(deadline) {
		items, err := repo.ListMaintainerJobs(context.Background(), query)
		if err == nil {
			last = items
			if len(items) > 0 {
				sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
				job = items[0]
				if job.Status == maintainerdomain.StatusFailed {
					t.Fatalf("maintainer job failed: %#v", job)
				}
				if job.Status == want {
					return job
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("maintainer job query %#v => latest=%#v all=%#v, want %s", query, job, last, want)
	return maintainerdomain.Job{}
}

func mustGetMaintainerRun(t *testing.T, repo *memory.Store, runID string) maintainerdomain.Run {
	t.Helper()
	run, err := repo.GetMaintainerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetMaintainerRun(%q) error = %v", runID, err)
	}
	return run
}

func waitForKnowledgeSyncJob(t *testing.T, repo *memory.Store, sourceID, want string, timeout time.Duration) knowledge.SyncJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var job knowledge.SyncJob
	var last []knowledge.SyncJob
	for time.Now().Before(deadline) {
		items, err := repo.ListKnowledgeSyncJobs(context.Background(), knowledge.SyncJobQuery{SourceID: sourceID, Limit: 20})
		if err == nil {
			last = items
			if len(items) > 0 {
				sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
				job = items[0]
				if job.Status == "failed" {
					t.Fatalf("knowledge sync job failed: %#v", job)
				}
				if job.Status == want {
					return job
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("knowledge sync for %s => latest=%#v all=%#v, want %s", sourceID, job, last, want)
	return knowledge.SyncJob{}
}

func requireExecutionQuality(t *testing.T, srv *Server, exec execution.TurnExecution) platformValidationQualityPayload {
	t.Helper()
	payload, err := srv.executionQualityPayload(context.Background(), exec, "")
	if err != nil {
		t.Fatalf("executionQualityPayload(%s) error = %v", exec.ID, err)
	}
	return platformValidationQualityPayload{
		Plan:            typedQualityPayload[quality.ResponsePlan](payload, "plan"),
		Claims:          typedQualityPayload[[]quality.ResponseClaim](payload, "claims"),
		EvidenceMatches: typedQualityPayload[[]quality.EvidenceMatch](payload, "evidence_matches"),
		Scorecard:       typedQualityPayload[quality.Scorecard](payload, "scorecard"),
		HardFailed:      boolQualityPayload(payload, "hard_failed"),
	}
}

func mustWriteValidationDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func knowledgeSnapshotsForScope(t *testing.T, repo *memory.Store, scopeKind, scopeID string) []knowledge.Snapshot {
	t.Helper()
	items, err := repo.ListKnowledgeSnapshots(context.Background(), knowledge.SnapshotQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 100})
	if err != nil {
		t.Fatalf("ListKnowledgeSnapshots(%s,%s) error = %v", scopeKind, scopeID, err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items
}

func knowledgePageIDs(t *testing.T, repo *memory.Store, scopeKind, scopeID string) []string {
	t.Helper()
	pages, err := repo.ListKnowledgePages(context.Background(), knowledge.PageQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1000})
	if err != nil {
		t.Fatalf("ListKnowledgePages(%s,%s) error = %v", scopeKind, scopeID, err)
	}
	out := make([]string, 0, len(pages))
	for _, page := range pages {
		out = append(out, page.ID)
	}
	sort.Strings(out)
	return out
}

func knowledgeProposalsForScope(t *testing.T, repo *memory.Store, scopeKind, scopeID string) []knowledge.UpdateProposal {
	t.Helper()
	items, err := repo.ListKnowledgeUpdateProposals(context.Background(), scopeKind, scopeID)
	if err != nil {
		t.Fatalf("ListKnowledgeUpdateProposals(%s,%s) error = %v", scopeKind, scopeID, err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items
}

func preferencesForCustomer(t *testing.T, repo *memory.Store, agentID, customerID string) []customer.Preference {
	t.Helper()
	items, err := repo.ListCustomerPreferences(context.Background(), customer.PreferenceQuery{
		AgentID:        agentID,
		CustomerID:     customerID,
		IncludeExpired: true,
		Limit:          100,
	})
	if err != nil {
		t.Fatalf("ListCustomerPreferences(%s,%s) error = %v", agentID, customerID, err)
	}
	return items
}

type platformValidationReport struct {
	Scenario             string                       `json:"scenario"`
	TestName             string                       `json:"test_name"`
	GeneratedAt          time.Time                    `json:"generated_at"`
	LiveProvider         bool                         `json:"live_provider"`
	ProviderStats        []model.ProviderStats        `json:"provider_stats,omitempty"`
	AgentID              string                       `json:"agent_id,omitempty"`
	CustomerID           string                       `json:"customer_id,omitempty"`
	Sessions             []platformValidationSession  `json:"sessions,omitempty"`
	Preferences          []customer.Preference        `json:"preferences,omitempty"`
	Feedback             []feedback.Record            `json:"feedback,omitempty"`
	Knowledge            []knowledge.UpdateProposal   `json:"knowledge_proposals,omitempty"`
	PolicyProposals      []rollout.Proposal           `json:"policy_proposals,omitempty"`
	MaintainerWorkspaces []maintainerdomain.Workspace `json:"maintainer_workspaces,omitempty"`
	MaintainerJobs       []maintainerdomain.Job       `json:"maintainer_jobs,omitempty"`
	MaintainerRuns       []maintainerdomain.Run       `json:"maintainer_runs,omitempty"`
	KnowledgeSnapshots   []knowledge.Snapshot         `json:"knowledge_snapshots,omitempty"`
}

type platformValidationSession struct {
	ID           string                                      `json:"id"`
	Transcript   []platformValidationTranscript              `json:"transcript,omitempty"`
	Executions   []execution.TurnExecution                   `json:"executions,omitempty"`
	Scorecards   map[string]quality.Scorecard                `json:"scorecards,omitempty"`
	Quality      map[string]platformValidationQualityPayload `json:"quality,omitempty"`
	ResponseText string                                      `json:"response_text,omitempty"`
	UsedPageIDs  []string                                    `json:"used_page_ids,omitempty"`
}

type platformValidationTranscript struct {
	EventID     string    `json:"event_id"`
	Source      string    `json:"source"`
	Kind        string    `json:"kind"`
	Text        string    `json:"text,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type platformValidationQualityPayload struct {
	Plan            quality.ResponsePlan    `json:"plan"`
	Claims          []quality.ResponseClaim `json:"claims,omitempty"`
	EvidenceMatches []quality.EvidenceMatch `json:"evidence_matches,omitempty"`
	Scorecard       quality.Scorecard       `json:"scorecard"`
	HardFailed      bool                    `json:"hard_failed"`
}

func writePlatformValidationReport(t *testing.T, repo *memory.Store, router *model.Router, testName, scenario, agentID, customerID string, sessionIDs []string) {
	t.Helper()
	if repo == nil {
		return
	}
	report := platformValidationReport{
		Scenario:     scenario,
		TestName:     testName,
		GeneratedAt:  time.Now().UTC(),
		LiveProvider: hasLiveProvider(),
		AgentID:      agentID,
		CustomerID:   customerID,
	}
	if router != nil {
		report.ProviderStats = router.Snapshot()
	}
	for _, sessionID := range sessionIDs {
		events, err := repo.ListEvents(context.Background(), sessionID)
		if err != nil {
			continue
		}
		var transcript []platformValidationTranscript
		for _, event := range events {
			transcript = append(transcript, platformValidationTranscript{
				EventID:     event.ID,
				Source:      event.Source,
				Kind:        event.Kind,
				Text:        eventText(event),
				TraceID:     event.TraceID,
				ExecutionID: event.ExecutionID,
				CreatedAt:   event.CreatedAt,
			})
		}
		sort.Slice(transcript, func(i, j int) bool { return transcript[i].CreatedAt.Before(transcript[j].CreatedAt) })
		execs, _ := repo.ListExecutions(context.Background())
		var sessionExecs []execution.TurnExecution
		scorecards := map[string]quality.Scorecard{}
		qualityPayloads := map[string]platformValidationQualityPayload{}
		for _, item := range execs {
			if item.SessionID == sessionID {
				sessionExecs = append(sessionExecs, item)
				srv := &Server{store: repo, router: router}
				if payload, err := srv.executionQualityPayload(context.Background(), item, ""); err == nil {
					if card, ok := payload["scorecard"].(quality.Scorecard); ok {
						scorecards[item.ID] = card
					}
					qualityPayloads[item.ID] = platformValidationQualityPayload{
						Plan:            typedQualityPayload[quality.ResponsePlan](payload, "plan"),
						Claims:          typedQualityPayload[[]quality.ResponseClaim](payload, "claims"),
						EvidenceMatches: typedQualityPayload[[]quality.EvidenceMatch](payload, "evidence_matches"),
						Scorecard:       typedQualityPayload[quality.Scorecard](payload, "scorecard"),
						HardFailed:      boolQualityPayload(payload, "hard_failed"),
					}
				}
			}
		}
		sort.Slice(sessionExecs, func(i, j int) bool { return sessionExecs[i].CreatedAt.Before(sessionExecs[j].CreatedAt) })
		report.Sessions = append(report.Sessions, platformValidationSession{
			ID:         sessionID,
			Transcript: transcript,
			Executions: sessionExecs,
			Scorecards: scorecards,
			Quality:    qualityPayloads,
		})
	}
	if agentID != "" && customerID != "" {
		prefs, _ := repo.ListCustomerPreferences(context.Background(), customer.PreferenceQuery{
			AgentID:        agentID,
			CustomerID:     customerID,
			IncludeExpired: true,
			Limit:          1000,
		})
		report.Preferences = prefs
	}
	if customerID != "" {
		items, _ := repo.ListFeedbackRecords(context.Background(), feedback.Query{SessionID: "", Limit: 1000})
		for _, item := range items {
			if len(sessionIDs) == 0 || containsString(sessionIDs, item.SessionID) {
				report.Feedback = append(report.Feedback, item)
			}
		}
	}
	if agentID != "" {
		knowledgeItems, _ := repo.ListKnowledgeUpdateProposals(context.Background(), "agent", agentID)
		report.Knowledge = knowledgeItems
	}
	policyItems, _ := repo.ListProposals(context.Background())
	for _, item := range policyItems {
		if containsAnyString(sessionIDs, item.EvidenceRefs) || strings.Contains(item.SourceBundleID, "bundle") || strings.Contains(item.SourceBundleID, "ecommerce_support") || strings.Contains(item.SourceBundleID, "language_bundle") || strings.Contains(item.SourceBundleID, "preference_review_bundle") {
			report.PolicyProposals = append(report.PolicyProposals, item)
		}
	}
	dir := strings.TrimSpace(os.Getenv("PLATFORM_VALIDATION_REPORT_DIR"))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "parmesan-platform-validation")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("platform validation report mkdir error: %v", err)
		return
	}
	filename := sanitizeTestName(testName) + ".json"
	path := filepath.Join(dir, filename)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("platform validation report encode error: %v", err)
		return
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Logf("platform validation report write error: %v", err)
		return
	}
	t.Logf("platform validation report written to %s", path)
}

func writePlatformValidationScorecardReport(t *testing.T, testName, scenario string, router *model.Router, card quality.Scorecard) {
	t.Helper()
	report := platformValidationReport{
		Scenario:     scenario,
		TestName:     testName,
		GeneratedAt:  time.Now().UTC(),
		LiveProvider: hasLiveProvider(),
		Sessions: []platformValidationSession{{
			ID: "catalog_" + sanitizeTestName(scenario),
			Scorecards: map[string]quality.Scorecard{
				"exec_" + sanitizeTestName(scenario): card,
			},
			Quality: map[string]platformValidationQualityPayload{
				"exec_" + sanitizeTestName(scenario): {Scorecard: card, Claims: card.Claims, EvidenceMatches: card.EvidenceMatches, HardFailed: quality.HardFailed(card)},
			},
		}},
	}
	if router != nil {
		report.ProviderStats = router.Snapshot()
	}
	writePlatformValidationReportFile(t, report)
}

func writePlatformValidationReportFile(t *testing.T, report platformValidationReport) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("PLATFORM_VALIDATION_REPORT_DIR"))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "parmesan-platform-validation")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("platform validation report mkdir error: %v", err)
		return
	}
	filename := sanitizeTestName(report.TestName) + ".json"
	path := filepath.Join(dir, filename)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("platform validation report encode error: %v", err)
		return
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Logf("platform validation report write error: %v", err)
		return
	}
	t.Logf("platform validation report written to %s", path)
}

func typedQualityPayload[T any](payload map[string]any, key string) T {
	var zero T
	value, ok := payload[key].(T)
	if !ok {
		return zero
	}
	return value
}

func boolQualityPayload(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func eventText(event session.Event) string {
	var parts []string
	for _, part := range event.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func hasLiveProvider() bool {
	return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" || strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func sanitizeTestName(name string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	clean := re.ReplaceAllString(name, "_")
	return strings.Trim(clean, "_")
}

func containsAnyString(needles []string, haystack []string) bool {
	for _, needle := range needles {
		for _, item := range haystack {
			if strings.Contains(item, needle) {
				return true
			}
		}
	}
	return false
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func validationTimeout(base time.Duration) time.Duration {
	if hasLiveProvider() {
		scaled := base * 15
		if scaled < 90*time.Second {
			return 90 * time.Second
		}
		return scaled
	}
	return base
}
