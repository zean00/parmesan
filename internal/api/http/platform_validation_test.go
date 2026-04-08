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
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/quality"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/runtime/runner"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

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
	scenarios := []struct {
		id       string
		response string
		view     policyruntime.EngineResult
	}{
		{
			id:       "ecommerce_knowledge_grounding_damaged_toaster_replacem",
			response: "Damaged toaster purchases within 30 days qualify for replacement review after verification.",
			view:     liveGateKnowledgeView("Damaged toaster purchases within 30 days qualify for replacement review after verification."),
		},
		{
			id:       "ecommerce_knowledge_grounding_refund_timing_question",
			response: "Refund timing starts after order verification and policy review.",
			view:     liveGateKnowledgeView("Refund timing starts after order verification and policy review."),
		},
		{
			id:       "pet_store_topic_scope_human_cooking_question",
			response: "I can help with pet-store questions, but not cooking or human food. If you want, I can help with pet-safe food options.",
			view:     liveGateBoundaryView("out_of_scope", "refuse", "I can help with pet-store questions, but not cooking or human food. If you want, I can help with pet-safe food options."),
		},
		{
			id:       "pet_store_topic_scope_pet_food_question",
			response: "I can help compare pet food options within the store catalog.",
			view: policyruntime.EngineResult{
				Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{AllowedTopics: []string{"pet food"}}},
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "in_scope", Action: "allow"},
			},
		},
		{
			id:       "support_multilingual_english_fallback",
			response: "I can help with your notification options in English.",
			view:     policyruntime.EngineResult{Bundle: &policy.Bundle{Soul: policy.Soul{DefaultLanguage: "en"}}},
		},
		{
			id:       "support_multilingual_respond_in_indonesian",
			response: "Saya bisa membantu Anda dengan pilihan notifikasi untuk pesanan.",
			view:     policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_language_id", Key: "preferred_language", Value: "indonesian"}}},
		},
		{
			id:       "support_preference_call_me_rina",
			response: "Rina, I can help with your account updates.",
			view:     policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_name_rina", Key: "preferred_name", Value: "Rina"}}},
		},
		{
			id:       "support_preference_prefer_email",
			response: "I will keep email as your preferred update channel.",
			view:     policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_email", Key: "contact_channel", Value: "email"}}},
		},
		{
			id:       "support_refusal_escalation_operator_handoff",
			response: "I need to bring in a human operator for this. They will review the conversation and continue from here.",
			view: policyruntime.EngineResult{
				MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "handoff", Then: "Escalate to a human operator when the customer asks for operator support."}}},
			},
		},
		{
			id:       "support_refusal_escalation_unsafe_request",
			response: "I cannot help with that request, but I can help with safe support options.",
			view:     liveGateBoundaryView("out_of_scope", "refuse", "I cannot help with that request, but I can help with safe support options."),
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.id, func(t *testing.T) {
			card := quality.GradeWithLLM(context.Background(), router, scenario.view, scenario.response, nil)
			if quality.HardFailed(card) || !card.Passed || card.Overall < 0.7 {
				t.Fatalf("scenario %s scorecard = %#v, want release-gate pass", scenario.id, card)
			}
			writePlatformValidationScorecardReport(t, t.Name(), scenario.id, router, card)
		})
	}
}

func liveGateKnowledgeView(evidence string) policyruntime.EngineResult {
	return policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        evidence,
			ResultHash:  "live_gate_knowledge",
			Citations:   []knowledge.Citation{{URI: "kb://live-gate"}},
		}}},
		MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{
			ID:   "verify_first",
			Then: "Verify the order before promising a refund or replacement.",
		}}},
	}
}

func liveGateBoundaryView(classification, action, reply string) policyruntime.EngineResult {
	return policyruntime.EngineResult{
		Bundle: &policy.Bundle{DomainBoundary: policy.DomainBoundary{
			BlockedTopics: []string{"human food", "cooking", "unsafe request"},
		}},
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: classification,
			Action:         action,
			Reply:          reply,
			Reasons:        []string{"live_gate_boundary"},
		},
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

type platformValidationReport struct {
	Scenario        string                      `json:"scenario"`
	TestName        string                      `json:"test_name"`
	GeneratedAt     time.Time                   `json:"generated_at"`
	LiveProvider    bool                        `json:"live_provider"`
	ProviderStats   []model.ProviderStats       `json:"provider_stats,omitempty"`
	AgentID         string                      `json:"agent_id,omitempty"`
	CustomerID      string                      `json:"customer_id,omitempty"`
	Sessions        []platformValidationSession `json:"sessions,omitempty"`
	Preferences     []customer.Preference       `json:"preferences,omitempty"`
	Feedback        []feedback.Record           `json:"feedback,omitempty"`
	Knowledge       []knowledge.UpdateProposal  `json:"knowledge_proposals,omitempty"`
	PolicyProposals []rollout.Proposal          `json:"policy_proposals,omitempty"`
}

type platformValidationSession struct {
	ID         string                                      `json:"id"`
	Transcript []platformValidationTranscript              `json:"transcript,omitempty"`
	Executions []execution.TurnExecution                   `json:"executions,omitempty"`
	Scorecards map[string]quality.Scorecard                `json:"scorecards,omitempty"`
	Quality    map[string]platformValidationQualityPayload `json:"quality,omitempty"`
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
