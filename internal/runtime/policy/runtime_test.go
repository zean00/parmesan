package policyruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
)

func TestResolveBuildsARQDrivenView(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I need to return my damaged order"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Observations: []policy.Observation{{
				ID:   "obs_damaged",
				When: "damaged order",
				MCP:  &policy.MCPRef{Server: "commerce"},
			}},
			Guidelines: []policy.Guideline{{
				ID:   "guide_returns",
				When: "return order",
				Then: "Help the customer start a return.",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "guide_returns", ToolID: "commerce.*"},
				{GuidelineID: "journey_node:return_flow:collect_reason", ToolID: "commerce.get_order"},
			},
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return order", "damaged order"},
				States: []policy.JourneyNode{{
					ID:          "collect_reason",
					Type:        "message",
					Instruction: "Ask the customer why they want to return the item.",
					Next:        []string{"fetch_order"},
				}},
			}},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "commerce_get_order", ProviderID: "commerce", Name: "get_order"},
			{ID: "commerce_get_return", ProviderID: "commerce", Name: "get_return_status"},
			{ID: "payments_get_refund", ProviderID: "payments", Name: "get_refund"},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ActiveJourney == nil || view.ActiveJourney.ID != "return_flow" {
		t.Fatalf("active journey = %#v, want return_flow", view.ActiveJourney)
	}
	if view.ActiveJourneyState == nil || view.ActiveJourneyState.ID != "collect_reason" {
		t.Fatalf("active journey state = %#v, want collect_reason", view.ActiveJourneyState)
	}
	if len(view.ExposedTools) != 2 {
		t.Fatalf("exposed tools = %#v, want 2 commerce tools", view.ExposedTools)
	}
	if len(view.ARQResults) < 4 {
		t.Fatalf("ARQ results = %#v, want staged outputs", view.ARQResults)
	}
	if view.Attention.CriticalInstructionIDs[0] != "guide_returns" {
		t.Fatalf("attention critical ids = %#v, want guide_returns", view.Attention.CriticalInstructionIDs)
	}
	if len(view.BatchResults) == 0 || len(view.PromptSetVersions) == 0 {
		t.Fatalf("batch metadata = %#v / %#v, want matcher batch results and prompt versions", view.BatchResults, view.PromptSetVersions)
	}
}

func TestResolveWithCustomMatcherUsesCustomStrategy(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "return order"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:      "returns",
				When:    "return order",
				Then:    "Help with returns",
				Matcher: "custom",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	found := false
	for _, batch := range view.BatchResults {
		if batch.Name == "custom_actionable_match" && batch.Strategy == "custom" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("batch results = %#v, want custom strategy batch", view.BatchResults)
	}
}

func TestPromptBuildersIncludeShotExamples(t *testing.T) {
	obs := buildObservationPrompt(MatchingContext{LatestCustomerText: "hello"}, []policy.Observation{{ID: "obs_1", When: "damaged order"}})
	act := buildActionablePrompt(MatchingContext{LatestCustomerText: "hello"}, []policy.Guideline{{ID: "g_1", When: "refund", Then: "help"}})
	if !strings.Contains(obs, "Examples:") || !strings.Contains(obs, "Input:") {
		t.Fatalf("observation prompt = %q, want shot examples", obs)
	}
	if !strings.Contains(act, "Examples:") || !strings.Contains(act, "Input:") {
		t.Fatalf("actionable prompt = %q, want shot examples", act)
	}
}

func TestResolveSuppressesAlreadyAppliedGuidelineWithoutNewTrigger(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-time.Minute),
				Content:   []session.ContentPart{{Type: "text", Text: "return order"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-30 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Help with returns"}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "return order"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "returns",
				When: "return order",
				Then: "Help with returns",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.MatchedGuidelines) != 0 {
		t.Fatalf("matched guidelines = %#v, want none because guideline was already applied", view.MatchedGuidelines)
	}
	if len(view.ReapplyDecisions) != 1 || view.ReapplyDecisions[0].ShouldReapply {
		t.Fatalf("reapply decisions = %#v, want no reapply", view.ReapplyDecisions)
	}
}

