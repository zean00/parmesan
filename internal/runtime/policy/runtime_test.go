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
	if view.JourneyDecision.Action != "backtrack" || view.JourneyDecision.BacktrackTo != "choose_method" {
		t.Fatalf("journey decision = %#v, want backtrack to choose_method branch point", view.JourneyDecision)
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
	if view.JourneyDecision.Action != "advance" || view.JourneyDecision.NextState != "ask_store" {
		t.Fatalf("journey decision = %#v, want advance to ask_store", view.JourneyDecision)
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
	if view.JourneyDecision.Action != "backtrack" || view.JourneyDecision.BacktrackTo != "ask_size" || view.JourneyDecision.NextState != "check_stock" {
		t.Fatalf("journey decision = %#v, want backtrack to ask_size then fast-forward to check_stock", view.JourneyDecision)
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
	if view.JourneyDecision.Action != "backtrack" || view.JourneyDecision.BacktrackTo != "ask_dropoff_location" || view.JourneyDecision.NextState != "" {
		t.Fatalf("journey decision = %#v, want backtrack to ask_dropoff_location without fast-forwarding on stale history", view.JourneyDecision)
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
	if !containsGuideline(view.MatchedGuidelines, "refund_help") || !containsGuideline(view.MatchedGuidelines, "returns_closure") {
		t.Fatalf("matched guidelines = %#v, want refund_help and entailed returns_closure", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, want standalone guideline", view.MatchedGuidelines)
	}
	if containsGuidelineID(view.MatchedGuidelines, "journey_node:Drink Recommendation Journey:ask_drink") {
		t.Fatalf("matched guidelines = %#v, do not want active journey node to survive higher-priority guideline", view.MatchedGuidelines)
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
	if containsGuidelineID(view.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, do not want standalone guideline to survive higher-priority journey", view.MatchedGuidelines)
	}
	if !containsSuppressedGuideline(view.SuppressedGuidelines, "standalone") {
		t.Fatalf("suppressed guidelines = %#v, want standalone deprioritized", view.SuppressedGuidelines)
	}
	if !containsGuidelineID(view.MatchedGuidelines, "journey_node:Drink Recommendation Journey:ask_drink") {
		t.Fatalf("matched guidelines = %#v, want active journey node to remain", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, want standalone guideline", view.MatchedGuidelines)
	}
	if view.ActiveJourney != nil || view.ActiveJourneyState != nil {
		t.Fatalf("active journey = %#v / %#v, want journey cleared by higher numerical priority guideline", view.ActiveJourney, view.ActiveJourneyState)
	}
	if !containsResolutionRecord(view.ResolutionRecords, "journey:Drink Recommendation Journey", ResolutionDeprioritized) {
		t.Fatalf("resolution records = %#v, want journey entity deprioritized", view.ResolutionRecords)
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
	if containsGuidelineID(view.MatchedGuidelines, "standalone") {
		t.Fatalf("matched guidelines = %#v, do not want standalone guideline to survive higher numerical priority journey", view.MatchedGuidelines)
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
	if containsGuidelineID(view.MatchedGuidelines, "depends_on_journey") {
		t.Fatalf("matched guidelines = %#v, do not want journey-dependent guideline when journey is deprioritized", view.MatchedGuidelines)
	}
	if !containsGuidelineID(view.MatchedGuidelines, "winner") {
		t.Fatalf("matched guidelines = %#v, want winner guideline", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "loser") {
		t.Fatalf("matched guidelines = %#v, want loser to remain active when winner is inactive", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "journey_node:Journey B:ask_b") {
		t.Fatalf("matched guidelines = %#v, want Journey B node to remain active when Journey A is inactive", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "winner") || !containsGuidelineID(view.MatchedGuidelines, "journey_condition") {
		t.Fatalf("matched guidelines = %#v, want winner and journey_condition", view.MatchedGuidelines)
	}
	if containsGuidelineID(view.MatchedGuidelines, "journey_node:Journey 1:recommend_product") {
		t.Fatalf("matched guidelines = %#v, do not want journey node to survive priority loss", view.MatchedGuidelines)
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
	if len(view.ToolPlan.Candidates) != 2 {
		t.Fatalf("tool candidates = %#v, want 2", view.ToolPlan.Candidates)
	}
	if len(view.ToolPlan.OverlappingGroups) != 1 {
		t.Fatalf("overlapping groups = %#v, want 1", view.ToolPlan.OverlappingGroups)
	}
	if len(view.ToolPlan.Batches) != 1 || view.ToolPlan.Batches[0].Kind != "overlapping_tools" || view.ToolPlan.Batches[0].SelectedTool == "" {
		t.Fatalf("tool batches = %#v, want one overlapping-tools batch with a selected winner", view.ToolPlan.Batches)
	}
	if view.ToolPlan.SelectedTool == "" || view.ToolDecision.SelectedTool == "" {
		t.Fatalf("tool plan/decision = %#v / %#v, want a selected tool", view.ToolPlan, view.ToolDecision)
	}
	states := map[string]string{}
	rejectedBy := map[string]string{}
	for _, item := range view.ToolPlan.Candidates {
		states[item.ToolID] = item.DecisionState
		rejectedBy[item.ToolID] = item.RejectedBy
	}
	if states[view.ToolPlan.SelectedTool] != "selected" {
		t.Fatalf("tool candidate states = %#v, want selected state for chosen tool", states)
	}
	for toolID, state := range states {
		if toolID == view.ToolPlan.SelectedTool {
			continue
		}
		if state != "rejected_overlap" {
			t.Fatalf("tool candidate states = %#v, want rejected_overlap for overlapping alternative", states)
		}
		if rejectedBy[toolID] != view.ToolPlan.SelectedTool {
			t.Fatalf("tool rejection provenance = %#v, want %s rejected by %s", rejectedBy, toolID, view.ToolPlan.SelectedTool)
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
	if len(view.ToolPlan.OverlappingGroups) != 1 {
		t.Fatalf("overlapping groups = %#v, want 1 transitive group", view.ToolPlan.OverlappingGroups)
	}
	if !sameStringSlice(view.ToolPlan.OverlappingGroups[0], []string{"aa", "bb", "cc"}) {
		t.Fatalf("overlap group = %#v, want [aa bb cc]", view.ToolPlan.OverlappingGroups[0])
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
	if len(view.ToolPlan.OverlappingGroups) != 0 {
		t.Fatalf("overlapping groups = %#v, want none without matched middle tool", view.ToolPlan.OverlappingGroups)
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
	if view.ToolPlan.SelectedTool != "lock_card" {
		t.Fatalf("tool plan = %#v, want selected lock_card", view.ToolPlan)
	}
	if len(view.ToolPlan.Candidates) != 1 || view.ToolPlan.Candidates[0].DecisionState != "already_staged" {
		t.Fatalf("tool candidates = %#v, want already_staged candidate state", view.ToolPlan.Candidates)
	}
	if len(view.ToolPlan.Batches) != 1 || view.ToolPlan.Batches[0].Kind != "single_tool" || view.ToolPlan.Batches[0].SelectedTool != "" {
		t.Fatalf("tool batches = %#v, want one single-tool batch with no runnable selection", view.ToolPlan.Batches)
	}
	if view.ToolDecision.SelectedTool != "" || view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want staged tool to be suppressed from execution", view.ToolDecision)
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
	if view.ToolPlan.SelectedTool != "ping" {
		t.Fatalf("tool plan = %#v, want ping selected", view.ToolPlan)
	}
	if len(view.ToolPlan.Candidates) != 1 || !view.ToolPlan.Candidates[0].AutoApproved || view.ToolPlan.Candidates[0].DecisionState != "selected" {
		t.Fatalf("tool candidates = %#v, want one auto-approved selected candidate", view.ToolPlan.Candidates)
	}
	if len(view.ToolPlan.Batches) != 1 || view.ToolPlan.Batches[0].Kind != "single_tool" || !view.ToolPlan.Batches[0].Simplified || view.ToolPlan.Batches[0].SelectedTool != "ping" {
		t.Fatalf("tool batches = %#v, want one simplified single-tool batch selecting ping", view.ToolPlan.Batches)
	}
	if view.ToolDecision.SelectedTool != "ping" || !view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want runnable ping selection", view.ToolDecision)
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
	states := map[string]string{}
	rejectedBy := map[string]string{}
	for _, item := range view.ToolPlan.Candidates {
		states[item.ToolID] = item.DecisionState
		rejectedBy[item.ToolID] = item.RejectedBy
	}
	if states["lock_card"] != "selected" {
		t.Fatalf("tool candidate states = %#v, want lock_card selected", states)
	}
	if states["generic_lookup"] != "rejected_ungrounded" {
		t.Fatalf("tool candidate states = %#v, want generic_lookup rejected_ungrounded", states)
	}
	if rejectedBy["generic_lookup"] != "lock_card" {
		t.Fatalf("tool rejection provenance = %#v, want generic_lookup rejected by lock_card", rejectedBy)
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
	if view.ToolPlan.SelectedTool != "check_motorcycle_price" {
		t.Fatalf("tool plan = %#v, want specialized motorcycle tool selected", view.ToolPlan)
	}
	reasons := map[string]string{}
	states := map[string]string{}
	for _, item := range view.ToolPlan.Candidates {
		reasons[item.ToolID] = item.Rationale
		states[item.ToolID] = item.DecisionState
	}
	if states["check_vehicle_price"] != "rejected_overlap" {
		t.Fatalf("tool candidate states = %#v, want generic vehicle tool rejected_overlap", states)
	}
	if !strings.Contains(strings.ToLower(reasons["check_motorcycle_price"]), "specialized") {
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
	var confirmation ToolCandidate
	var found bool
	for _, item := range view.ToolPlan.Candidates {
		if item.ToolID == "send_confirmation_email" {
			confirmation = item
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tool candidates = %#v, want send_confirmation_email candidate present", view.ToolPlan.Candidates)
	}
	if !slices.Contains(view.ToolPlan.SelectedTools, "schedule_appointment") || !slices.Contains(view.ToolPlan.SelectedTools, "send_confirmation_email") {
		t.Fatalf("selected tools = %#v, want both schedule_appointment and send_confirmation_email", view.ToolPlan.SelectedTools)
	}
	if !slices.Contains(confirmation.RunInTandemWith, "schedule_appointment") {
		t.Fatalf("confirmation candidate tandem targets = %#v, want schedule_appointment", confirmation.RunInTandemWith)
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
	if len(view.ToolPlan.Calls) != 2 {
		t.Fatalf("planned calls = %#v, want 2", view.ToolPlan.Calls)
	}
	firstArgs := view.ToolPlan.Calls[0].Arguments
	secondArgs := view.ToolPlan.Calls[1].Arguments
	if firstArgs["vendor"] == secondArgs["vendor"] || firstArgs["keyword"] == secondArgs["keyword"] {
		t.Fatalf("planned call args = %#v, want distinct vendor/keyword combinations", view.ToolPlan.Calls)
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
	if len(view.ToolPlan.Candidates) != 1 || view.ToolPlan.Candidates[0].DecisionState != "already_satisfied" {
		t.Fatalf("tool candidates = %#v, want already_satisfied candidate state", view.ToolPlan.Candidates)
	}
	if len(view.ToolPlan.Batches) != 1 || view.ToolPlan.Batches[0].Kind != "single_tool" || view.ToolPlan.Batches[0].SelectedTool != "" {
		t.Fatalf("tool batches = %#v, want one single-tool batch with no runnable selection", view.ToolPlan.Batches)
	}
	if view.ToolDecision.SelectedTool != "" || view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want already-satisfied tool to be suppressed from execution", view.ToolDecision)
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	got := make([]string, 0, len(view.MatchedGuidelines))
	for _, item := range view.MatchedGuidelines {
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
	if !containsGuidelineID(view.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive tag_any dependency", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "t2_member") {
		t.Fatalf("matched guidelines = %#v, want t2_member to survive without active t1 source", view.MatchedGuidelines)
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
	for _, item := range view.MatchedGuidelines {
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
	if containsGuidelineID(view.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup filtered by unmet tag_all dependency", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive dependency_any", view.MatchedGuidelines)
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
	if containsGuidelineID(view.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup filtered by unmet dependency_any", view.MatchedGuidelines)
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
	if !containsGuidelineID(view.MatchedGuidelines, "followup") {
		t.Fatalf("matched guidelines = %#v, want followup to survive mixed dependency + dependency_any", view.MatchedGuidelines)
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
	if view.ToolDecision.SelectedTool != "" || view.ToolPlan.SelectedTool != "" {
		t.Fatalf("selected tool = %#v / %#v, want blocked candidate not to remain selected", view.ToolDecision.SelectedTool, view.ToolPlan.SelectedTool)
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
	if view.ToolDecision.SelectedTool != "" || view.ToolPlan.SelectedTool != "" {
		t.Fatalf("selected tool = %#v / %#v, want blocked candidate not to remain selected", view.ToolDecision.SelectedTool, view.ToolPlan.SelectedTool)
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
	if view.JourneyDecision.Action == "advance" || view.JourneyDecision.NextState != "" {
		t.Fatalf("journey decision = %#v, do not want tool step to advance before execution", view.JourneyDecision)
	}
	if !slices.Contains(view.JourneyDecision.Missing, "tool_execution") {
		t.Fatalf("journey decision = %#v, want tool_execution missing signal", view.JourneyDecision)
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
	if view.JourneyDecision.Action != "advance" || view.JourneyDecision.NextState != "summarize_status" {
		t.Fatalf("journey decision = %#v, want tool step to advance after execution", view.JourneyDecision)
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
	if view.JourneyDecision.Action != "advance" || view.JourneyDecision.NextState != "collect_reason" {
		t.Fatalf("journey decision = %#v, want automatic fork advance to collect_reason", view.JourneyDecision)
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
	selected := selectBestBacktrackTarget(ctx, flow, []string{"ask_quantity", "ask_type", "ask_size", "ask_drinks", "check_stock"}, "check_stock", false)
	if selected == nil || selected.ID != "ask_size" {
		t.Fatalf("selected = %#v, want ask_size", selected)
	}
	if next := fastForwardJourneyState(ctx, flow, selected.ID); next != "check_stock" {
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one tracked analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("response analysis = %#v, want one analyzed guideline", view.ResponseAnalysis)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
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
	if !containsGuideline(view.MatchedGuidelines, "suggest_drink_underage") {
		t.Fatalf("matched guidelines = %#v, want suggest_drink_underage", view.MatchedGuidelines)
	}
	if containsGuideline(view.MatchedGuidelines, "suggest_drink_adult") {
		t.Fatalf("matched guidelines = %#v, do not want suggest_drink_adult", view.MatchedGuidelines)
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
	state := view.ToolCandidateStates()["get_return_status"]
	if state != "already_staged" && state != "already_satisfied" {
		t.Fatalf("tool candidate state = %q, want already_staged or already_satisfied", state)
	}
	if view.ToolDecision.SelectedTool != "" || view.ToolDecision.CanRun {
		t.Fatalf("tool decision = %#v, want no runnable tool because call is already staged/satisfied", view.ToolDecision)
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
	if len(view.ResponseAnalysis.AnalyzedGuidelines) != 1 {
		t.Fatalf("analyzed guidelines = %#v, want 1", view.ResponseAnalysis.AnalyzedGuidelines)
	}
	item := view.ResponseAnalysis.AnalyzedGuidelines[0]
	if !item.AlreadySatisfied || !item.SatisfiedByToolEvent {
		t.Fatalf("response analysis item = %#v, want satisfied by tool event", item)
	}
	if item.RequiresResponse {
		t.Fatalf("response analysis item = %#v, do not want response still required", item)
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

func (view ResolvedView) ToolCandidateStates() map[string]string {
	out := map[string]string{}
	for _, item := range view.ToolPlan.Candidates {
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
