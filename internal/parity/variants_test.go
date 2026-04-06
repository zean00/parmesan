package parity

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestScenarioVariantsRemainStable(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}

	t.Run("under_21_flight_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "journey_dependency_guideline_under_21")
		scenario.Transcript[0].Text = "Hey, I'm John Smith and need to book a flight from Ben Gurion airport to JFK. We leave on 12.10 and come back on 17.10. I'm only 20, if that matters."

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if !slices.Contains(got.MatchedGuidelines, "under_21") {
			t.Fatalf("MatchedGuidelines = %#v, want under_21", got.MatchedGuidelines)
		}
		if slices.Contains(got.MatchedGuidelines, "age_21_or_older") {
			t.Fatalf("MatchedGuidelines = %#v, do not want age_21_or_older", got.MatchedGuidelines)
		}
		if !semanticContains(got.ResponseText, "only economy class is available") {
			t.Fatalf("ResponseText = %q, want economy-only guidance", got.ResponseText)
		}
	})

	t.Run("discount_partial_apology_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "response_analysis_discount_partially_applied")
		scenario.Transcript[1].Text = "Sorry about the delay."

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if !slices.Contains(got.MatchedGuidelines, "late_so_discount") {
			t.Fatalf("MatchedGuidelines = %#v, want late_so_discount", got.MatchedGuidelines)
		}
		if !slices.Contains(got.ResponseAnalysisPartiallyApplied, "late_so_discount") && len(got.ResponseAnalysisStillRequired) == 0 {
			t.Fatalf("Response analysis = partial=%#v still_required=%#v, want guideline to remain live", got.ResponseAnalysisPartiallyApplied, got.ResponseAnalysisStillRequired)
		}
	})

	t.Run("schedule_and_confirm_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "tool_schedule_confirmation_runs_in_tandem")
		scenario.Transcript[0].Text = "Can you book me with Dr. Gabi for tomorrow at 6pm and email me a confirmation afterward?"

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if got.SelectedTool != "local:schedule_appointment" {
			t.Fatalf("SelectedTool = %q, want local:schedule_appointment", got.SelectedTool)
		}
		if !sameSet(got.SelectedTools, []string{"local:schedule_appointment", "local:send_confirmation_email"}) {
			t.Fatalf("SelectedTools = %#v, want scheduling + confirmation", got.SelectedTools)
		}
		tandem := got.ToolCandidateTandemWith["local:send_confirmation_email"]
		if !sameSet(tandem, []string{"local:schedule_appointment"}) {
			t.Fatalf("ToolCandidateTandemWith = %#v, want send_confirmation_email to run with schedule_appointment", got.ToolCandidateTandemWith)
		}
	})

	t.Run("entailed_tool_guideline_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "tool_from_entailed_guideline")
		scenario.Transcript[0].Text = "I need something with pineapple on it."

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if !slices.Contains(got.ExposedTools, "local:get_available_toppings") {
			t.Fatalf("ExposedTools = %#v, want local:get_available_toppings", got.ExposedTools)
		}
		if got.SelectedTool != "local:get_available_toppings" {
			t.Fatalf("SelectedTool = %q, want local:get_available_toppings", got.SelectedTool)
		}
	})

	t.Run("staged_tool_underage_drink_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "staged_tool_age_underage_drink_match")
		scenario.Transcript[0].Text = "Hey, I'm in the mood for a sweeter drink. What do you recommend?"
		scenario.Transcript[2].Text = "My account number is 199877"

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if !slices.Contains(got.MatchedGuidelines, "suggest_drink_underage") {
			t.Fatalf("MatchedGuidelines = %#v, want suggest_drink_underage", got.MatchedGuidelines)
		}
		if slices.Contains(got.MatchedGuidelines, "suggest_drink_adult") {
			t.Fatalf("MatchedGuidelines = %#v, do not want suggest_drink_adult", got.MatchedGuidelines)
		}
	})

	t.Run("specialized_motorcycle_price_paraphrase", func(t *testing.T) {
		scenario := fixtureScenario(t, fx, "tool_reference_motorcycle_price_specialized_choice")
		scenario.Transcript[0].Text = "What's the price for a Yamaha motorcycle?"

		got, err := RunParmesan(context.Background(), scenario)
		if err != nil {
			t.Fatalf("RunParmesan() error = %v", err)
		}
		if got.SelectedTool != "local:check_motorcycle_price" {
			t.Fatalf("SelectedTool = %q, want local:check_motorcycle_price", got.SelectedTool)
		}
		if got.ToolCandidateRejectedBy["local:check_vehicle_price"] != "local:check_motorcycle_price" {
			t.Fatalf("ToolCandidateRejectedBy = %#v, want vehicle tool rejected by motorcycle tool", got.ToolCandidateRejectedBy)
		}
	})
}

func fixtureScenario(t *testing.T, fx Fixture, id string) Scenario {
	t.Helper()
	for _, scenario := range fx.Scenarios {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("fixture scenario %q not found", id)
	return Scenario{}
}