func TestResolveBacktracksJourneyWhenStateNoLongerMatches(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "actually I want to change the item"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return order"},
				States: []policy.JourneyNode{
					{ID: "collect_reason", Type: "message", Next: []string{"fetch_order"}},
					{ID: "fetch_order", Type: "message", When: []string{"return order"}, Next: []string{"complete"}},
					{ID: "complete", Type: "message"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "fetch_order",
			Path:      []string{"collect_reason", "fetch_order"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.JourneyDecision.Action != "backtrack" || view.JourneyDecision.BacktrackTo != "collect_reason" {
		t.Fatalf("journey decision = %#v, want backtrack to collect_reason", view.JourneyDecision)
	}
}

func TestResolveRestartsJourneyFromRootForNewPurpose(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I want a different item instead"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return order"},
				States: []policy.JourneyNode{
					{ID: "collect_reason", Type: "message", Next: []string{"fetch_order"}},
					{ID: "fetch_order", Type: "message", When: []string{"return order"}, Next: []string{"complete"}},
					{ID: "complete", Type: "message"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "fetch_order",
			Path:      []string{"collect_reason", "fetch_order"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.JourneyDecision.Action != "backtrack" || view.JourneyDecision.BacktrackTo != "collect_reason" {
		t.Fatalf("journey decision = %#v, want restart from root state", view.JourneyDecision)
	}
}

func TestResolveKeepsGuidelineWhenJourneyDependencyUsesJourneyPrefix(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I want to book a flight from Tel Aviv to JFK and I'm 19."}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{
					ID:   "under_21",
					When: "customer wants to book a flight and the traveler is under 21",
					Then: "inform the customer that only economy class is available",
				},
				{
					ID:   "age_21_or_older",
					When: "customer wants to book a flight and the traveler is 21 or older",
					Then: "tell the customer they may choose between economy and business class",
				},
			},
			Journeys: []policy.Journey{{
				ID:   "Book Flight",
				When: []string{"book a flight"},
				States: []policy.JourneyNode{
					{ID: "ask_origin", Type: "message", Next: []string{"ask_destination"}},
					{ID: "ask_destination", Type: "message"},
				},
			}},
			Relationships: []policy.Relationship{
				{Kind: "dependency", Source: "under_21", Target: "journey:Book Flight"},
				{Kind: "dependency", Source: "age_21_or_older", Target: "journey:Book Flight"},
				{Kind: "priority", Source: "under_21", Target: "age_21_or_older"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ActiveJourney == nil || view.ActiveJourney.ID != "Book Flight" {
		t.Fatalf("active journey = %#v, want Book Flight", view.ActiveJourney)
	}
	if !containsGuideline(view.MatchedGuidelines, "under_21") {
		t.Fatalf("matched guidelines = %#v, want under_21", view.MatchedGuidelines)
	}
	if containsGuideline(view.MatchedGuidelines, "age_21_or_older") {
		t.Fatalf("matched guidelines = %#v, do not want age_21_or_older", view.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "age_21_or_older") {
		t.Fatalf("suppressed guidelines = %#v, want age_21_or_older", view.SuppressedGuidelines)
	}
}

func TestResolveSuppressesWeakerSiblingGuidelinesDuringDisambiguation(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I lost my card"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "report_lost", When: "the customer wants to report a card lost", Then: "report card lost"},
				{ID: "lock_card", When: "the customer wants to lock their card", Then: "do locking"},
				{ID: "replacement_card", When: "the customer requests a replacement card", Then: "order them a new card"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuideline(view.MatchedGuidelines, "report_lost") {
		t.Fatalf("matched guidelines = %#v, want report_lost", view.MatchedGuidelines)
	}
	if containsGuideline(view.MatchedGuidelines, "lock_card") || containsGuideline(view.MatchedGuidelines, "replacement_card") {
		t.Fatalf("matched guidelines = %#v, do not want weaker sibling card actions", view.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "lock_card") || !containsSuppressedGuideline(view.SuppressedGuidelines, "replacement_card") {
		t.Fatalf("suppressed guidelines = %#v, want lock_card and replacement_card", view.SuppressedGuidelines)
	}
}

func TestResolveChoosesBestJourneyFollowUp(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "my item is damaged and I need help"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "support_flow",
				When: []string{"help"},
				States: []policy.JourneyNode{
					{ID: "start", Type: "message", Next: []string{"refund_path", "damage_path"}},
					{ID: "refund_path", Type: "message", When: []string{"refund"}, Instruction: "Handle refund questions"},
					{ID: "damage_path", Type: "message", When: []string{"damaged item"}, Instruction: "Handle damage claims"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "support_flow",
			StateID:   "start",
			Path:      []string{"start"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.JourneyDecision.Action != "advance" || view.JourneyDecision.NextState != "damage_path" {
		t.Fatalf("journey decision = %#v, want advance to damage_path", view.JourneyDecision)
	}
}

func TestResolveBlocksCustomerDependentGuidelineWithoutClarification(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "help"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "collect_reason",
				When:  "help",
				Then:  "Ask the customer for the reason",
				Scope: "customer",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.MatchedGuidelines) != 0 {
		t.Fatalf("matched guidelines = %#v, want none because customer clarification is missing", view.MatchedGuidelines)
	}
	if len(view.CustomerDecisions) != 1 || len(view.CustomerDecisions[0].MissingCustomerData) == 0 {
		t.Fatalf("customer decisions = %#v, want blocked customer-dependent guideline", view.CustomerDecisions)
	}
}

func TestResolveMatchesLowCriticalityGuidelineWhenStillRelevant(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "I still need help with my damaged order details"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "core_damage", When: "damaged order", Then: "Help with the damage claim", Priority: 1},
				{ID: "gentle_followup", When: "damaged order details", Then: "Offer a concise follow-up suggestion", Priority: -1},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	found := false
	for _, item := range view.MatchedGuidelines {
		if item.ID == "gentle_followup" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("matched guidelines = %#v, want low-criticality guideline to remain active", view.MatchedGuidelines)
	}
	arqFound := false
	for _, item := range view.ARQResults {
		if item.Name == "low_criticality_match" {
			arqFound = true
			break
		}
	}
	if !arqFound {
		t.Fatalf("ARQ results = %#v, want low_criticality_match stage", view.ARQResults)
	}
}

func TestResolveWithRouterUsesStructuredARQOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"returns\",\"applies\":true,\"rationale\":\"structured yes\"}]}"}}]}`))
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "returns",
				When: "return order",
				Then: "Help with returns",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if len(view.MatchedGuidelines) != 1 || view.MatchedGuidelines[0].ID != "returns" {
		t.Fatalf("matched guidelines = %#v, want structured-selected returns", view.MatchedGuidelines)
	}
	if len(view.GuidelineMatches) != 1 || view.GuidelineMatches[0].Rationale != "structured yes" {
		t.Fatalf("guideline matches = %#v, want structured rationale", view.GuidelineMatches)
	}
}

func TestResolveWithRouterRetriesStructuredARQ(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"returns\",\"applies\":true,\"rationale\":\"retried yes\"}]}"}}]}`))
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "returns",
				When: "return order",
				Then: "Help with returns",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("structured provider calls = %d, want retry", calls)
	}
	if len(view.MatchedGuidelines) != 1 || view.GuidelineMatches[0].Rationale != "retried yes" {
		t.Fatalf("view = %#v, want retried structured match", view)
	}
}

func TestResolveWithRouterUsesStructuredToolDecision(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"lookup\",\"applies\":true,\"rationale\":\"structured yes\"}]}"}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"selected_tool\":\"get_return_status\",\"approval_required\":false,\"arguments\":{\"reason\":\"status\"},\"rationale\":\"status tool is the best fit\"}"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[]}"}}]}`))
		}
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "what is my return status?"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "return status",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_order"},
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "commerce_get_order", ProviderID: "commerce", Name: "get_order"},
			{ID: "commerce_get_return_status", ProviderID: "commerce", Name: "get_return_status"},
		},
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if view.ToolDecision.SelectedTool != "get_return_status" {
		t.Fatalf("selected tool = %q, want get_return_status", view.ToolDecision.SelectedTool)
	}
	if view.ToolDecision.Arguments["reason"] != "status" {
		t.Fatalf("tool args = %#v, want structured tool arguments", view.ToolDecision.Arguments)
	}
}

