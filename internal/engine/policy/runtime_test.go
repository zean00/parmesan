package policyruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/engine/semantics"
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
	if len(view.ToolExposureStage.ExposedTools) != 2 {
		t.Fatalf("exposed tools = %#v, want 2 commerce tools", view.ToolExposureStage.ExposedTools)
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
	foundSized := false
	for _, item := range view.BatchResults {
		if item.BatchSize > 0 {
			foundSized = true
			break
		}
	}
	if !foundSized {
		t.Fatalf("batch results = %#v, want at least one batch with recorded size metadata", view.BatchResults)
	}
	if len(view.ObservationStage.Matches) == 0 || len(view.ObservationStage.Observations) == 0 {
		t.Fatalf("observation stage = %#v, want canonical observation stage on the resolved view", view.ObservationStage)
	}
	if len(view.MatchFinalizeStage.GuidelineMatches) == 0 {
		t.Fatalf("match finalize stage = %#v, want canonical finalize stage on the resolved view", view.MatchFinalizeStage)
	}
	if len(view.JourneyProgressStage.Evaluation.JourneySatisfactions) == 0 {
		t.Fatalf("journey satisfactions = %#v, want semantics artifacts on the resolved view", view.JourneyProgressStage.Evaluation.JourneySatisfactions)
	}
	if view.JourneyProgressStage.Decision.Action == "" {
		t.Fatalf("journey progress stage = %#v, want canonical stage result on the resolved view", view.JourneyProgressStage)
	}
	if len(view.JourneyBacktrackStage.Evaluation.BacktrackEvaluations) == 0 && len(view.JourneyProgressStage.Evaluation.NextNodeEvaluations) == 0 {
		t.Fatalf("journey evaluation artifacts = %#v / %#v, want persisted journey evaluation artifacts on the resolved view", view.JourneyBacktrackStage.Evaluation.BacktrackEvaluations, view.JourneyProgressStage.Evaluation.NextNodeEvaluations)
	}
	if len(view.JourneyProgressStage.Evaluation.NextNodeEvaluations) > 0 && view.JourneyProgressStage.Evaluation.SelectedNextNode.Selection.StateID == "" {
		t.Fatalf("journey progress stage = %#v, want selected next-node evaluation on the canonical stage result", view.JourneyProgressStage)
	}
	if len(view.ConditionArtifactsStage.Artifacts) == 0 {
		t.Fatalf("condition artifacts = %#v, want persisted condition evidence on the resolved view", view.ConditionArtifactsStage.Artifacts)
	}
	if len(view.ToolPlanStage.Evaluation.Grounding) == 0 || len(view.ToolPlanStage.Evaluation.SelectionEvidence) == 0 {
		t.Fatalf("tool semantics artifacts = %#v / %#v, want grounding and selection artifacts", view.ToolPlanStage.Evaluation.Grounding, view.ToolPlanStage.Evaluation.SelectionEvidence)
	}
	if len(view.ResponseAnalysisStage.Evaluation.AnalyzedGuidelines) == 0 {
		t.Fatalf("response analysis evaluation = %#v, want persisted response-analysis stage artifact", view.ResponseAnalysisStage.Evaluation)
	}
	if len(view.ResponseAnalysisStage.Analysis.AnalyzedGuidelines) == 0 {
		t.Fatalf("response analysis stage = %#v, want canonical response-analysis stage result on the resolved view", view.ResponseAnalysisStage)
	}
	if len(view.ToolPlanStage.Evaluation.Candidates) == 0 {
		t.Fatalf("tool plan evaluation = %#v, want persisted tool-plan stage artifact", view.ToolPlanStage.Evaluation)
	}
	if len(view.ToolPlanStage.Plan.Candidates) == 0 || view.ToolDecisionStage.Evaluation.FinalSelectedTool == "" {
		t.Fatalf("tool stage results = %#v / %#v, want canonical tool stage results on the resolved view", view.ToolPlanStage, view.ToolDecisionStage)
	}
	if len(view.ToolPlanStage.Evaluation.Batches) == 0 {
		t.Fatalf("tool batch evaluations = %#v, want persisted tool batch artifacts on the resolved view", view.ToolPlanStage.Evaluation.Batches)
	}
	if view.ToolDecisionStage.Evaluation.PlannedSelectedTool == "" {
		t.Fatalf("tool decision evaluation = %#v, want persisted tool decision artifact on the resolved view", view.ToolDecisionStage.Evaluation)
	}
}

func TestResolveEmitsUpdateIntentArtifacts(t *testing.T) {
	now := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "Please schedule an appointment tomorrow at 6pm and remind me about it."}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "schedule_visit", When: "appointment", Then: "schedule the appointment"},
				{ID: "send_reminder", When: "remind me", Then: "set a reminder"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "schedule_visit", ToolID: "commerce.schedule_appointment"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "commerce_schedule_appointment", ProviderID: "commerce", Name: "schedule_appointment"},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.UpdateIntents) != 1 {
		t.Fatalf("update intents = %#v, want one appointment reminder artifact", view.UpdateIntents)
	}
	item := view.UpdateIntents[0]
	if item.Kind != "appointment_reminder" || item.Source != "runtime" {
		t.Fatalf("update intent = %#v, want runtime appointment reminder", item)
	}
	if item.SubjectRef == "" || item.RemindAt == "" {
		t.Fatalf("update intent = %#v, want subject ref and remind_at", item)
	}
	foundARQ := false
	for _, arq := range view.ARQResults {
		if arq.Name == "update_intents" {
			foundARQ = true
			break
		}
	}
	if !foundARQ {
		t.Fatalf("ARQ results = %#v, want update_intents artifact", view.ARQResults)
	}
}

