package parity

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScenarioTextMutationsPreserveKeyOutcomes(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}

	tests := []struct {
		name      string
		scenarioID string
		mutate    func(Scenario) Scenario
		assert    func(*testing.T, NormalizedResult)
	}{
		{
			name:       "under_21_with_punctuation_noise",
			scenarioID: "journey_dependency_guideline_under_21",
			mutate: func(s Scenario) Scenario {
				s.Transcript[0].Text = "Hi!!! my name is John Smith, and I'd like to book a flight from Ben Gurion airport to JFK... We fly on 12.10 and return on 17.10; I'm 19."
				return s
			},
			assert: func(t *testing.T, got NormalizedResult) {
				t.Helper()
				if !slices.Contains(got.MatchedGuidelines, "under_21") {
					t.Fatalf("MatchedGuidelines = %#v, want under_21", got.MatchedGuidelines)
				}
				if got.JourneyDecision != "start" {
					t.Fatalf("JourneyDecision = %q, want start", got.JourneyDecision)
				}
			},
		},
		{
			name:       "schedule_confirmation_with_case_noise",
			scenarioID: "tool_schedule_confirmation_runs_in_tandem",
			mutate: func(s Scenario) Scenario {
				s.Transcript[0].Text = strings.ToUpper("please schedule an appointment with Dr. Gabi tomorrow at 6pm and send me a confirmation email")
				return s
			},
			assert: func(t *testing.T, got NormalizedResult) {
				t.Helper()
				if got.SelectedTool != "local:schedule_appointment" {
					t.Fatalf("SelectedTool = %q, want local:schedule_appointment", got.SelectedTool)
				}
				if !sameSet(got.SelectedTools, []string{"local:schedule_appointment", "local:send_confirmation_email"}) {
					t.Fatalf("SelectedTools = %#v, want scheduling + confirmation", got.SelectedTools)
				}
			},
		},
		{
			name:       "entailed_tool_with_polite_filler",
			scenarioID: "tool_from_entailed_guideline",
			mutate: func(s Scenario) Scenario {
				s.Transcript[0].Text = "Hi there, please tell me what pizza topping I should take."
				return s
			},
			assert: func(t *testing.T, got NormalizedResult) {
				t.Helper()
				if !slices.Contains(got.MatchedGuidelines, "check_stock") {
					t.Fatalf("MatchedGuidelines = %#v, want check_stock", got.MatchedGuidelines)
				}
				if got.SelectedTool != "local:get_available_toppings" {
					t.Fatalf("SelectedTool = %q, want local:get_available_toppings", got.SelectedTool)
				}
			},
		},
		{
			name:       "staged_tool_age_with_extra_words",
			scenarioID: "staged_tool_age_underage_drink_match",
			mutate: func(s Scenario) Scenario {
				s.Transcript[0].Text = "Hi there, honestly I want a sweeter drink recommendation if you have one."
				s.Transcript[2].Text = "Sure, the account number is 199877."
				return s
			},
			assert: func(t *testing.T, got NormalizedResult) {
				t.Helper()
				if !slices.Contains(got.MatchedGuidelines, "suggest_drink_underage") {
					t.Fatalf("MatchedGuidelines = %#v, want suggest_drink_underage", got.MatchedGuidelines)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			scenario := fixtureScenario(t, fx, tc.scenarioID)
			scenario = tc.mutate(scenario)
			got, err := RunParmesan(context.Background(), scenario)
			if err != nil {
				t.Fatalf("RunParmesan() error = %v", err)
			}
			tc.assert(t, got)
		})
	}
}