func TestResolveMarksToolAsNotRunnableWhenRequiredArgsMissing(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "check my return"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "check return",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:         "commerce_get_return_status",
			ProviderID: "commerce",
			Name:       "get_return_status",
			Schema:     `{"type":"object","properties":{"return_id":{"type":"string"}},"required":["return_id"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want cannot run", view.ToolDecision)
	}
	if len(view.ToolDecision.MissingArguments) != 1 || view.ToolDecision.MissingArguments[0] != "return_id" {
		t.Fatalf("missing arguments = %#v, want return_id", view.ToolDecision.MissingArguments)
	}
	if len(view.ToolDecision.MissingIssues) != 1 || view.ToolDecision.MissingIssues[0].Significance != "critical" {
		t.Fatalf("missing issues = %#v, want one critical issue", view.ToolDecision.MissingIssues)
	}
}

func TestResolveDerivesHiddenToolArgumentWithoutBlocking(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "check my return"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "check return",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:         "commerce_get_return_status",
			ProviderID: "commerce",
			Name:       "get_return_status",
			Schema:     `{"type":"object","properties":{"session_id":{"type":"string","x-hidden":true},"customer_message":{"type":"string","x-hidden":true}},"required":["session_id","customer_message"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want hidden fields derived automatically", view.ToolDecision)
	}
	if len(view.ToolDecision.MissingIssues) != 0 {
		t.Fatalf("missing issues = %#v, want none", view.ToolDecision.MissingIssues)
	}
	if got := view.ToolDecision.Arguments["session_id"]; got != "sess_1" {
		t.Fatalf("session_id arg = %#v, want sess_1", got)
	}
}

func TestResolveAppliesToolDefaultArguments(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "check my return"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "check return",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:         "commerce_get_return_status",
			ProviderID: "commerce",
			Name:       "get_return_status",
			Schema:     `{"type":"object","properties":{"return_id":{"type":"string"},"locale":{"type":"string","default":"en","enum":["en","id"]}},"required":["return_id"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := view.ToolDecision.Arguments["locale"]; got != "en" {
		t.Fatalf("locale arg = %#v, want default en", got)
	}
}

func TestResolveBlocksWhenHiddenRequiredArgumentCannotBeDerived(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "check my return"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "check return",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:         "commerce_get_return_status",
			ProviderID: "commerce",
			Name:       "get_return_status",
			Schema:     `{"type":"object","properties":{"tenant_id":{"type":"string","x-hidden":true}},"required":["tenant_id"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want hidden unresolved arg to block invocation", view.ToolDecision)
	}
	if len(view.ToolDecision.MissingIssues) != 1 || !view.ToolDecision.MissingIssues[0].Hidden {
		t.Fatalf("missing issues = %#v, want one hidden missing issue", view.ToolDecision.MissingIssues)
	}
	if view.ToolDecision.MissingIssues[0].Significance != "internal" {
		t.Fatalf("missing issue = %#v, want internal significance", view.ToolDecision.MissingIssues[0])
	}
}

func TestResolveReportsInvalidToolArgumentWithChoicesAndSignificance(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"lookup\",\"applies\":true,\"rationale\":\"structured yes\"}]}"}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"selected_tool\":\"get_return_status\",\"approval_required\":false,\"arguments\":{\"return_id\":\"ret_1\",\"channel\":\"email\"},\"rationale\":\"need to check status\"}"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[]}"}}]}`))
		}
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "check my return"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup",
				When: "check return",
				Then: "Check the return status first",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup", ToolID: "commerce.get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:         "commerce_get_return_status",
			ProviderID: "commerce",
			Name:       "get_return_status",
			Schema:     `{"type":"object","properties":{"return_id":{"type":"string"},"channel":{"type":"string","enum":["web","sms"],"x-parmesan-significance":"critical"},"locale":{"type":"string","default":"en","enum":["en","id"]}},"required":["return_id","channel"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want invalid channel to block invocation", view.ToolDecision)
	}
	if len(view.ToolDecision.InvalidIssues) != 1 {
		t.Fatalf("invalid issues = %#v, want one invalid issue", view.ToolDecision.InvalidIssues)
	}
	issue := view.ToolDecision.InvalidIssues[0]
	if issue.Parameter != "channel" || issue.Significance != "critical" {
		t.Fatalf("invalid issue = %#v, want critical channel issue", issue)
	}
	if len(issue.Choices) != 2 || issue.Choices[0] != "web" || issue.Choices[1] != "sms" {
		t.Fatalf("invalid issue choices = %#v, want web/sms", issue.Choices)
	}
}

func TestResolveAppliesIterativeRelationships(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "cancel my return please"}},
		}},
		[]policy.Bundle{{
			ID:              "bundle_1",
			Version:         "v1",
			CompositionMode: "strict",
			NoMatch:         "I need a bit more detail before I can continue.",
			Guidelines: []policy.Guideline{
				{ID: "return_flow", When: "return", Then: "Help with returns"},
				{ID: "cancel_return", When: "cancel return", Then: "Cancel the return", Priority: 1},
				{ID: "cancel_confirm", When: "cancel", Then: "Confirm the cancellation"},
			},
			Relationships: []policy.Relationship{
				{Source: "cancel_return", Kind: "priority", Target: "return_flow"},
				{Source: "cancel_return", Kind: "entails", Target: "cancel_confirm"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.MatchedGuidelines) != 2 {
		t.Fatalf("matched guidelines = %#v, want cancel_return + cancel_confirm", view.MatchedGuidelines)
	}
	if view.MatchedGuidelines[0].ID != "cancel_return" {
		t.Fatalf("top matched guideline = %#v, want cancel_return", view.MatchedGuidelines)
	}
	if len(view.SuppressedGuidelines) != 1 || view.SuppressedGuidelines[0].ID != "return_flow" {
		t.Fatalf("suppressed guidelines = %#v, want return_flow suppressed", view.SuppressedGuidelines)
	}
}

func TestAdvanceJourneyUsesDecisionNextState(t *testing.T) {
	instance := &journey.Instance{
		ID:        "journey_1",
		SessionID: "sess_1",
		JourneyID: "return_flow",
		StateID:   "collect_reason",
		Path:      []string{"collect_reason"},
		Status:    journey.StatusActive,
		UpdatedAt: time.Now().UTC(),
	}
	state := &policy.JourneyNode{ID: "collect_reason", Type: "tool", Next: []string{"fetch_order"}}
	flow := &policy.Journey{ID: "return_flow"}
	next := AdvanceJourney(instance, state, flow, JourneyDecision{Action: "advance", NextState: "fetch_order"})
	if next.StateID != "fetch_order" {
		t.Fatalf("journey next state = %s, want fetch_order", next.StateID)
	}
	if len(next.Path) != 2 || next.Path[1] != "fetch_order" {
		t.Fatalf("journey path = %#v, want appended fetch_order", next.Path)
	}
}

func TestAdvanceJourneyTrimsPathOnBacktrack(t *testing.T) {
	instance := &journey.Instance{
		ID:        "journey_1",
		SessionID: "sess_1",
		JourneyID: "return_flow",
		StateID:   "review",
		Path:      []string{"collect_reason", "fetch_order", "review"},
		Status:    journey.StatusActive,
		UpdatedAt: time.Now().UTC(),
	}
	flow := &policy.Journey{
		ID: "return_flow",
		States: []policy.JourneyNode{
			{ID: "collect_reason"},
			{ID: "fetch_order"},
			{ID: "review"},
		},
	}
	state := &policy.JourneyNode{ID: "review"}
	next := AdvanceJourney(instance, state, flow, JourneyDecision{Action: "backtrack", BacktrackTo: "fetch_order"})
	if next.StateID != "fetch_order" {
		t.Fatalf("journey state = %s, want fetch_order", next.StateID)
	}
	if len(next.Path) != 2 || next.Path[0] != "collect_reason" || next.Path[1] != "fetch_order" {
		t.Fatalf("journey path = %#v, want trimmed path", next.Path)
	}
}

func TestResolveRejectsIllegalBacktrackTargetNotInVisitedPath(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "actually change the item"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return order"},
				States: []policy.JourneyNode{
					{ID: "start", Type: "message", Next: []string{"collect_reason"}},
					{ID: "collect_reason", Type: "message", Next: []string{"fetch_order"}},
					{ID: "fetch_order", Type: "message", When: []string{"return order"}, Next: []string{"complete"}},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "fetch_order",
			Path:      []string{"fetch_order"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.JourneyDecision.Action == "backtrack" {
		t.Fatalf("journey decision = %#v, want no illegal backtrack to an unvisited state", view.JourneyDecision)
	}
}

func TestVerifyDraftEnforcesStrictTemplate(t *testing.T) {
	view := ResolvedView{
		CompositionMode: "strict",
		NoMatch:         "Need more detail.",
		CandidateTemplates: []policy.Template{{
			ID:   "tmpl_1",
			Mode: "strict",
			Text: "Your return is approved.",
		}},
	}
	got := VerifyDraft(view, "Something else", nil)
	if got.Status != "revise" || got.Replacement != "Your return is approved." {
		t.Fatalf("VerifyDraft() = %#v, want strict replacement", got)
	}
}

func TestVerifyDraftUsesResponseAnalysisTemplate(t *testing.T) {
	view := ResolvedView{
		ResponseAnalysis: ResponseAnalysis{
			RecommendedTemplate: "Use this approved answer.",
		},
	}
	got := VerifyDraft(view, "Something else", nil)
	if got.Status != "revise" || got.Replacement != "Use this approved answer." {
		t.Fatalf("VerifyDraft() = %#v, want response-analysis replacement", got)
	}
}

func TestVerifyDraftAllowsStrictJourneyInstructionFallback(t *testing.T) {
	view := ResolvedView{
		CompositionMode: "strict",
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "ask_origin",
			Instruction: "From where are you looking to fly?",
		},
	}
	got := VerifyDraft(view, "Something else", nil)
	if got.Status != "revise" || got.Replacement != "From where are you looking to fly?" {
		t.Fatalf("VerifyDraft() = %#v, want strict journey instruction replacement", got)
	}
}

func TestExtractMentionedAgeRequiresExplicitAgeContext(t *testing.T) {
	cases := []struct {
		text string
		want int
		ok   bool
	}{
		{text: "I'm 19 if that affects anything.", want: 19, ok: true},
		{text: "Age 21 and over only.", want: 21, ok: true},
		{text: "Main Street 1234", want: 0, ok: false},
		{text: "flight 18 leaves tomorrow", want: 0, ok: false},
		{text: "return on 12.10", want: 0, ok: false},
	}
	for _, tc := range cases {
		got, ok := extractMentionedAge(tc.text)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("extractMentionedAge(%q) = (%d,%t), want (%d,%t)", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestResolveDoesNotSkipJourneyStepsFromGenericFixtureHeuristics(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I want to book a flight from Main Street 1234 to JFK on 12.10"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "Book Flight",
				When: []string{"book a flight"},
				States: []policy.JourneyNode{
					{ID: "ask_origin", Type: "message", Next: []string{"ask_destination"}},
					{ID: "ask_destination", Type: "message", Next: []string{"ask_dates"}},
					{ID: "ask_dates", Type: "message", Next: []string{"ask_class"}},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "Book Flight",
			StateID:   "ask_origin",
			Path:      []string{"ask_origin"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.JourneyDecision.Action == "advance" && view.JourneyDecision.NextState != "ask_destination" {
		t.Fatalf("journey decision = %#v, want no heuristic skip beyond immediate next state", view.JourneyDecision)
	}
}

func TestResolveWithRouterSupportsResponseAnalysisARQ(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"returns\",\"applies\":true,\"rationale\":\"structured yes\"}]}"}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"needs_revision\":true,\"needs_strict_mode\":true,\"recommended_template\":\"Approved strict template.\",\"rationale\":\"strict policy should use approved template\",\"analyzed_guidelines\":[{\"id\":\"returns\",\"already_satisfied\":false,\"requires_response\":true,\"requires_template\":true,\"rationale\":\"must answer with template\"}]}"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[]}"}}]}`))
		}
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there"}},
		}},
		[]policy.Bundle{{
			ID:              "bundle_1",
			Version:         "v1",
			CompositionMode: "guided",
			Guidelines: []policy.Guideline{{
				ID:   "returns",
				When: "return order",
				Then: "Help with returns",
			}},
			Templates: []policy.Template{{
				ID:   "tmpl_1",
				Mode: "strict",
				Text: "Approved strict template.",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if !view.ResponseAnalysis.NeedsStrictMode || view.ResponseAnalysis.RecommendedTemplate != "Approved strict template." {
		t.Fatalf("response analysis = %#v, want strict template recommendation", view.ResponseAnalysis)
	}
	if view.CompositionMode != "strict" {
		t.Fatalf("composition mode = %s, want strict after response analysis", view.CompositionMode)
	}
}

func containsGuideline(items []policy.Guideline, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func containsSuppressedGuideline(items []SuppressedGuideline, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