func TestResolveEmitsGenericWatchCapabilityArtifact(t *testing.T) {
	now := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "Please schedule an appointment tomorrow at 6pm and notify me about it."}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Semantics: policy.SemanticsPolicy{
				Signals: []policy.SemanticSignal{{ID: "custom_reminder", Tokens: []string{"schedule", "appointment", "notify"}}},
			},
			WatchCapabilities: []policy.WatchCapability{{
				ID:                  "custom_appointment_reminder",
				Kind:                "appointment_reminder",
				ScheduleStrategy:    "reminder",
				TriggerSignals:      []string{"custom_reminder"},
				ToolMatchTerms:      []string{"appointment", "schedule"},
				SubjectKeys:         []string{"appointment_id", "id"},
				ReminderLeadSeconds: 1800,
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "schedule", ToolID: "local.schedule_appointment"},
			},
			Guidelines: []policy.Guideline{{ID: "schedule", When: "appointment", Then: "Schedule the appointment"}},
		}},
		nil,
		[]tool.CatalogEntry{{ID: "local_schedule_appointment", ProviderID: "local", Name: "schedule_appointment"}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.UpdateIntents) != 1 {
		t.Fatalf("update intents = %#v, want one generic watch artifact", view.UpdateIntents)
	}
	if got := view.UpdateIntents[0]; got.CapabilityID != "custom_appointment_reminder" || got.Kind != "appointment_reminder" {
		t.Fatalf("update intent = %#v, want capability-backed artifact", got)
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
	foundFinalize := false
	for _, batch := range view.BatchResults {
		if batch.Name == "custom_actionable_match" && batch.Strategy == "custom" {
			found = true
		}
		if batch.Name == "match_finalize" && batch.Strategy == "custom" {
			foundFinalize = true
		}
	}
	if !found {
		t.Fatalf("batch results = %#v, want custom strategy batch", view.BatchResults)
	}
	if !foundFinalize {
		t.Fatalf("batch results = %#v, want custom strategy finalize batch", view.BatchResults)
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

func TestBuildMatchingContextIncludesModerationSignals(t *testing.T) {
	ctx := buildMatchingContext([]session.Event{{
		ID:        "evt_1",
		SessionID: "sess_1",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: time.Now().UTC(),
		Content:   []session.ContentPart{{Type: "text", Text: "Customer message censored due to unsafe or manipulative content."}},
		Metadata: map[string]any{
			"moderation": map[string]any{
				"mode":       "paranoid",
				"decision":   "censored",
				"categories": []any{"prompt_injection", "jailbreak"},
				"jailbreak":  true,
			},
		},
	}})
	if !ctx.LastMessageCensored {
		t.Fatalf("LastMessageCensored = false, want true")
	}
	if !ctx.LastMessageJailbreak {
		t.Fatalf("LastMessageJailbreak = false, want true")
	}
	if ctx.LastMessageModerationMode != "paranoid" {
		t.Fatalf("LastMessageModerationMode = %q, want paranoid", ctx.LastMessageModerationMode)
	}
	if !slices.Contains(ctx.DerivedSignals, "moderation:censored") || !slices.Contains(ctx.DerivedSignals, "moderation:jailbreak") {
		t.Fatalf("DerivedSignals = %#v, want moderation signals", ctx.DerivedSignals)
	}
	if !slices.Contains(ctx.DerivedSignals, "moderation:category:prompt_injection") {
		t.Fatalf("DerivedSignals = %#v, want prompt_injection category signal", ctx.DerivedSignals)
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
	if len(view.MatchFinalizeStage.MatchedGuidelines) != 0 {
		t.Fatalf("matched guidelines = %#v, want none because guideline was already applied", view.MatchFinalizeStage.MatchedGuidelines)
	}
	reapply := view.PreviouslyAppliedStage.Decisions
	if len(reapply) != 1 || reapply[0].ShouldReapply {
		t.Fatalf("reapply decisions = %#v, want no reapply", reapply)
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
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "backtrack" || journeyDecision.BacktrackTo != "collect_reason" {
		t.Fatalf("journey decision = %#v, want backtrack to collect_reason", journeyDecision)
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
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "backtrack" || journeyDecision.BacktrackTo != "collect_reason" {
		t.Fatalf("journey decision = %#v, want restart from root state", journeyDecision)
	}
}

func TestResolveBacktracksToRelevantVisitedBranchPoint(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "actually let's switch this to store pickup"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "order_fulfillment",
				When:   []string{"order"},
				RootID: "choose_method",
				States: []policy.JourneyNode{
					{ID: "choose_method", Type: "message", Instruction: "Ask whether they want delivery or pickup"},
					{ID: "ask_address", Type: "message", When: []string{"delivery"}, Instruction: "Ask for the delivery address"},
					{ID: "ask_time", Type: "message", When: []string{"delivery"}, Instruction: "Ask for delivery time"},
					{ID: "ask_store", Type: "message", When: []string{"pickup", "store"}, Instruction: "Ask which store they want"},
				},
				Edges: []policy.JourneyEdge{
					{ID: "edge_choose_address", Source: "choose_method", Target: "ask_address", Condition: "delivery"},
					{ID: "edge_choose_store", Source: "choose_method", Target: "ask_store", Condition: "pickup or store"},
					{ID: "edge_address_time", Source: "ask_address", Target: "ask_time"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "order_fulfillment",
			StateID:   "ask_time",
			Path:      []string{"choose_method", "ask_address", "ask_time"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "backtrack" || journeyDecision.BacktrackTo != "choose_method" {
		t.Fatalf("journey decision = %#v, want backtrack to choose_method branch point", journeyDecision)
	}
}

func TestResolveAdvancesJourneyToBestGraphFollowUp(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I'll pick it up from the downtown store"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "order_fulfillment",
				When:   []string{"order"},
				RootID: "choose_method",
				States: []policy.JourneyNode{
					{ID: "choose_method", Type: "message", Instruction: "Ask whether they want delivery or pickup"},
					{ID: "ask_address", Type: "message", When: []string{"delivery"}, Instruction: "Ask for the delivery address"},
					{ID: "ask_store", Type: "message", When: []string{"pickup", "store"}, Instruction: "Ask which store they want"},
				},
				Edges: []policy.JourneyEdge{
					{ID: "edge_choose_address", Source: "choose_method", Target: "ask_address", Condition: "delivery"},
					{ID: "edge_choose_store", Source: "choose_method", Target: "ask_store", Condition: "pickup or store"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "order_fulfillment",
			StateID:   "choose_method",
			Path:      []string{"choose_method"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "ask_store" {
		t.Fatalf("journey decision = %#v, want advance to ask_store", journeyDecision)
	}
}

func TestResolveBacktracksAndFastForwardsThroughSatisfiedFollowUps(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "Actually, can I change those to medium size instead of large? No drinks still."}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "calzone_journey",
				When:   []string{"order calzones"},
				RootID: "ask_quantity",
				States: []policy.JourneyNode{
					{ID: "ask_quantity", Type: "message", Instruction: "Ask how many calzones they want"},
					{ID: "ask_type", Type: "message", Instruction: "Ask what type of calzones they want", When: []string{"classic italian", "spinach", "chicken"}},
					{ID: "ask_size", Type: "message", Instruction: "Ask what size they want", When: []string{"small", "medium", "large"}},
					{ID: "ask_drinks", Type: "message", Instruction: "Ask whether they want any drinks", When: []string{"drinks", "no drinks"}},
					{ID: "check_stock", Type: "tool", Tool: "check_stock"},
				},
				Edges: []policy.JourneyEdge{
					{Source: "ask_quantity", Target: "ask_type"},
					{Source: "ask_type", Target: "ask_size"},
					{Source: "ask_size", Target: "ask_drinks"},
					{Source: "ask_drinks", Target: "check_stock"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "calzone_journey",
			StateID:   "check_stock",
			Path:      []string{"ask_quantity", "ask_type", "ask_size", "ask_drinks", "check_stock"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "backtrack" || journeyDecision.BacktrackTo != "ask_size" || journeyDecision.NextState != "check_stock" {
		t.Fatalf("journey decision = %#v, want backtrack to ask_size then fast-forward to check_stock", journeyDecision)
	}
}

func TestResolveBacktracksWithoutFastForwardingAcrossStaleHistory(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-6 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Hi, I'd like to book a taxi for myself"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-5 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Great! What's your pickup location?"}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-4 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Main street 1234"}},
			},
			{
				ID:        "evt_4",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-3 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Got it. What's your drop-off location?"}},
			},
			{
				ID:        "evt_5",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "3rd Avenue by the river"}},
			},
			{
				ID:        "evt_6",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-1 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Got it. What time would you like to pick up?"}},
			},
			{
				ID:        "evt_7",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Oh hold up, my plans have changed. I'm actually going to need a cab for my son, he'll be waiting at JFK airport, at the taxi stand."}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "Book Taxi Ride",
				When: []string{"book a taxi"},
				States: []policy.JourneyNode{
					{ID: "ask_pickup_location", Type: "message", Instruction: "What's your pickup location?", Next: []string{"ask_dropoff_location"}},
					{ID: "ask_dropoff_location", Type: "message", Instruction: "What's your drop-off location?", Next: []string{"ask_pickup_time"}},
					{ID: "ask_pickup_time", Type: "message", Instruction: "What time would you like to pick up?"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "Book Taxi Ride",
			StateID:   "ask_pickup_time",
			Path:      []string{"ask_pickup_location", "ask_dropoff_location", "ask_pickup_time"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "backtrack" || journeyDecision.BacktrackTo != "ask_dropoff_location" || journeyDecision.NextState != "" {
		t.Fatalf("journey decision = %#v, want backtrack to ask_dropoff_location without fast-forwarding on stale history", journeyDecision)
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
	if !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "under_21") {
		t.Fatalf("matched guidelines = %#v, want under_21", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "age_21_or_older") {
		t.Fatalf("matched guidelines = %#v, do not want age_21_or_older", view.MatchFinalizeStage.MatchedGuidelines)
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
	if !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "report_lost") {
		t.Fatalf("matched guidelines = %#v, want report_lost", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "lock_card") || containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "replacement_card") {
		t.Fatalf("matched guidelines = %#v, do not want weaker sibling card actions", view.MatchFinalizeStage.MatchedGuidelines)
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
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "damage_path" {
		t.Fatalf("journey decision = %#v, want advance to damage_path", journeyDecision)
	}
}

func TestResolveProjectsJourneyMetadataAndLegalFollowUps(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "help me reset my password"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "reset_password",
				When: []string{"reset password"},
				States: []policy.JourneyNode{
					{ID: "ask_email", Type: "message", Instruction: "What email is on the account?", Next: []string{"verify_code"}},
					{ID: "verify_code", Type: "message", Instruction: "What code did you receive?", Mode: "strict"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.ProjectedNodes) != 2 {
		t.Fatalf("projected nodes = %#v, want 2", view.ProjectedNodes)
	}
	first := view.ProjectedNodes[0]
	if first.ID != "journey_node:reset_password:ask_email" || first.Index != 1 {
		t.Fatalf("first projected node = %#v, want stable projected id and index", first)
	}
	if len(first.FollowUps) != 1 || first.FollowUps[0] != "journey_node:reset_password:verify_code:reset_password:ask_email->verify_code" {
		t.Fatalf("follow ups = %#v, want projected follow-up ids", first.FollowUps)
	}
	if len(first.LegalFollowUps) != 1 || first.LegalFollowUps[0] != "journey_node:reset_password:verify_code:reset_password:ask_email->verify_code" {
		t.Fatalf("legal follow ups = %#v, want projected legal follow-up ids", first.LegalFollowUps)
	}
	if first.Metadata["journey_node"] == nil {
		t.Fatalf("metadata = %#v, want journey_node metadata", first.Metadata)
	}
}

func TestResolveActivatesJourneyWithSyntheticRoot(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "I need help resetting my password"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "reset_password",
				When:   []string{"resetting my password", "reset my password"},
				RootID: "root",
				States: []policy.JourneyNode{
					{ID: "ask_email", Type: "message", Instruction: "What email is on the account?"},
					{ID: "verify_code", Type: "message", Instruction: "What code did you receive?"},
				},
				Edges: []policy.JourneyEdge{
					{ID: "edge_root_email", Source: "root", Target: "ask_email"},
					{ID: "edge_email_code", Source: "ask_email", Target: "verify_code"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ActiveJourney == nil || view.ActiveJourney.ID != "reset_password" {
		t.Fatalf("active journey = %#v, want reset_password", view.ActiveJourney)
	}
	if view.ActiveJourneyState == nil || view.ActiveJourneyState.ID != "ask_email" {
		t.Fatalf("active journey state = %#v, want ask_email", view.ActiveJourneyState)
	}
}

func TestResolveProjectsJourneyOrderWithStateRootFirst(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "reset my password"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "reset_password",
				When:   []string{"reset my password"},
				RootID: "ask_name",
				States: []policy.JourneyNode{
					{ID: "ask_name", Type: "message", Instruction: "What is your account name?"},
					{ID: "ask_email", Type: "message", Instruction: "What email is on the account?"},
					{ID: "verify_code", Type: "message", Instruction: "What code did you receive?"},
				},
				Edges: []policy.JourneyEdge{
					{Source: "ask_name", Target: "ask_email"},
					{Source: "ask_email", Target: "verify_code"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	projected := map[string]ProjectedJourneyNode{}
	for _, node := range view.ProjectedNodes {
		projected[node.StateID] = node
	}
	if projected["ask_name"].Index != 1 || projected["ask_email"].Index != 2 || projected["verify_code"].Index != 3 {
		t.Fatalf("projected node indices = %#v, want ask_name=1 ask_email=2 verify_code=3", projected)
	}
}

func TestResolveProjectsExplicitJourneyEdges(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "help me reset my password"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "reset_password",
				When:   []string{"reset password"},
				RootID: "root",
				States: []policy.JourneyNode{
					{ID: "ask_email", Type: "message", Instruction: "What email is on the account?", Labels: []string{"collect"}},
					{ID: "verify_code", Type: "message", Instruction: "What code did you receive?"},
				},
				Edges: []policy.JourneyEdge{
					{ID: "edge_root_email", Source: "root", Target: "ask_email", Metadata: map[string]any{"journey_node": map[string]any{"kind": "chat"}}},
					{ID: "edge_email_code", Source: "ask_email", Target: "verify_code", Condition: "customer provided email", Metadata: map[string]any{"journey_node": map[string]any{"edge_label": "after_email"}}},
				},
				Metadata: map[string]any{"scope": "account_help"},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.ProjectedNodes) != 2 {
		t.Fatalf("projected nodes = %#v, want 2", view.ProjectedNodes)
	}
	first := view.ProjectedNodes[0]
	if first.ID != "journey_node:reset_password:ask_email:edge_root_email" {
		t.Fatalf("first projected node = %#v, want edge-aware projected id", first)
	}
	if first.SourceEdgeID != "edge_root_email" {
		t.Fatalf("first projected node = %#v, want source edge id", first)
	}
	meta, _ := first.Metadata["journey_node"].(map[string]any)
	if meta["kind"] != "chat" {
		t.Fatalf("metadata = %#v, want merged edge metadata", first.Metadata)
	}
	actionMeta, _ := first.Metadata["customer_dependent_action_data"].(map[string]any)
	if actionMeta["is_customer_dependent"] != true {
		t.Fatalf("metadata = %#v, want customer-dependent projection metadata", first.Metadata)
	}
	second := view.ProjectedNodes[1]
	if second.ID != "journey_node:reset_password:verify_code:edge_email_code" {
		t.Fatalf("second projected node = %#v, want edge-aware projected id", second)
	}
	meta2, _ := second.Metadata["journey_node"].(map[string]any)
	if meta2["edge_label"] != "after_email" {
		t.Fatalf("metadata = %#v, want downstream merged edge metadata", second.Metadata)
	}
	if meta2["kind"] != "chat" {
		t.Fatalf("metadata = %#v, want projected node kind metadata", second.Metadata)
	}
}

func TestResolvePreservesIncomingRootEdgeMetadataForProjectedRootState(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "I want to order pizza"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "pizza_order",
				When:   []string{"order pizza"},
				RootID: "ask_quantity",
				States: []policy.JourneyNode{
					{ID: "ask_quantity", Type: "message", Instruction: "How many pizzas do you want?"},
					{ID: "confirm_order", Type: "message", Instruction: "Confirm the order."},
				},
				Edges: []policy.JourneyEdge{
					{ID: "edge_root_quantity", Source: "__journey_root__", Target: "ask_quantity", Condition: "pizza order requested", Metadata: map[string]any{"journey_node": map[string]any{"edge_label": "entry"}}},
					{ID: "edge_quantity_confirm", Source: "ask_quantity", Target: "confirm_order"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.ProjectedNodes) == 0 {
		t.Fatalf("projected nodes = %#v, want at least 1", view.ProjectedNodes)
	}
	first := view.ProjectedNodes[0]
	if first.ID != "journey_node:pizza_order:ask_quantity:edge_root_quantity" {
		t.Fatalf("first projected node = %#v, want root-edge-aware projected id", first)
	}
	if first.SourceEdgeID != "edge_root_quantity" {
		t.Fatalf("first projected node = %#v, want root source edge id", first)
	}
	meta, _ := first.Metadata["journey_node"].(map[string]any)
	if meta["edge_label"] != "entry" {
		t.Fatalf("metadata = %#v, want incoming root edge metadata", first.Metadata)
	}
	if meta["edge_condition"] != "pizza order requested" {
		t.Fatalf("metadata = %#v, want incoming root edge condition", first.Metadata)
	}
}

func TestResolveSynthesizesIDsForExplicitEdgesWithoutID(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "help me reset my password"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "reset_password",
				When:   []string{"reset password"},
				RootID: "root",
				States: []policy.JourneyNode{
					{ID: "ask_email", Type: "message", Instruction: "What email is on the account?"},
					{ID: "verify_code", Type: "message", Instruction: "What code did you receive?"},
				},
				Edges: []policy.JourneyEdge{
					{Source: "root", Target: "ask_email", Metadata: map[string]any{"journey_node": map[string]any{"kind": "chat"}}},
					{Source: "ask_email", Target: "verify_code", Condition: "customer provided email", Metadata: map[string]any{"journey_node": map[string]any{"edge_label": "after_email"}}},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(view.ProjectedNodes) != 2 {
		t.Fatalf("projected nodes = %#v, want 2", view.ProjectedNodes)
	}
	first := view.ProjectedNodes[0]
	if first.SourceEdgeID == "" {
		t.Fatalf("first projected node = %#v, want synthesized source edge id", first)
	}
	if !strings.HasPrefix(first.ID, "journey_node:reset_password:ask_email:reset_password:root->ask_email#") {
		t.Fatalf("first projected node = %#v, want synthesized edge-aware projected id", first)
	}
	meta, _ := first.Metadata["journey_node"].(map[string]any)
	if meta["kind"] != "chat" {
		t.Fatalf("metadata = %#v, want synthesized edge metadata preserved", first.Metadata)
	}
	second := view.ProjectedNodes[1]
	meta2, _ := second.Metadata["journey_node"].(map[string]any)
	if meta2["edge_label"] != "after_email" {
		t.Fatalf("metadata = %#v, want downstream synthesized edge metadata preserved", second.Metadata)
	}
	if meta2["edge_condition"] != "customer provided email" {
		t.Fatalf("metadata = %#v, want synthesized edge condition preserved", second.Metadata)
	}
}

func TestResolveRecordsRelationalResolutionDetails(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "I need a refund for my damaged order"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "refund_help", When: "refund", Then: "Help with refund", Tags: []string{"returns"}, Priority: 2},
				{ID: "generic_help", When: "damaged order", Then: "Generic help", Priority: 1},
				{ID: "returns_closure", When: "the case is approved for closure", Then: "Close the refund loop", Tags: []string{"returns"}},
			},
			Relationships: []policy.Relationship{
				{Kind: "priority", Source: "tag:returns", Target: "generic_help"},
				{Kind: "entails", Source: "refund_help", Target: "returns_closure"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "refund_help") || !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "returns_closure") {
		t.Fatalf("matched guidelines = %#v, want refund_help and entailed returns_closure", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "generic_help") {
		t.Fatalf("suppressed guidelines = %#v, want generic_help", view.SuppressedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "generic_help", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want deprioritized generic_help", view.ResolutionRecords)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "returns_closure", ResolutionEntailed) {
		t.Fatalf("resolution records = %#v, want entailed returns_closure", view.ResolutionRecords)
	}
}

func TestResolvePrioritizesGuidelineOverJourney(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer asks about drinks"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "standalone", When: "customer asks about drinks", Then: "Recommend Pepsi", Priority: 20},
			},
			Journeys: []policy.Journey{{
				ID:   "Drink Recommendation Journey",
				When: []string{"customer asks about drinks"},
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want", Next: []string{"recommend_cola"}},
					{ID: "recommend_cola", Type: "message", Instruction: "Recommend Coca-Cola"},
				},
				Priority: 5,
			}},
			Relationships: []policy.Relationship{
				{Source: "standalone", Kind: "priority", Target: "journey:Drink Recommendation Journey"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, want standalone guideline", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "journey_node:Drink Recommendation Journey:ask_drink") {
		t.Fatalf("matched guidelines = %#v, do not want active journey node to survive higher-priority guideline", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "journey_node:Drink Recommendation Journey:ask_drink") {
		t.Fatalf("suppressed guidelines = %#v, want journey node deprioritized", view.SuppressedGuidelines)
	}
	if view.ActiveJourney != nil || view.ActiveJourneyState != nil {
		t.Fatalf("active journey = %#v / %#v, want journey cleared after it loses priority", view.ActiveJourney, view.ActiveJourneyState)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey:Drink Recommendation Journey", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want deprioritized journey entity", view.ResolutionRecords)
	}
}

func TestResolvePrioritizesJourneyOverGuideline(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer asks about drinks"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "standalone", When: "customer asks about drinks", Then: "Recommend Coca-Cola", Priority: 5},
			},
			Journeys: []policy.Journey{{
				ID:   "Drink Recommendation Journey",
				When: []string{"customer asks about drinks"},
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want", Next: []string{"recommend_pepsi"}},
					{ID: "recommend_pepsi", Type: "message", Instruction: "Recommend Pepsi"},
				},
				Priority: 20,
			}},
			Relationships: []policy.Relationship{
				{Source: "journey:Drink Recommendation Journey", Kind: "priority", Target: "standalone"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, do not want standalone guideline to survive higher-priority journey", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "standalone") {
		t.Fatalf("suppressed guidelines = %#v, want standalone deprioritized", view.SuppressedGuidelines)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "journey_node:Drink Recommendation Journey:ask_drink") {
		t.Fatalf("matched guidelines = %#v, want active journey node to remain", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if view.ActiveJourney == nil || view.ActiveJourneyState == nil || view.ActiveJourney.ID != "Drink Recommendation Journey" {
		t.Fatalf("active journey = %#v / %#v, want journey to remain active", view.ActiveJourney, view.ActiveJourneyState)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey:Drink Recommendation Journey", ResolutionNone) {
		t.Fatalf("resolution records = %#v, want active journey entity to remain", view.ResolutionRecords)
	}
}

func TestResolveAppliesNumericalPriorityOverJourneyEntity(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer asks about drinks"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "standalone", When: "customer asks about drinks", Then: "Recommend sparkling water", Priority: 20},
			},
			Journeys: []policy.Journey{{
				ID:       "Drink Recommendation Journey",
				When:     []string{"customer asks about drinks"},
				Priority: 5,
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, want standalone guideline", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if view.ActiveJourney != nil || view.ActiveJourneyState != nil {
		t.Fatalf("active journey = %#v / %#v, want journey cleared by higher numerical priority guideline", view.ActiveJourney, view.ActiveJourneyState)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey:Drink Recommendation Journey", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want journey entity deprioritized", view.ResolutionRecords)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey_node:Drink Recommendation Journey:ask_drink", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want active journey node deprioritized", view.ResolutionRecords)
	}
}

func TestResolveAppliesNumericalPriorityJourneyOverGuideline(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer asks about drinks"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "standalone", When: "customer asks about drinks", Then: "Recommend water", Priority: 5},
			},
			Journeys: []policy.Journey{{
				ID:       "Drink Recommendation Journey",
				When:     []string{"customer asks about drinks"},
				Priority: 20,
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, do not want standalone guideline to survive higher numerical priority journey", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if view.ActiveJourney == nil || view.ActiveJourney.ID != "Drink Recommendation Journey" {
		t.Fatalf("active journey = %#v, want journey to remain active", view.ActiveJourney)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "standalone", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want standalone guideline deprioritized", view.ResolutionRecords)
	}
}

func TestResolveFiltersJourneyDependentGuidelineWhenJourneyIsDeprioritized(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer asks about drinks"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "depends_on_journey", When: "customer asks about drinks", Then: "Recommend Sprite", Priority: 10},
				{ID: "winner", When: "customer asks about drinks", Then: "Recommend Pepsi", Priority: 20},
			},
			Journeys: []policy.Journey{{
				ID:   "Drink Recommendation Journey",
				When: []string{"customer asks about drinks"},
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want"},
				},
				Priority: 5,
			}},
			Relationships: []policy.Relationship{
				{Source: "depends_on_journey", Kind: "dependency", Target: "journey:Drink Recommendation Journey"},
				{Source: "winner", Kind: "priority", Target: "journey:Drink Recommendation Journey"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "depends_on_journey") {
		t.Fatalf("matched guidelines = %#v, do not want journey-dependent guideline when journey is deprioritized", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "winner") {
		t.Fatalf("matched guidelines = %#v, want winner guideline", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "depends_on_journey", ResolutionUnmetDependency) {
		t.Fatalf("resolution records = %#v, want journey-dependent guideline filtered by unmet dependency after journey deprioritization", view.ResolutionRecords)
	}
}

func TestResolveDoesNotSuppressGuidelineWhenPriorityWinnerIsInactive(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "telescope"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "winner", When: "volcano", Then: "winner action"},
				{ID: "loser", When: "telescope", Then: "loser action"},
			},
			Relationships: []policy.Relationship{
				{Source: "winner", Kind: "priority", Target: "loser"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "loser") {
		t.Fatalf("matched guidelines = %#v, want loser to remain active when winner is inactive", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsSuppressedGuideline(view.SuppressedGuidelines, "loser") {
		t.Fatalf("suppressed guidelines = %#v, do not want loser suppressed when winner is inactive", view.SuppressedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "loser", ResolutionNone) {
		t.Fatalf("resolution records = %#v, want loser to remain active", view.ResolutionRecords)
	}
}

func TestResolveDoesNotSuppressJourneyNodeWhenPriorityJourneyIsInactive(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "nebula itinerary"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{
				{
					ID:   "Journey A",
					When: []string{"sunflower itinerary"},
					States: []policy.JourneyNode{
						{ID: "ask_a", Type: "message", Instruction: "Ask A"},
					},
				},
				{
					ID:   "Journey B",
					When: []string{"nebula itinerary"},
					States: []policy.JourneyNode{
						{ID: "ask_b", Type: "message", Instruction: "Ask B"},
					},
				},
			},
			Relationships: []policy.Relationship{
				{Source: "journey:Journey A", Kind: "priority", Target: "journey:Journey B"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "journey_node:Journey B:ask_b") {
		t.Fatalf("matched guidelines = %#v, want Journey B node to remain active when Journey A is inactive", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsSuppressedGuideline(view.SuppressedGuidelines, "journey_node:Journey B:ask_b") {
		t.Fatalf("suppressed guidelines = %#v, do not want Journey B node suppressed when Journey A is inactive", view.SuppressedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey_node:Journey B:ask_b", ResolutionNone) {
		t.Fatalf("resolution records = %#v, want Journey B node to remain active", view.ResolutionRecords)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey_node:Journey A:ask_a", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want inactive Journey A root node recorded as deprioritized", view.ResolutionRecords)
	}
}

func TestResolveConditionGuidelineSurvivesWhenJourneyNodeIsDeprioritized(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "customer is interested"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "journey_condition", When: "customer is interested", Then: "observe interest", Tags: []string{"journey:Journey 1"}},
				{ID: "winner", When: "customer is interested", Then: "recommend alternative", Priority: 20},
			},
			Journeys: []policy.Journey{{
				ID:   "Journey 1",
				When: []string{"customer is interested"},
				States: []policy.JourneyNode{
					{ID: "recommend_product", Type: "message", Instruction: "recommend product"},
				},
			}},
			Relationships: []policy.Relationship{
				{Source: "winner", Kind: "priority", Target: "journey:Journey 1"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "winner") || !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "journey_condition") {
		t.Fatalf("matched guidelines = %#v, want winner and journey_condition", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "journey_node:Journey 1:recommend_product") {
		t.Fatalf("matched guidelines = %#v, do not want journey node to survive priority loss", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey_condition", ResolutionNone) {
		t.Fatalf("resolution records = %#v, want journey_condition to survive", view.ResolutionRecords)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey_node:Journey 1:recommend_product", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want journey node deprioritized", view.ResolutionRecords)
	}
}

func TestResolveBuildsGroupedToolPlan(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "I lost my card and need help"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "card_help",
				When: "lost my card",
				Then: "Help with card actions",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "card_help", ToolID: "cards.lock_card"},
				{GuidelineID: "card_help", ToolID: "cards.report_lost"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "cards_lock", ProviderID: "cards", Name: "lock_card", MetadataJSON: `{"overlap_group":"cards:lost-card","consequential":true}`},
			{ID: "cards_report", ProviderID: "cards", Name: "report_lost", MetadataJSON: `{"overlap_group":"cards:lost-card","consequential":true}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if len(toolPlan.Candidates) != 2 {
		t.Fatalf("tool candidates = %#v, want 2", toolPlan.Candidates)
	}
	if len(toolPlan.OverlappingGroups) != 1 {
		t.Fatalf("overlapping groups = %#v, want 1", toolPlan.OverlappingGroups)
	}
	if len(toolPlan.Batches) != 1 || toolPlan.Batches[0].Kind != "overlapping_tools" || toolPlan.Batches[0].SelectedTool == "" {
		t.Fatalf("tool batches = %#v, want one overlapping-tools batch with a selected winner", toolPlan.Batches)
	}
	if toolPlan.SelectedTool == "" || toolDecision.SelectedTool == "" {
		t.Fatalf("tool plan/decision = %#v / %#v, want a selected tool", toolPlan, toolDecision)
	}
	states := map[string]string{}
	rejectedBy := map[string]string{}
	for _, item := range toolPlan.Candidates {
		states[item.ToolID] = item.DecisionState
		rejectedBy[item.ToolID] = item.RejectedBy
	}
	if states[toolPlan.SelectedTool] != "selected" {
		t.Fatalf("tool candidate states = %#v, want selected state for chosen tool", states)
	}
	for toolID, state := range states {
		if toolID == toolPlan.SelectedTool {
			continue
		}
		if state != "rejected_overlap" {
			t.Fatalf("tool candidate states = %#v, want rejected_overlap for overlapping alternative", states)
		}
		if rejectedBy[toolID] != toolPlan.SelectedTool {
			t.Fatalf("tool rejection provenance = %#v, want %s rejected by %s", rejectedBy, toolID, toolPlan.SelectedTool)
		}
	}
}

func TestResolveBuildsTransitiveOverlapGroupOnlyAcrossMatchedTools(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "please perform alpha action, bravo action, and charlie action"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "do_a", When: "perform alpha action", Then: "run aa"},
				{ID: "do_b", When: "perform bravo action", Then: "run bb"},
				{ID: "do_c", When: "perform charlie action", Then: "run cc"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "do_a", ToolID: "local:aa"},
				{GuidelineID: "do_b", ToolID: "local:bb"},
				{GuidelineID: "do_c", ToolID: "local:cc"},
			},
			Relationships: []policy.Relationship{
				{Kind: "overlap", Source: "local:aa", Target: "local:bb"},
				{Kind: "overlap", Source: "local:bb", Target: "local:cc"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:aa", ProviderID: "local", Name: "aa", Description: "run aa", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
			{ID: "local:bb", ProviderID: "local", Name: "bb", Description: "run bb", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
			{ID: "local:cc", ProviderID: "local", Name: "cc", Description: "run cc", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	if len(toolPlan.OverlappingGroups) != 1 {
		t.Fatalf("overlapping groups = %#v, want 1 transitive group", toolPlan.OverlappingGroups)
	}
	if !sameStringSlice(toolPlan.OverlappingGroups[0], []string{"local.aa", "local.bb", "local.cc"}) {
		t.Fatalf("overlap group = %#v, want [local.aa local.bb local.cc]", toolPlan.OverlappingGroups[0])
	}
}

func TestResolveDoesNotCreateIndirectOverlapAcrossUnmatchedMiddleTool(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "please perform alpha action and charlie action"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "do_a", When: "perform alpha action", Then: "run aa"},
				{ID: "do_c", When: "perform charlie action", Then: "run cc"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "do_a", ToolID: "local:aa"},
				{GuidelineID: "do_c", ToolID: "local:cc"},
			},
			Relationships: []policy.Relationship{
				{Kind: "overlap", Source: "local:aa", Target: "local:bb"},
				{Kind: "overlap", Source: "local:bb", Target: "local:cc"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:aa", ProviderID: "local", Name: "aa", Description: "run aa", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
			{ID: "local:cc", ProviderID: "local", Name: "cc", Description: "run cc", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	if len(toolPlan.OverlappingGroups) != 0 {
		t.Fatalf("overlapping groups = %#v, want none without matched middle tool", toolPlan.OverlappingGroups)
	}
}

func TestResolveSkipsAlreadyStagedToolCall(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "please lock my card"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I'm checking lock_card now."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "please lock my card"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lock_requested",
				When: "lock my card",
				Then: "lock the card",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lock_requested", ToolID: "local:lock_card"},
			},
			ToolPolicies: []policy.ToolPolicy{{
				ID:       "allow_lock_card",
				ToolIDs:  []string{"local:lock_card"},
				Exposure: "allow",
			}},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:              "local:lock_card",
			ProviderID:      "local",
			Name:            "lock_card",
			Description:     "lock a payment card",
			RuntimeProtocol: "mcp",
			Schema:          `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if toolPlan.SelectedTool != "local.lock_card" {
		t.Fatalf("tool plan = %#v, want selected local.lock_card", toolPlan)
	}
	if len(toolPlan.Candidates) != 1 || toolPlan.Candidates[0].DecisionState != "already_staged" {
		t.Fatalf("tool candidates = %#v, want already_staged candidate state", toolPlan.Candidates)
	}
	if len(toolPlan.Batches) != 1 || toolPlan.Batches[0].Kind != "single_tool" || toolPlan.Batches[0].SelectedTool != "" {
		t.Fatalf("tool batches = %#v, want one single-tool batch with no runnable selection", toolPlan.Batches)
	}
	if toolDecision.SelectedTool != "" || toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want staged tool to be suppressed from execution", toolDecision)
	}
}

func TestResolveAutoApprovesNonConsequentialNoArgTool(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "please ping for me"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "ping_requested",
				When: "ping",
				Then: "ping the service",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "ping_requested", ToolID: "local:ping"},
			},
			ToolPolicies: []policy.ToolPolicy{{
				ID:       "allow_ping",
				ToolIDs:  []string{"local:ping"},
				Exposure: "allow",
			}},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:              "local:ping",
			ProviderID:      "local",
			Name:            "ping",
			Description:     "perform a ping",
			RuntimeProtocol: "mcp",
			Schema:          `{"type":"object","properties":{}}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if toolPlan.SelectedTool != "local.ping" {
		t.Fatalf("tool plan = %#v, want local.ping selected", toolPlan)
	}
	if len(toolPlan.Candidates) != 1 || !toolPlan.Candidates[0].AutoApproved || toolPlan.Candidates[0].DecisionState != "selected" {
		t.Fatalf("tool candidates = %#v, want one auto-approved selected candidate", toolPlan.Candidates)
	}
	if len(toolPlan.Batches) != 1 || toolPlan.Batches[0].Kind != "single_tool" || !toolPlan.Batches[0].Simplified || toolPlan.Batches[0].SelectedTool != "local.ping" {
		t.Fatalf("tool batches = %#v, want one simplified single-tool batch selecting local.ping", toolPlan.Batches)
	}
	if toolDecision.SelectedTool != "local.ping" || !toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want runnable local.ping selection", toolDecision)
	}
}

func TestResolveRejectsUngroundedToolWhenGroundedAlternativeExists(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "please lock my card"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "lock_requested", When: "lock my card", Then: "lock the card"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lock_requested", ToolID: "local:lock_card"},
				{GuidelineID: "lock_requested", ToolID: "local:generic_lookup"},
			},
			ToolPolicies: []policy.ToolPolicy{
				{ID: "allow_lock_card", ToolIDs: []string{"local:lock_card"}, Exposure: "allow"},
				{ID: "allow_generic_lookup", ToolIDs: []string{"local:generic_lookup"}, Exposure: "allow"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:lock_card", ProviderID: "local", Name: "lock_card", Description: "lock a payment card", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
			{ID: "local:generic_lookup", ProviderID: "local", Name: "generic_lookup", Description: "look up unrelated generic account data", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	states := map[string]string{}
	rejectedBy := map[string]string{}
	for _, item := range toolPlan.Candidates {
		states[item.ToolID] = item.DecisionState
		rejectedBy[item.ToolID] = item.RejectedBy
	}
	if states["local.lock_card"] != "selected" {
		t.Fatalf("tool candidate states = %#v, want local.lock_card selected", states)
	}
	if states["local.generic_lookup"] != "rejected_ungrounded" {
		t.Fatalf("tool candidate states = %#v, want local.generic_lookup rejected_ungrounded", states)
	}
	if rejectedBy["local.generic_lookup"] != "local.lock_card" {
		t.Fatalf("tool rejection provenance = %#v, want local.generic_lookup rejected by local.lock_card", rejectedBy)
	}
}

func TestResolveSelectsSpecializedReferenceTool(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "What's your price for a Harley-Davidson Street Glide motorcycle?"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "vehicle_price", When: "price of a vehicle", Then: "check the vehicle price"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "vehicle_price", ToolID: "local:check_vehicle_price"},
				{GuidelineID: "vehicle_price", ToolID: "local:check_motorcycle_price"},
			},
			ToolPolicies: []policy.ToolPolicy{
				{ID: "allow_vehicle", ToolIDs: []string{"local:check_vehicle_price", "local:check_motorcycle_price"}, Exposure: "allow"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:check_vehicle_price", ProviderID: "local", Name: "check_vehicle_price", Description: "check generic vehicle price", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"model":{"type":"string"}},"required":["model"]}`, MetadataJSON: `{"overlap_group":"vehicle_price","consequential":true}`},
			{ID: "local:check_motorcycle_price", ProviderID: "local", Name: "check_motorcycle_price", Description: "check motorcycle price", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"model":{"type":"string"}},"required":["model"]}`, MetadataJSON: `{"overlap_group":"vehicle_price","consequential":true}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	if toolPlan.SelectedTool != "local.check_motorcycle_price" {
		t.Fatalf("tool plan = %#v, want specialized local.check_motorcycle_price selected", toolPlan)
	}
	reasons := map[string]string{}
	states := map[string]string{}
	for _, item := range toolPlan.Candidates {
		reasons[item.ToolID] = item.Rationale
		states[item.ToolID] = item.DecisionState
	}
	if states["local.check_vehicle_price"] != "rejected_overlap" {
		t.Fatalf("tool candidate states = %#v, want local.check_vehicle_price rejected_overlap", states)
	}
	if !strings.Contains(strings.ToLower(reasons["local.check_motorcycle_price"]), "specialized") {
		t.Fatalf("tool candidate reasons = %#v, want specialized rationale for motorcycle tool", reasons)
	}
}

func TestResolveMarksConfirmationToolToRunInTandemWithScheduleTool(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "Please schedule an appointment with Dr. Gabi tomorrow at 6pm and send me a confirmation email."}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "schedule_visit", When: "customer wants to schedule an appointment", Then: "schedule the appointment"},
				{ID: "send_confirmation", When: "customer wants confirmation after scheduling", Then: "send a confirmation email"},
			},
			Relationships: []policy.Relationship{
				{Source: "local:send_confirmation_email", Kind: "reference", Target: "local:schedule_appointment"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "schedule_visit", ToolID: "local:schedule_appointment"},
				{GuidelineID: "send_confirmation", ToolID: "local:send_confirmation_email"},
			},
			ToolPolicies: []policy.ToolPolicy{
				{ID: "allow_schedule", ToolIDs: []string{"local:schedule_appointment", "local:send_confirmation_email"}, Exposure: "allow"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:schedule_appointment", ProviderID: "local", Name: "schedule_appointment", Description: "schedule an appointment with a doctor", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"date":{"type":"string"}}}`},
			{ID: "local:send_confirmation_email", ProviderID: "local", Name: "send_confirmation_email", Description: "send a confirmation email after an appointment is scheduled", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"session_id":{"type":"string"}}}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	var confirmation ToolCandidate
	var found bool
	for _, item := range toolPlan.Candidates {
		if item.ToolID == "local.send_confirmation_email" {
			confirmation = item
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tool candidates = %#v, want send_confirmation_email candidate present", toolPlan.Candidates)
	}
	if !slices.Contains(toolPlan.SelectedTools, "local.schedule_appointment") || !slices.Contains(toolPlan.SelectedTools, "local.send_confirmation_email") {
		t.Fatalf("selected tools = %#v, want both local.schedule_appointment and local.send_confirmation_email", toolPlan.SelectedTools)
	}
	if !slices.Contains(confirmation.RunInTandemWith, "local.schedule_appointment") {
		t.Fatalf("confirmation candidate tandem targets = %#v, want local.schedule_appointment", confirmation.RunInTandemWith)
	}
	if !strings.Contains(strings.ToLower(confirmation.Rationale), "tandem") {
		t.Fatalf("confirmation rationale = %q, want tandem signal", confirmation.Rationale)
	}
}

func TestResolvePlansTwoCallsForSameSearchToolWhenOptionalArgsDiffer(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "Hey, do you have Dell laptop or Samsung SSD?"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "filter_electronic_products", When: "laptop", Then: "search electronic products"},
			},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "filter_electronic_products", ToolID: "local:search_electronic_products"},
			},
		}},
		nil,
		[]tool.CatalogEntry{
			{ID: "local:search_electronic_products", ProviderID: "local", Name: "search_electronic_products", Description: "search electronic products by vendor and keyword", RuntimeProtocol: "mcp", Schema: `{"type":"object","properties":{"vendor":{"type":"string"},"keyword":{"type":"string"}}}`},
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	if len(toolPlan.Calls) != 2 {
		t.Fatalf("planned calls = %#v, want 2", toolPlan.Calls)
	}
	firstArgs := toolPlan.Calls[0].Arguments
	secondArgs := toolPlan.Calls[1].Arguments
	if firstArgs["vendor"] == secondArgs["vendor"] || firstArgs["keyword"] == secondArgs["keyword"] {
		t.Fatalf("planned call args = %#v, want distinct vendor/keyword combinations", toolPlan.Calls)
	}
}

func TestResolveSkipsAlreadySatisfiedToolCall(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "what is my return status?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Your return status is currently in transit."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "thanks, can you repeat the return status?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup_status",
				When: "return status",
				Then: "check the return status",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "lookup_status", ToolID: "local:get_return_status"},
			},
			ToolPolicies: []policy.ToolPolicy{{
				ID:       "allow_get_return_status",
				ToolIDs:  []string{"local:get_return_status"},
				Exposure: "allow",
			}},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:              "local:get_return_status",
			ProviderID:      "local",
			Name:            "get_return_status",
			Description:     "look up the current return status",
			RuntimeProtocol: "mcp",
			Schema:          `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`,
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if len(toolPlan.Candidates) != 1 || toolPlan.Candidates[0].DecisionState != "already_satisfied" {
		t.Fatalf("tool candidates = %#v, want already_satisfied candidate state", toolPlan.Candidates)
	}
	if len(toolPlan.Batches) != 1 || toolPlan.Batches[0].Kind != "single_tool" || toolPlan.Batches[0].SelectedTool != "" {
		t.Fatalf("tool batches = %#v, want one single-tool batch with no runnable selection", toolPlan.Batches)
	}
	if toolDecision.SelectedTool != "" || toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want already-satisfied tool to be suppressed from execution", toolDecision)
	}
}

func TestResolveMarksGuidelineSatisfiedByToolHistory(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "where is my return?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I checked the return status. It is currently in transit."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "okay, and anything else about my return status?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "lookup_status",
				When: "return",
				Then: "check the return status",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || !item.SatisfiedByToolEvent || item.RequiresResponse {
		t.Fatalf("analyzed guideline = %#v, want satisfied-by-tool classification", item)
	}
	if item.SatisfactionSource != "tool_event" {
		t.Fatalf("analyzed guideline = %#v, want tool_event satisfaction source", item)
	}
}

func TestResolveMarksGuidelineSatisfiedByAssistantMessageSource(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "can you greet me politely?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Please greet the customer politely and warmly."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "thanks, anything else?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "greet_politely",
				When:  "greet me politely",
				Then:  "Please greet the customer politely and warmly.",
				Track: true,
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || item.RequiresResponse {
		t.Fatalf("analyzed guideline = %#v, want already satisfied by assistant message", item)
	}
	if item.SatisfactionSource != "assistant_message" {
		t.Fatalf("analyzed guideline = %#v, want assistant_message satisfaction source", item)
	}
}

func TestResolveDiscountAlreadyAppliedStaysAssistantMessageSource(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "My order is late again."}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I'm sorry about that. I'll give you a discount on this order."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Thanks, but where is it now?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "late_so_discount",
				When:  "customer complains that they didn't get the order on time",
				Then:  "offer a discount",
				Track: true,
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied {
		t.Fatalf("analyzed guideline = %#v, want already satisfied", item)
	}
	if item.SatisfactionSource != "assistant_message" {
		t.Fatalf("analyzed guideline = %#v, want assistant_message satisfaction source", item)
	}
	if item.SatisfiedByToolEvent {
		t.Fatalf("analyzed guideline = %#v, do not want tool-event satisfaction", item)
	}
}

func TestResolveMarksCustomerDependentGuidelineSatisfiedByCustomerAnswer(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I'd like to book a table for tomorrow night."}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Sure! Would you prefer to sit inside or outside?"}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Outside, please."}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "reservation_location",
				When:  "customer wants to make a reservation",
				Then:  "check if they prefer inside or outside",
				Track: true,
				Scope: "customer",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || item.RequiresResponse {
		t.Fatalf("analyzed guideline = %#v, want already satisfied by customer answer", item)
	}
	if item.SatisfactionSource != "customer_answer" {
		t.Fatalf("analyzed guideline = %#v, want customer_answer satisfaction source", item)
	}
}

func TestResolveAppliesPriorityRelationship(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there and I need followup help"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "greet_howdy", When: "customer says hello", Then: "say Howdy", Priority: 30},
				{ID: "greet_hi", When: "customer says hello", Then: "say Hi", Priority: 20},
			},
			Relationships: []policy.Relationship{
				{Source: "greet_howdy", Kind: "priority", Target: "greet_hi"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	got := make([]string, 0, len(view.MatchFinalizeStage.MatchedGuidelines))
	for _, item := range view.MatchFinalizeStage.MatchedGuidelines {
		got = append(got, item.ID)
	}
	if len(got) != 1 || got[0] != "greet_howdy" {
		t.Fatalf("matched guidelines = %#v, want only greet_howdy", got)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "greet_howdy", ResolutionNone) ||
		!containsResolutionRecord(view.ResolutionRecords, "greet_hi", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want none/deprioritized records", view.ResolutionRecords)
	}
}

func TestResolveSupportsTagAnyDependency(t *testing.T) {
	view, err := Resolve(
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
			Guidelines: []policy.Guideline{
				{ID: "followup", When: "hello", Then: "offer followup help", Priority: 10},
				{ID: "member_one", When: "hello", Then: "say hello", Tags: []string{"greetings"}},
				{ID: "member_two", When: "bye", Then: "say goodbye", Tags: []string{"greetings"}},
			},
			Relationships: []policy.Relationship{
				{Source: "followup", Kind: "dependency", Target: "tag_any:greetings"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive tag_any dependency", view.MatchFinalizeStage.MatchedGuidelines)
	}
}

func TestResolveDoesNotApplyTagPriorityWithoutActiveSourceMember(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "beta"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "t1_member", When: "alpha", Then: "t1 action", Tags: []string{"t1"}},
				{ID: "t2_member", When: "beta", Then: "t2 action", Tags: []string{"t2"}},
			},
			Relationships: []policy.Relationship{
				{Source: "tag_all:t1", Kind: "priority", Target: "tag_all:t2"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "t2_member") {
		t.Fatalf("matched guidelines = %#v, want t2_member to survive without active t1 source", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsSuppressedGuideline(view.SuppressedGuidelines, "t2_member") {
		t.Fatalf("suppressed guidelines = %#v, do not want t2_member deprioritized", view.SuppressedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "t2_member", ResolutionNone) {
		t.Fatalf("resolution records = %#v, want t2_member none record", view.ResolutionRecords)
	}
}

func TestResolveTagPriorityTransitivelyFiltersDependentGuideline(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "alpha beta gamma"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "t1_member", When: "alpha", Then: "t1 action", Tags: []string{"t1"}},
				{ID: "t2_member", When: "beta", Then: "t2 action", Tags: []string{"t2"}},
				{ID: "dependent", When: "gamma", Then: "dependent action"},
			},
			Relationships: []policy.Relationship{
				{Source: "tag_all:t1", Kind: "priority", Target: "tag_all:t2"},
				{Source: "dependent", Kind: "dependency", Target: "tag_all:t2"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	var got []string
	for _, item := range view.MatchFinalizeStage.MatchedGuidelines {
		got = append(got, item.ID)
	}
	if len(got) != 1 || got[0] != "t1_member" {
		t.Fatalf("matched guidelines = %#v, want only t1_member", got)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "t2_member", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want t2_member deprioritized", view.ResolutionRecords)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "dependent", ResolutionUnmetDependency) {
		t.Fatalf("resolution records = %#v, want dependent unmet_dependency", view.ResolutionRecords)
	}
}

func TestResolveFiltersTagAllDependencyWhenNotAllMatched(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there and I need followup help"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "followup", When: "hello", Then: "offer followup help", Priority: 10},
				{ID: "member_one", When: "hello", Then: "say hello", Tags: []string{"greetings"}},
				{ID: "member_two", When: "bye", Then: "say goodbye", Tags: []string{"greetings"}},
			},
			Relationships: []policy.Relationship{
				{Source: "followup", Kind: "dependency", Target: "tag_all:greetings"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup filtered by unmet tag_all dependency", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "followup", ResolutionUnmetDependency) {
		t.Fatalf("resolution records = %#v, want unmet dependency for followup", view.ResolutionRecords)
	}
}

func TestResolveSupportsDependencyAnyGroup(t *testing.T) {
	view, err := Resolve(
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
			Guidelines: []policy.Guideline{
				{ID: "followup", When: "hello", Then: "offer followup help", Priority: 10},
				{ID: "member_one", When: "hello", Then: "say hello"},
				{ID: "member_two", When: "bye", Then: "say goodbye"},
			},
			Relationships: []policy.Relationship{
				{Source: "followup", Kind: "dependency_any", Target: "member_one"},
				{Source: "followup", Kind: "dependency_any", Target: "member_two"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive dependency_any", view.MatchFinalizeStage.MatchedGuidelines)
	}
}

func TestResolveFiltersDependencyAnyGroupWhenNoTargetsActive(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "please help me"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "followup", When: "help", Then: "offer followup help", Priority: 10},
				{ID: "member_one", When: "hello", Then: "say hello"},
				{ID: "member_two", When: "bye", Then: "say goodbye"},
			},
			Relationships: []policy.Relationship{
				{Source: "followup", Kind: "dependency_any", Target: "member_one"},
				{Source: "followup", Kind: "dependency_any", Target: "member_two"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup filtered by unmet dependency_any", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "followup", ResolutionUnmetDependencyAny) {
		t.Fatalf("resolution records = %#v, want unmet dependency_any for followup", view.ResolutionRecords)
	}
}

func TestResolveSupportsMixedDependencyAndDependencyAny(t *testing.T) {
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: time.Now().UTC(),
			Content:   []session.ContentPart{{Type: "text", Text: "hello there and help with my refund"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "followup", When: "refund", Then: "offer followup help", Priority: 10},
				{ID: "required_one", When: "refund", Then: "say refund"},
				{ID: "any_one", When: "hello", Then: "say hello"},
				{ID: "any_two", When: "bye", Then: "say goodbye"},
			},
			Relationships: []policy.Relationship{
				{Source: "followup", Kind: "dependency", Target: "required_one"},
				{Source: "followup", Kind: "dependency_any", Target: "any_one"},
				{Source: "followup", Kind: "dependency_any", Target: "any_two"},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuidelineID(view.MatchFinalizeStage.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive mixed dependency + dependency_any", view.MatchFinalizeStage.MatchedGuidelines)
	}
}

func containsGuidelineID(items []policy.Guideline, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
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
	if len(view.MatchFinalizeStage.MatchedGuidelines) != 0 {
		t.Fatalf("matched guidelines = %#v, want none because customer clarification is missing", view.MatchFinalizeStage.MatchedGuidelines)
	}
	customer := view.CustomerDependencyStage.Decisions
	if len(customer) != 1 || len(customer[0].MissingCustomerData) == 0 {
		t.Fatalf("customer decisions = %#v, want blocked customer-dependent guideline", customer)
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
	for _, item := range view.MatchFinalizeStage.MatchedGuidelines {
		if item.ID == "gentle_followup" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("matched guidelines = %#v, want low-criticality guideline to remain active", view.MatchFinalizeStage.MatchedGuidelines)
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
	if len(view.MatchFinalizeStage.MatchedGuidelines) != 1 || view.MatchFinalizeStage.MatchedGuidelines[0].ID != "returns" {
		t.Fatalf("matched guidelines = %#v, want structured-selected returns", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if len(view.MatchFinalizeStage.GuidelineMatches) != 1 || view.MatchFinalizeStage.GuidelineMatches[0].Rationale != "structured yes" {
		t.Fatalf("guideline matches = %#v, want structured rationale", view.MatchFinalizeStage.GuidelineMatches)
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
	if len(view.MatchFinalizeStage.MatchedGuidelines) != 1 || view.MatchFinalizeStage.GuidelineMatches[0].Rationale != "retried yes" {
		t.Fatalf("view = %#v, want retried structured match", view)
	}
}

func TestResolveWithRouterRetainsStagedToolAgeGuidelineWhenStructuredActionableMisses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[]}"}}]}`))
	}))
	defer server.Close()

	router := model.NewRouter(config.ProviderConfig{
		DefaultStructured: "openrouter",
		OpenRouterBase:    server.URL,
		OpenRouterAPIKey:  "test-key",
	})

	now := time.Now().UTC()
	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Hi there, I want a drink that's on the sweeter side, what would you suggest?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Let me check your account first."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "It's 199877"}},
			},
			{
				ID:        "evt_4",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "tool",
				CreatedAt: now.Add(3 * time.Second),
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_user_age",
						"arguments": map[string]any{"user_id": "199877"},
						"result":    map[string]any{"data": 16},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "suggest_drink_underage", When: "drink under 21", Then: "Suggest a sweet non-alcoholic drink."},
				{ID: "suggest_drink_adult", When: "drink 21 or older", Then: "Suggest an alcoholic option if appropriate."},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	if !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "suggest_drink_underage") {
		t.Fatalf("matched guidelines = %#v, guideline matches = %#v, batches = %#v, condition artifacts = %#v, want suggest_drink_underage", view.MatchFinalizeStage.MatchedGuidelines, view.MatchFinalizeStage.GuidelineMatches, view.BatchResults, view.ConditionArtifactsStage.Artifacts)
	}
	if containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "suggest_drink_adult") {
		t.Fatalf("matched guidelines = %#v, do not want suggest_drink_adult", view.MatchFinalizeStage.MatchedGuidelines)
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
	toolDecision := view.ToolDecisionStage.Decision
	if toolDecision.SelectedTool != "commerce.get_return_status" {
		t.Fatalf("selected tool = %q, want commerce.get_return_status", toolDecision.SelectedTool)
	}
	if toolDecision.Arguments["reason"] != "status" {
		t.Fatalf("tool args = %#v, want structured tool arguments", toolDecision.Arguments)
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
	toolDecision := view.ToolDecisionStage.Decision
	toolPlan := view.ToolPlanStage.Plan
	if toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want cannot run", toolDecision)
	}
	if len(toolDecision.MissingArguments) != 1 || toolDecision.MissingArguments[0] != "return_id" {
		t.Fatalf("missing arguments = %#v, want return_id", toolDecision.MissingArguments)
	}
	if len(toolDecision.MissingIssues) != 1 || toolDecision.MissingIssues[0].Significance != "critical" {
		t.Fatalf("missing issues = %#v, want one critical issue", toolDecision.MissingIssues)
	}
	if toolDecision.SelectedTool != "" || toolPlan.SelectedTool != "" {
		t.Fatalf("selected tool = %#v / %#v, want blocked candidate not to remain selected", toolDecision.SelectedTool, toolPlan.SelectedTool)
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
	toolDecision := view.ToolDecisionStage.Decision
	if !toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want hidden fields derived automatically", toolDecision)
	}
	if len(toolDecision.MissingIssues) != 0 {
		t.Fatalf("missing issues = %#v, want none", toolDecision.MissingIssues)
	}
	if got := toolDecision.Arguments["session_id"]; got != "sess_1" {
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
	toolDecision := view.ToolDecisionStage.Decision
	if got := toolDecision.Arguments["locale"]; got != "en" {
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
	toolDecision := view.ToolDecisionStage.Decision
	if toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want hidden unresolved arg to block invocation", toolDecision)
	}
	if len(toolDecision.MissingIssues) != 1 || !toolDecision.MissingIssues[0].Hidden {
		t.Fatalf("missing issues = %#v, want one hidden missing issue", toolDecision.MissingIssues)
	}
	if toolDecision.MissingIssues[0].Significance != "internal" {
		t.Fatalf("missing issue = %#v, want internal significance", toolDecision.MissingIssues[0])
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
	toolDecision := view.ToolDecisionStage.Decision
	toolPlan := view.ToolPlanStage.Plan
	if toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want invalid channel to block invocation", toolDecision)
	}
	if len(toolDecision.InvalidIssues) != 1 {
		t.Fatalf("invalid issues = %#v, want one invalid issue", toolDecision.InvalidIssues)
	}
	issue := toolDecision.InvalidIssues[0]
	if issue.Parameter != "channel" || issue.Significance != "critical" {
		t.Fatalf("invalid issue = %#v, want critical channel issue", issue)
	}
	if len(issue.Choices) != 2 || issue.Choices[0] != "web" || issue.Choices[1] != "sms" {
		t.Fatalf("invalid issue choices = %#v, want web/sms", issue.Choices)
	}
	if toolDecision.SelectedTool != "" || toolPlan.SelectedTool != "" {
		t.Fatalf("selected tool = %#v / %#v, want blocked candidate not to remain selected", toolDecision.SelectedTool, toolPlan.SelectedTool)
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
	if len(view.MatchFinalizeStage.MatchedGuidelines) != 2 {
		t.Fatalf("matched guidelines = %#v, want cancel_return + cancel_confirm", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if view.MatchFinalizeStage.MatchedGuidelines[0].ID != "cancel_return" {
		t.Fatalf("top matched guideline = %#v, want cancel_return", view.MatchFinalizeStage.MatchedGuidelines)
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

func TestResolveDoesNotAdvanceToolJourneyStateBeforeExecution(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "yes, please continue"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return"},
				States: []policy.JourneyNode{
					{ID: "check_status", Type: "tool", Tool: "local:get_return_status", Next: []string{"summarize_status"}},
					{ID: "summarize_status", Type: "message", Instruction: "Summarize the status"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "check_status",
			Path:      []string{"check_status"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action == "advance" || journeyDecision.NextState != "" {
		t.Fatalf("journey decision = %#v, do not want tool step to advance before execution", journeyDecision)
	}
	if !slices.Contains(journeyDecision.Missing, "tool_execution") {
		t.Fatalf("journey decision = %#v, want tool_execution missing signal", journeyDecision)
	}
}

func TestResolveAdvancesToolJourneyStateAfterExecution(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "yes, please continue"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "tool",
				CreatedAt: now.Add(time.Second),
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_return_status",
						"arguments": map[string]any{"return_id": "ret_123"},
						"result":    map[string]any{"data": "RETURN STATUS: In transit"},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return"},
				States: []policy.JourneyNode{
					{ID: "check_status", Type: "tool", Tool: "local:get_return_status", Next: []string{"summarize_status"}},
					{ID: "summarize_status", Type: "message", Instruction: "Summarize the status"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "check_status",
			Path:      []string{"check_status"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "summarize_status" {
		t.Fatalf("journey decision = %#v, want tool step to advance after execution", journeyDecision)
	}
}

func TestResolveAdvancesForkJourneyStateAutomatically(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "let's continue"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "return_flow",
				When: []string{"return"},
				States: []policy.JourneyNode{
					{ID: "route", Type: "message", Kind: "fork", Next: []string{"collect_reason"}},
					{ID: "collect_reason", Type: "message", Instruction: "Ask for the return reason"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "return_flow",
			StateID:   "route",
			Path:      []string{"route"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "collect_reason" {
		t.Fatalf("journey decision = %#v, want automatic fork advance to collect_reason", journeyDecision)
	}
}

func TestResolveStartsJourneyAtFirstExecutableStateAfterActionlessRoot(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{{
			ID:        "evt_1",
			SessionID: "sess_1",
			Source:    "customer",
			Kind:      "message",
			CreatedAt: now,
			Content:   []session.ContentPart{{Type: "text", Text: "I need to return something"}},
		}},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:     "return_flow",
				When:   []string{"return"},
				RootID: "root",
				States: []policy.JourneyNode{
					{ID: "root", Type: "message", Kind: "fork"},
					{ID: "collect_reason", Type: "message", Instruction: "Ask for the return reason"},
				},
				Edges: []policy.JourneyEdge{
					{Source: "root", Target: "collect_reason"},
				},
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if view.ActiveJourneyState == nil || view.ActiveJourneyState.ID != "collect_reason" {
		t.Fatalf("active journey state = %#v, want collect_reason", view.ActiveJourneyState)
	}
	if view.JourneyInstance == nil || strings.Join(view.JourneyInstance.Path, ",") != "root,collect_reason" {
		t.Fatalf("journey instance = %#v, want path root->collect_reason", view.JourneyInstance)
	}
}

func TestSelectBestBacktrackTargetPrefersEarlierUnresolvedBranchPoint(t *testing.T) {
	flow := &policy.Journey{
		ID:     "calzone",
		RootID: "ask_quantity",
		States: []policy.JourneyNode{
			{ID: "ask_quantity", Type: "message", Instruction: "Ask how many"},
			{ID: "ask_type", Type: "message", Instruction: "Ask what type", When: []string{"classic italian", "spinach", "chicken"}},
			{ID: "ask_size", Type: "message", Instruction: "Ask what size", When: []string{"small", "medium", "large"}},
			{ID: "ask_drinks", Type: "message", Instruction: "Ask whether they want any drinks", When: []string{"drinks", "no drinks"}},
			{ID: "check_stock", Type: "tool"},
		},
		Edges: []policy.JourneyEdge{
			{Source: "ask_quantity", Target: "ask_type"},
			{Source: "ask_type", Target: "ask_size"},
			{Source: "ask_size", Target: "ask_drinks"},
			{Source: "ask_drinks", Target: "check_stock"},
		},
	}
	ctx := MatchingContext{
		LatestCustomerText: "Actually, can I change those to medium size instead of large? No drinks still.",
		ConversationText:   "Actually, can I change those to medium size instead of large? No drinks still.",
	}
	selection := selectBestBacktrackEvaluation(ctx, flow, []string{"ask_quantity", "ask_type", "ask_size", "ask_drinks", "check_stock"}, "check_stock", false)
	if selection.Candidate.Selection.StateID != "ask_size" {
		t.Fatalf("selection = %#v, want ask_size", selection)
	}
	if next := fastForwardJourneyState(ctx, flow, selection.Candidate.Selection.StateID, ""); next != "check_stock" {
		t.Fatalf("fast-forward next state = %q, want check_stock", next)
	}
}

func TestAdvanceJourneyBacktracksAndFastForwards(t *testing.T) {
	instance := &journey.Instance{
		ID:        "journey_1",
		SessionID: "sess_1",
		JourneyID: "order_fulfillment",
		StateID:   "check_stock",
		Path:      []string{"choose_method", "ask_address", "ask_time", "ask_drinks", "check_stock"},
		Status:    journey.StatusActive,
		UpdatedAt: time.Now().UTC(),
	}
	flow := &policy.Journey{
		ID: "order_fulfillment",
		States: []policy.JourneyNode{
			{ID: "choose_method"},
			{ID: "ask_address"},
			{ID: "ask_time"},
			{ID: "ask_drinks"},
			{ID: "check_stock", Type: "tool"},
		},
		Edges: []policy.JourneyEdge{
			{Source: "choose_method", Target: "ask_address"},
			{Source: "ask_address", Target: "ask_time"},
			{Source: "ask_time", Target: "ask_drinks"},
			{Source: "ask_drinks", Target: "check_stock"},
		},
	}
	state := &policy.JourneyNode{ID: "check_stock", Type: "tool"}
	next := AdvanceJourney(instance, state, flow, JourneyDecision{Action: "backtrack", BacktrackTo: "ask_time", NextState: "check_stock"})
	if next.StateID != "check_stock" {
		t.Fatalf("journey state = %s, want check_stock", next.StateID)
	}
	wantPath := []string{"choose_method", "ask_address", "ask_time", "ask_drinks", "check_stock"}
	if strings.Join(next.Path, ",") != strings.Join(wantPath, ",") {
		t.Fatalf("journey path = %#v, want %#v", next.Path, wantPath)
	}
}

func TestCustomerSatisfiedGuidelineDetectsDeliveryPickupAnswer(t *testing.T) {
	item := policy.Guideline{Then: "Ask whether they want delivery or pickup"}
	if !customerSatisfiedGuideline("Actually let's switch this to store pickup", item) {
		t.Fatalf("customerSatisfiedGuideline() = false, want true for pickup answer")
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
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action == "backtrack" {
		t.Fatalf("journey decision = %#v, want no illegal backtrack to an unvisited state", journeyDecision)
	}
}

func TestVerifyDraftEnforcesStrictTemplate(t *testing.T) {
	view := EngineResult{
		CompositionMode: "strict",
		NoMatch:         "Need more detail.",
		ResponseAnalysisStage: ResponseAnalysisStageResult{
			CandidateTemplates: []policy.Template{{
				ID:   "tmpl_1",
				Mode: "strict",
				Text: "Your return is approved.",
			}},
		},
	}
	got := VerifyDraft(view, "Something else", nil)
	if got.Status != "revise" || got.Replacement != "Your return is approved." {
		t.Fatalf("VerifyDraft() = %#v, want strict replacement", got)
	}
}

func TestVerifyDraftUsesResponseAnalysisTemplate(t *testing.T) {
	view := EngineResult{
		ResponseAnalysisStage: ResponseAnalysisStageResult{
			Analysis: ResponseAnalysis{
				RecommendedTemplate: "Use this approved answer.",
			},
		},
	}
	got := VerifyDraft(view, "Something else", nil)
	if got.Status != "revise" || got.Replacement != "Use this approved answer." {
		t.Fatalf("VerifyDraft() = %#v, want response-analysis replacement", got)
	}
}

func TestVerifyDraftAllowsStrictJourneyInstructionFallback(t *testing.T) {
	view := EngineResult{
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

func TestResolveWithOptionsClassifiesOutOfScopeBoundary(t *testing.T) {
	bundle := policy.Bundle{
		ID:      "pet_store",
		Version: "v1",
		DomainBoundary: policy.DomainBoundary{
			Mode:            "hard_refuse",
			AllowedTopics:   []string{"pet food", "dog toys"},
			BlockedTopics:   []string{"human food", "cooking"},
			OutOfScopeReply: "I can help with pet-store questions, but I cannot help with cooking or human food.",
		},
		Guidelines: []policy.Guideline{{
			ID:   "pet_help",
			When: "pet food",
			Then: "Help with pet food questions.",
		}},
		Retrievers: []policy.RetrieverBinding{{
			ID:    "wiki",
			Kind:  "knowledge",
			Scope: "agent",
		}},
	}
	view, err := ResolveWithOptions(context.Background(), []session.Event{{
		ID:        "evt",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "How do I season pasta for human food?"}},
	}}, []policy.Bundle{bundle}, nil, []tool.CatalogEntry{{
		ID:          "tool_pet",
		Name:        "find_pet_food",
		Description: "find pet food",
	}}, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if view.ScopeBoundaryStage.Classification != "out_of_scope" || view.ScopeBoundaryStage.Action != "refuse" {
		t.Fatalf("scope boundary stage = %#v, want out_of_scope refuse", view.ScopeBoundaryStage)
	}
	if len(view.ToolExposureStage.ExposedTools) != 0 {
		t.Fatalf("exposed tools = %#v, want none for out-of-scope turn", view.ToolExposureStage.ExposedTools)
	}
	if len(view.RetrieverStage.Results) != 0 {
		t.Fatalf("retriever results = %#v, want none for out-of-scope turn", view.RetrieverStage.Results)
	}
	if reply := view.ScopeBoundaryStage.Reply; reply == "" {
		t.Fatalf("scope boundary reply = %q, want configured response", reply)
	}
}

func TestVerifyDraftReplacesOutOfScopeAnswer(t *testing.T) {
	view := EngineResult{
		ScopeBoundaryStage: ScopeBoundaryStageResult{
			Classification: "out_of_scope",
			Action:         "refuse",
			Reply:          "I can help with pet-store questions, but I cannot help with cooking or human food.",
			Reasons:        []string{"matched_blocked_topic"},
		},
	}
	got := VerifyDraft(view, "Here is how to cook pasta.", nil)
	if got.Status != "revise" || got.Replacement != view.ScopeBoundaryStage.Reply {
		t.Fatalf("VerifyDraft() = %#v, want out-of-scope replacement", got)
	}
}

func TestVerifyDraftReplacesPrematureHighRiskCommitment(t *testing.T) {
	view := EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Please share the order number before I review refund or replacement options.",
		},
		MatchFinalizeStage: FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "verify_first",
				Then: "Verify the order before promising a refund or replacement.",
			}},
		},
	}
	got := VerifyDraft(view, "You qualify for a replacement right away.", nil)
	if got.Status != "revise" || got.Replacement != "Please share the order number before I review refund or replacement options." {
		t.Fatalf("VerifyDraft() = %#v, want verification-first replacement", got)
	}
}

func TestVerifyDraftReplacesPrematureResolutionChoice(t *testing.T) {
	view := EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Please share the order number before I review refund or replacement options.",
		},
		MatchFinalizeStage: FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "verify_first",
				Then: "Verify the order before promising a refund or replacement.",
			}},
		},
	}
	got := VerifyDraft(view, "Do you want to proceed with a replacement or a refund for your cracked toaster?", nil)
	if got.Status != "revise" || got.Replacement != "Please share the order number before I review refund or replacement options." {
		t.Fatalf("VerifyDraft() = %#v, want verification-first replacement", got)
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

func TestResolveSkipsJourneyStepsAlreadySatisfiedByCustomerHistory(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Hi, my name is John Smith and I'd like to book a flight for myself from Ben Gurion airport. We flight in the 12.10 and return in the 17.10."}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "agent",
				Kind:      "message",
				CreatedAt: now.Add(time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Hi John, thanks for reaching out! I see you're planning to fly from Ben Gurion airport. Could you please let me know your destination airport?"}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Suvarnabhumi Airport, please"}},
			},
		},
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
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "ask_class" {
		t.Fatalf("journey decision = %#v, want advance to ask_class after skipping satisfied steps", journeyDecision)
	}
}

func TestResolveDoesNotBacktrackWhenCustomerCompletesPriorJourneyStepLate(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Hi, my name is John Smith and I'd like to book a flight for myself from Ben Gurion airport. We flight in the 12.10 and return in the 17.10."}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Hi John, thanks for reaching out! I see you're planning to fly from Ben Gurion airport. Could you please let me know your destination airport?"}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Suvarnabhumi Airport, please"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Journeys: []policy.Journey{{
				ID:   "Book Flight",
				When: []string{"book a flight"},
				States: []policy.JourneyNode{
					{ID: "ask_airports", Type: "message", Instruction: "ask for the source and destination airport", Next: []string{"ask_dates"}},
					{ID: "ask_dates", Type: "message", Instruction: "ask for the dates of the departure and return flight", Next: []string{"ask_class"}},
					{ID: "ask_class", Type: "message", Instruction: "ask whether they want economy or business class"},
				},
			}},
		}},
		[]journey.Instance{{
			ID:        "journey_1",
			SessionID: "sess_1",
			JourneyID: "Book Flight",
			StateID:   "ask_dates",
			Path:      []string{"ask_dates"},
			Status:    journey.StatusActive,
			UpdatedAt: now,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	journeyDecision := view.JourneyProgressStage.Decision
	if journeyDecision.Action != "advance" || journeyDecision.NextState != "ask_class" {
		t.Fatalf("journey decision = %#v, want advance to ask_class instead of backtrack", journeyDecision)
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
	analysis := view.ResponseAnalysisStage.Analysis
	if !analysis.NeedsStrictMode || analysis.RecommendedTemplate != "Approved strict template." {
		t.Fatalf("response analysis = %#v, want strict template recommendation", analysis)
	}
	if view.CompositionMode != "strict" {
		t.Fatalf("composition mode = %s, want strict after response analysis", view.CompositionMode)
	}
}

func TestResolveWithRouterPreservesToolSatisfiedResponseAnalysis(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"checks\":[{\"id\":\"lookup_status\",\"applies\":true,\"rationale\":\"structured yes\"}]}"}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"needs_revision\":false,\"needs_strict_mode\":false,\"recommended_template\":\"\",\"rationale\":\"already handled\",\"analyzed_guidelines\":[{\"id\":\"lookup_status\",\"already_satisfied\":false,\"requires_response\":true,\"requires_template\":false,\"rationale\":\"model omitted tool event\"}]}"}}]}`))
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

	now := time.Now().UTC()
	view, err := ResolveWithRouter(
		context.Background(),
		router,
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Where is my return?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I checked the return status. It is currently in transit."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "worker",
				Kind:      "tool",
				CreatedAt: now,
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_return_status",
						"arguments": map[string]any{"return_id": "ret_123"},
						"result":    map[string]any{"data": "RETURN STATUS: In transit"},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "lookup_status",
				When:  "return status",
				Then:  "Check the return status for the customer.",
				Track: true,
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveWithRouter() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || !item.SatisfiedByToolEvent || item.RequiresResponse {
		t.Fatalf("analyzed guideline = %#v, want tool satisfaction preserved across ARQ merge", item)
	}
}

func TestResolveIncludesTrackedSatisfiedGuidelineInResponseAnalysis(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Where is my return?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I checked the return status. It is currently in transit."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "worker",
				Kind:      "tool",
				CreatedAt: now,
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_return_status",
						"arguments": map[string]any{"return_id": "ret_123"},
						"result":    map[string]any{"data": "RETURN STATUS: In transit"},
					},
				}},
			},
			{
				ID:        "evt_4",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "Thanks, anything else I should know?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "lookup_status",
				When:  "customer asks where their return is",
				Then:  "check the return status",
				Track: true,
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one tracked analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if item.ID != "lookup_status" || !item.AlreadySatisfied || !item.SatisfiedByToolEvent || item.RequiresResponse {
		t.Fatalf("analyzed guideline = %#v, want tracked guideline satisfied by tool event", item)
	}
}

func TestResolveMarksGuidelinePartiallyAppliedByAssistantHistory(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now.Add(-2 * time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "My order is late again."}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "message",
				CreatedAt: now.Add(-time.Second),
				Content:   []session.ContentPart{{Type: "text", Text: "I'm sorry about that."}},
			},
			{
				ID:        "evt_3",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Okay, what happens next?"}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:    "late_so_discount",
				When:  "customer complains that they didn't get the order on time",
				Then:  "Apologize and offer a discount.",
				Track: true,
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", analysis)
	}
	item := analysis.AnalyzedGuidelines[0]
	if item.ID != "late_so_discount" || item.AppliedDegree != "partial" || item.AlreadySatisfied || !item.RequiresResponse || item.SatisfactionSource != "assistant_message" {
		t.Fatalf("analyzed guideline = %#v, want partial assistant-message application with response still required", item)
	}
}

func TestResolveMatchesGuidelineFromStagedToolEventAge(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "I want a sweet drink recommendation"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "tool",
				CreatedAt: now.Add(time.Second),
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_user_age",
						"arguments": map[string]any{"user_id": "199877"},
						"result":    map[string]any{"data": 16},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{
				{ID: "suggest_drink_underage", When: "drink under 21", Then: "Recommend a sweet non-alcoholic drink."},
				{ID: "suggest_drink_adult", When: "drink 21 or older", Then: "Recommend an alcoholic option if appropriate."},
			},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "suggest_drink_underage") {
		t.Fatalf("matched guidelines = %#v, want suggest_drink_underage", view.MatchFinalizeStage.MatchedGuidelines)
	}
	if containsGuideline(view.MatchFinalizeStage.MatchedGuidelines, "suggest_drink_adult") {
		t.Fatalf("matched guidelines = %#v, do not want suggest_drink_adult", view.MatchFinalizeStage.MatchedGuidelines)
	}
}

func TestResolveMarksToolAlreadyStagedFromToolEvent(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Can you check my return status?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "tool",
				CreatedAt: now.Add(time.Second),
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_return_status",
						"arguments": map[string]any{"return_id": "ret_123"},
						"result":    map[string]any{"data": "ORDER STATUS: Confirmed"},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "return_status",
				When: "return status",
				Then: "Check the return status for the customer.",
			}},
			GuidelineToolAssociations: []policy.GuidelineToolAssociation{
				{GuidelineID: "return_status", ToolID: "local:get_return_status"},
			},
		}},
		nil,
		[]tool.CatalogEntry{{
			ID:          "local:get_return_status",
			ProviderID:  "local",
			Name:        "get_return_status",
			Description: "Check the return status",
		}},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	state := view.ToolCandidateStates()["local.get_return_status"]
	if state != "already_staged" && state != "already_satisfied" {
		t.Fatalf("tool candidate state = %q, want already_staged or already_satisfied", state)
	}
	toolDecision := view.ToolDecisionStage.Decision
	if toolDecision.SelectedTool != "" || toolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want no runnable tool because call is already staged/satisfied", toolDecision)
	}
}

func TestResolveResponseAnalysisUsesStagedToolEvent(t *testing.T) {
	now := time.Now().UTC()
	view, err := Resolve(
		[]session.Event{
			{
				ID:        "evt_1",
				SessionID: "sess_1",
				Source:    "customer",
				Kind:      "message",
				CreatedAt: now,
				Content:   []session.ContentPart{{Type: "text", Text: "Can you check my return status?"}},
			},
			{
				ID:        "evt_2",
				SessionID: "sess_1",
				Source:    "ai_agent",
				Kind:      "tool",
				CreatedAt: now.Add(time.Second),
				Content: []session.ContentPart{{
					Type: "tool_call",
					Meta: map[string]any{
						"tool_id":   "local:get_return_status",
						"arguments": map[string]any{"return_id": "ret_123"},
						"result":    map[string]any{"data": "RETURN STATUS: In transit"},
					},
				}},
			},
		},
		[]policy.Bundle{{
			ID:      "bundle_1",
			Version: "v1",
			Guidelines: []policy.Guideline{{
				ID:   "return_status",
				When: "return status",
				Then: "Check the return status for the customer.",
			}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if len(analysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("analyzed guidelines = %#v, want 1", analysis.AnalyzedGuidelines)
	}
	item := analysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || !item.SatisfiedByToolEvent {
		t.Fatalf("response analysis item = %#v, want satisfied by tool event", item)
	}
	if item.RequiresResponse {
		t.Fatalf("response analysis item = %#v, do not want response still required", item)
	}
}

func TestConditionEvidenceTracksContradictoryAgeSignal(t *testing.T) {
	evidence := semantics.DefaultConditionEvaluator{}.Evaluate(semantics.ConditionContext{Condition: "drink 21 or older", Text: "I am 19 and want a drink"})
	if evidence.Applies {
		t.Fatalf("condition evidence = %#v, do not want applies on contradictory age", evidence)
	}
	if evidence.Score >= 0 {
		t.Fatalf("condition evidence = %#v, want negative score for contradictory age evidence", evidence)
	}
	if evidence.Signal != "age_fact" {
		t.Fatalf("condition evidence signal = %q, want age_fact", evidence.Signal)
	}
}

func TestConditionEvidenceUsesSemanticSignalsInsteadOfRawLexicalOnly(t *testing.T) {
	evidence := semantics.DefaultConditionEvaluator{}.Evaluate(semantics.ConditionContext{Condition: "check the return status", Text: "can you check tracking for me"})
	if !evidence.Applies {
		t.Fatalf("condition evidence = %#v, want applies from semantic status/tracking signal", evidence)
	}
	if evidence.Signal != "semantic_overlap" {
		t.Fatalf("condition evidence signal = %q, want semantic_overlap", evidence.Signal)
	}
}

func TestDetectedSemanticSignalsCollectsCentralFamilies(t *testing.T) {
	base := signalSet(canonicalKeywordSignals("please arrange store pickup and check tracking"))
	signals := detectedSemanticSignals("please arrange store pickup and check tracking", base)
	if !slices.Contains(signals, "pickup") {
		t.Fatalf("detected semantic signals = %#v, want pickup", signals)
	}
	if !slices.Contains(signals, "return_status") {
		t.Fatalf("detected semantic signals = %#v, want return_status", signals)
	}
}

func TestCanonicalKeywordFamilyTableMapsSharedParent(t *testing.T) {
	parent, ok := semantics.CanonicalKeywordFamily("motorcycle")
	if !ok {
		t.Fatalf("canonicalKeywordFamily(motorcycle) did not resolve")
	}
	if parent != "vehicle" {
		t.Fatalf("canonicalKeywordFamily(motorcycle) = %q, want vehicle", parent)
	}
}

func TestNormalizedTokensUseSharedAliasAndStopwordRules(t *testing.T) {
	tokens := semantics.NormalizedTokens("Hey, can you book the vehicle?")
	if !slices.Contains(tokens, "hello") {
		t.Fatalf("normalizedTokens = %#v, want hello alias", tokens)
	}
	if slices.Contains(tokens, "the") {
		t.Fatalf("normalizedTokens = %#v, do not want stopword 'the'", tokens)
	}
}

func TestJourneyStateSatisfactionUsesStructuredEvidence(t *testing.T) {
	state := policy.JourneyNode{
		ID:          "ask_destination",
		Instruction: "Ask for the destination city",
	}
	result := semantics.DefaultJourneySatisfactionEvaluator{}.Evaluate(semantics.JourneyStateContext{
		Text:                    "Business class to Singapore tomorrow",
		State:                   state,
		EdgeCondition:           "",
		LatestTurn:              true,
		CustomerSatisfiedAnswer: customerSatisfiedGuideline,
	})
	if !result.Satisfied {
		t.Fatalf("journey state satisfaction = %#v, want satisfied", result)
	}
	if result.Source == "" {
		t.Fatalf("journey state satisfaction = %#v, want explicit source", result)
	}
}

func TestToolCandidateGroundingEvidencePrefersJourneyState(t *testing.T) {
	evidence := semantics.DefaultToolGroundingEvaluator{}.Evaluate(semantics.ToolGroundingContext{
		LatestCustomerText: "Please check stock",
		ActiveStateTool:    "check_stock",
		ToolName:           "check_stock",
		ToolDescription:    "Check stock availability",
	})
	if !evidence.Grounded {
		t.Fatalf("grounding evidence = %#v, want grounded", evidence)
	}
	if evidence.Source != "journey_state" {
		t.Fatalf("grounding evidence source = %q, want journey_state", evidence.Source)
	}
}

func TestInferJourneyBacktrackIntentSeparatesRestartAndSameProcess(t *testing.T) {
	sameProcess := semantics.DefaultJourneyBacktrackEvaluator{}.Evaluate(semantics.JourneyBacktrackContext{LatestCustomerText: "Actually change that to business class"})
	if !sameProcess.RequiresBacktrack || sameProcess.RestartFromRoot {
		t.Fatalf("same-process intent = %#v, want same-process backtrack", sameProcess)
	}

	restart := semantics.DefaultJourneyBacktrackEvaluator{}.Evaluate(semantics.JourneyBacktrackContext{LatestCustomerText: "Let's start over with a different booking"})
	if !restart.RequiresBacktrack || !restart.RestartFromRoot {
		t.Fatalf("restart intent = %#v, want restart-from-root backtrack", restart)
	}
}

func TestBacktrackIntentSignalsDetectRestartWithoutPhraseListOrderDependency(t *testing.T) {
	signals := backtrackIntentSignals("we need another booking, restart this")
	if !signals.Restart {
		t.Fatalf("backtrack intent signals = %#v, want restart", signals)
	}
	if signals.SameProcess {
		t.Fatalf("backtrack intent signals = %#v, do not want same-process", signals)
	}
}

func TestCustomerDependencyEvidenceRequiresClarificationWhenMissing(t *testing.T) {
	evidence := semantics.EvaluateGuidelineCustomerDependency(
		policy.Guideline{ID: "g1", Then: "Ask for the reason for the return.", Scope: "customer"},
		"I need help",
		false,
		false,
	)
	if !evidence.CustomerDependent {
		t.Fatalf("customer dependency evidence = %#v, want customer dependent", evidence)
	}
	if len(evidence.MissingData) == 0 {
		t.Fatalf("customer dependency evidence = %#v, want missing customer data", evidence)
	}
}

func TestCustomerSatisfiedGuidelineDoesNotTreatAddressAsDeliveryPickupChoice(t *testing.T) {
	item := policy.Guideline{ID: "g1", Then: "Ask for the delivery address.", Scope: "customer"}
	if customerSatisfiedGuideline("actually let's switch this to store pickup", item) {
		t.Fatalf("customerSatisfiedGuideline() incorrectly treated a delivery address request as satisfied by a pickup choice")
	}
}

func TestAssessActionCoverageDetectsPartialCoverage(t *testing.T) {
	evidence := semantics.EvaluateActionCoverage("we apologized to the customer", "Apologize and offer a discount", toolHistorySatisfiesInstruction, containsEquivalentInstruction, splitActionSegments, segmentSatisfiedByHistory, dedupe)
	if evidence.AppliedDegree != "partial" {
		t.Fatalf("action coverage evidence = %#v, want partial", evidence)
	}
	if evidence.Source != "assistant_message" {
		t.Fatalf("action coverage source = %q, want assistant_message", evidence.Source)
	}
}

func TestMatchedInstructionCoverageSignalsDetectsToolStatusCoverage(t *testing.T) {
	matched := matchedInstructionCoverageSignals("Check the return status", "The tracking result says the return status is pending")
	if len(matched) == 0 || matched[0] != "return_status" {
		t.Fatalf("matched instruction coverage = %#v, want return_status", matched)
	}
}

func TestEvaluateConditionConflictDetectsOpposingAgeBranch(t *testing.T) {
	active := map[string]policy.Guideline{
		"adult": {ID: "adult", When: "drink 21 or older"},
	}
	decision := evaluateConditionConflict(
		MatchingContext{LatestCustomerText: "I am 19 and want a drink"},
		policy.Guideline{ID: "underage", When: "drink under 21"},
		active,
	)
	if decision.ShouldDrop {
		t.Fatalf("condition conflict decision = %#v, do not want underage branch dropped", decision)
	}

	decision = evaluateConditionConflict(
		MatchingContext{LatestCustomerText: "I am 19 and want a drink"},
		policy.Guideline{ID: "adult", When: "drink 21 or older"},
		map[string]policy.Guideline{"underage": {ID: "underage", When: "drink under 21"}},
	)
	if !decision.ShouldDrop || decision.Reason != "condition_conflict" {
		t.Fatalf("condition conflict decision = %#v, want adult branch dropped as condition_conflict", decision)
	}
}

func TestActionableConditionEvidencePrefersConversationSignal(t *testing.T) {
	ctx := MatchingContext{
		LatestCustomerText: "yes",
		ConversationText:   "I want to ask about a refund for a damaged order. yes",
	}
	evidence := semantics.EvaluateConditionAcrossTexts("damaged order refund", matchingSource(ctx), ctx.ConversationText)
	if !evidence.Applies {
		t.Fatalf("actionable condition evidence = %#v, want applies", evidence)
	}
	if evidence.Score <= 0 {
		t.Fatalf("actionable condition evidence = %#v, want positive score", evidence)
	}
}

func TestToolSelectionEvidenceDetectsSpecializedTool(t *testing.T) {
	candidate := ToolCandidate{ToolID: "check_motorcycle_price", ReferenceTools: []string{"check_vehicle_price"}}
	evidence := semantics.DefaultToolSelectionEvaluator{}.Evaluate(semantics.ToolSelectionContext{
		CandidateID:      candidate.ToolID,
		CandidateTerms:   semantics.Signals("check motorcycle price"),
		ReferenceToolIDs: candidate.ReferenceTools,
		CandidateSets: map[string][]string{
			"check_motorcycle_price": semantics.Signals("check motorcycle price"),
			"check_vehicle_price":    semantics.Signals("check vehicle price"),
		},
	})
	if !evidence.Specialized {
		t.Fatalf("tool selection evidence = %#v, want specialized", evidence)
	}
}

func TestSemanticSpecializationUsesSharedCategoryEvidence(t *testing.T) {
	if !semantics.SemanticSpecialization([]string{"schedule", "confirmation"}, []string{"appointment", "email"}) {
		t.Fatalf("semanticSpecialization() want shared category specialization signal")
	}
}

func TestSemanticCategoriesUseSharedVocabularyTables(t *testing.T) {
	categories := semantics.Categories([]string{"motorcycle", "confirmation"})
	if _, ok := categories["vehicle"]; !ok {
		t.Fatalf("semanticCategories = %#v, want vehicle", categories)
	}
	if _, ok := categories["confirmation"]; !ok {
		t.Fatalf("semanticCategories = %#v, want confirmation", categories)
	}
}

func TestObservationConditionEvidenceUsesConversationContext(t *testing.T) {
	ctx := MatchingContext{
		LatestCustomerText: "yes",
		ConversationText:   "The customer reported a damaged order and wants a refund. yes",
	}
	evidence := semantics.EvaluateConditionAcrossTexts("damaged order", matchingSource(ctx), ctx.ConversationText)
	if !evidence.Applies {
		t.Fatalf("observation condition evidence = %#v, want applies", evidence)
	}
}

func TestInferArgumentFromTextExtractsDateField(t *testing.T) {
	value, ok := inferArgumentFromText("date", toolArgumentSpec{}, "please book it for tomorrow morning")
	if !ok {
		t.Fatalf("inferArgumentFromText(date) did not extract a date-like value")
	}
	if value == "" {
		t.Fatalf("inferArgumentFromText(date) = %#v, want non-empty value", value)
	}
}

func TestInferArgumentFromTextTrimsDestinationSpan(t *testing.T) {
	value, ok := inferArgumentFromText("destination", toolArgumentSpec{Choices: []string{"Singapore"}}, "book it to singapore tomorrow")
	if !ok {
		t.Fatalf("inferArgumentFromText(destination) did not extract a destination")
	}
	if value != "Singapore" {
		t.Fatalf("inferArgumentFromText(destination) = %#v, want Singapore", value)
	}
}

func TestSlotKindForFieldUsesSharedDefinitions(t *testing.T) {
	kind := semantics.SlotKindForField("product_name")
	if kind != argumentSlotProductLike {
		t.Fatalf("slotKindForField(product_name) = %q, want product_like", kind)
	}
}

func TestSlotExtractorForKindUsesSharedDefinitions(t *testing.T) {
	extractor, ok := semantics.SlotExtractorForKind(argumentSlotDestination)
	if !ok {
		t.Fatalf("slotExtractorForKind(destination) did not resolve")
	}
	if !slices.Contains(extractor.Markers, "to") {
		t.Fatalf("slotExtractorForKind(destination) = %#v, want 'to' marker", extractor)
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

func (view EngineResult) ToolCandidateStates() map[string]string {
	toolPlan := view.ToolPlanStage.Plan
	out := map[string]string{}
	for _, item := range toolPlan.Candidates {
		out[item.ToolID] = item.DecisionState
	}
	return out
}

func containsSuppressedGuideline(items []SuppressedGuideline, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func containsResolutionRecord(items []ResolutionRecord, entityID string, kind ResolutionKind) bool {
	for _, item := range items {
		if item.EntityID == entityID && item.Kind == kind {
			return true
		}
	}
	return false
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
