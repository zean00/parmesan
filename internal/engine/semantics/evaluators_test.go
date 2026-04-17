package semantics

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestEvaluateConditionAgeBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condition string
		text      string
		applies   bool
		score     int
	}{
		{
			name:      "under 21 applies",
			condition: "customer is under 21",
			text:      "I am 19 years old",
			applies:   true,
			score:     3,
		},
		{
			name:      "under 21 contradicted",
			condition: "customer is under 21",
			text:      "I am 25 years old",
			applies:   false,
			score:     -3,
		},
		{
			name:      "21 or older applies",
			condition: "customer is 21 or older",
			text:      "I am 25 years old",
			applies:   true,
			score:     3,
		},
		{
			name:      "21 or older contradicted",
			condition: "customer is 21 or older",
			text:      "I am 19 years old",
			applies:   false,
			score:     -3,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EvaluateCondition(tc.condition, tc.text)
			if got.Applies != tc.applies {
				t.Fatalf("Applies = %t, want %t", got.Applies, tc.applies)
			}
			if got.Score != tc.score {
				t.Fatalf("Score = %d, want %d", got.Score, tc.score)
			}
			if got.Signal != "age_fact" {
				t.Fatalf("Signal = %q, want %q", got.Signal, "age_fact")
			}
		})
	}
}

func TestEvaluateConditionAcrossTextsChoosesBestEvidence(t *testing.T) {
	t.Parallel()

	got := EvaluateConditionAcrossTexts(
		"customer is under 21",
		"I am 32 years old",
		"I am 19 years old",
	)
	if !got.Applies {
		t.Fatalf("Applies = false, want true")
	}
	if got.Score != 3 {
		t.Fatalf("Score = %d, want 3", got.Score)
	}
}

func TestEvaluateJourneyStateSatisfiedBySemanticSlot(t *testing.T) {
	t.Parallel()

	state := policy.JourneyNode{
		ID:          "ask_destination",
		Instruction: "What is your destination airport?",
	}
	got := EvaluateJourneyState("I need to fly to Jakarta airport", nil, state, "", true, nil)
	if !got.Satisfied {
		t.Fatalf("Satisfied = false, want true")
	}
	if got.Source != string(SatisfactionSourceState) {
		t.Fatalf("Source = %q, want %q", got.Source, string(SatisfactionSourceState))
	}
}

func TestEvaluateJourneyStateSatisfiedByCustomerAnswer(t *testing.T) {
	t.Parallel()

	state := policy.JourneyNode{
		ID:          "ask_reason",
		Instruction: "What is the reason for your visit?",
	}
	got := EvaluateJourneyState(
		"I want to update my reservation",
		nil,
		state,
		"",
		true,
		func(text string, item policy.Guideline) bool {
			return text == "I want to update my reservation" && item.Then == "What is the reason for your visit?"
		},
	)
	if !got.Satisfied {
		t.Fatalf("Satisfied = false, want true")
	}
	if got.Source != string(SatisfactionSourceCustomer) {
		t.Fatalf("Source = %q, want %q", got.Source, string(SatisfactionSourceCustomer))
	}
}

func TestJourneyBacktrackIntent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text            string
		requires        bool
		restartFromRoot bool
		source          string
	}{
		{text: "Actually go back and change the previous option", requires: true, restartFromRoot: false, source: "same_process"},
		{text: "Let's start over with a different booking", requires: true, restartFromRoot: true, source: "restart"},
		{text: "Please continue", requires: true, restartFromRoot: false, source: "same_process"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()
			got := DefaultJourneyBacktrackEvaluator{}.Evaluate(JourneyBacktrackContext{LatestCustomerText: tc.text})
			if got.RequiresBacktrack != tc.requires {
				t.Fatalf("RequiresBacktrack = %t, want %t", got.RequiresBacktrack, tc.requires)
			}
			if got.RestartFromRoot != tc.restartFromRoot {
				t.Fatalf("RestartFromRoot = %t, want %t", got.RestartFromRoot, tc.restartFromRoot)
			}
			if got.Source != tc.source {
				t.Fatalf("Source = %q, want %q", got.Source, tc.source)
			}
		})
	}
}

func TestEvaluateActionCoverage(t *testing.T) {
	t.Parallel()

	t.Run("tool event satisfied", func(t *testing.T) {
		t.Parallel()
		got := EvaluateActionCoverage(
			"system executed tool and returned status",
			"Check return status",
			func(history, instruction string) bool { return true },
			nil,
			nil,
			nil,
			nil,
		)
		if got.AppliedDegree != string(CoverageKindFull) {
			t.Fatalf("AppliedDegree = %q, want %q", got.AppliedDegree, string(CoverageKindFull))
		}
		if got.Source != "tool_event" {
			t.Fatalf("Source = %q, want %q", got.Source, "tool_event")
		}
	})

	t.Run("partial assistant coverage", func(t *testing.T) {
		t.Parallel()
		got := EvaluateActionCoverage(
			"I apologized but did not mention the discount",
			"Apologize and mention the discount",
			nil,
			nil,
			func(instruction string) []string { return []string{"apology", "discount"} },
			func(history, segment string) ([]string, bool) {
				if segment == "apology" {
					return []string{"apology"}, true
				}
				return nil, false
			},
			func(parts []string) []string { return dedupeStrings(parts) },
		)
		if got.AppliedDegree != string(CoverageKindPartial) {
			t.Fatalf("AppliedDegree = %q, want %q", got.AppliedDegree, string(CoverageKindPartial))
		}
		if got.Source != "assistant_message" {
			t.Fatalf("Source = %q, want %q", got.Source, "assistant_message")
		}
	})
}

func TestEvaluateGuidelineCustomerDependency(t *testing.T) {
	t.Parallel()

	t.Run("missing email", func(t *testing.T) {
		t.Parallel()
		item := policy.Guideline{Then: "Ask for the customer's email address"}
		got := EvaluateGuidelineCustomerDependency(item, "I need help with my booking", false, false)
		if !got.CustomerDependent {
			t.Fatalf("CustomerDependent = false, want true")
		}
		if len(got.MissingData) != 1 || got.MissingData[0] != "email" {
			t.Fatalf("MissingData = %#v, want [email]", got.MissingData)
		}
	})

	t.Run("email already present", func(t *testing.T) {
		t.Parallel()
		item := policy.Guideline{Then: "Ask for the customer's email address"}
		got := EvaluateGuidelineCustomerDependency(item, "My email is sahal@example.com", false, true)
		if !got.CustomerDependent {
			t.Fatalf("CustomerDependent = false, want true")
		}
		if len(got.MissingData) != 0 {
			t.Fatalf("MissingData = %#v, want none", got.MissingData)
		}
	})

	t.Run("recommendation does not require follow up", func(t *testing.T) {
		t.Parallel()
		item := policy.Guideline{Then: "Recommend Pepsi to the customer"}
		got := EvaluateGuidelineCustomerDependency(item, "I want a drink", false, true)
		if got.CustomerDependent {
			t.Fatalf("CustomerDependent = true, want false")
		}
	})
}

func TestToolGroundingEvaluator(t *testing.T) {
	t.Parallel()

	t.Run("active state grounds tool", func(t *testing.T) {
		t.Parallel()
		got := DefaultToolGroundingEvaluator{}.Evaluate(ToolGroundingContext{
			LatestCustomerText: "please lock my card now",
			ActiveStateTool:    "lock_card",
			ToolName:           "lock_card",
			ToolDescription:    "lock a payment card",
		})
		if !got.Grounded {
			t.Fatalf("Grounded = false, want true")
		}
		if got.Source != string(GroundingSourceJourneyState) {
			t.Fatalf("Source = %q, want %q", got.Source, string(GroundingSourceJourneyState))
		}
	})

	t.Run("customer text grounds tool", func(t *testing.T) {
		t.Parallel()
		got := DefaultToolGroundingEvaluator{}.Evaluate(ToolGroundingContext{
			LatestCustomerText: "can you check my return status",
			ToolName:           "check_return_status",
			ToolDescription:    "look up the return status for an order",
		})
		if !got.Grounded {
			t.Fatalf("Grounded = false, want true")
		}
		if got.Source != string(GroundingSourceCustomerText) {
			t.Fatalf("Source = %q, want %q", got.Source, string(GroundingSourceCustomerText))
		}
	})
}

func TestToolSelectionEvaluator(t *testing.T) {
	t.Parallel()

	t.Run("specialized beats reference", func(t *testing.T) {
		t.Parallel()
		ctx := ToolSelectionContextFromIDs(
			"motorcycle_price_lookup",
			[]string{"vehicle_price_lookup"},
			"",
			[]string{"motorcycle_price_lookup", "vehicle_price_lookup"},
		)
		got := DefaultToolSelectionEvaluator{}.Evaluate(ctx)
		if !got.Specialized {
			t.Fatalf("Specialized = false, want true")
		}
		if got.ReferenceTo != "vehicle_price_lookup" {
			t.Fatalf("ReferenceTo = %q, want %q", got.ReferenceTo, "vehicle_price_lookup")
		}
	})

	t.Run("confirmation runs in tandem with scheduling", func(t *testing.T) {
		t.Parallel()
		ctx := ToolSelectionContextFromIDs(
			"send_confirmation",
			[]string{"schedule_appointment"},
			"schedule_appointment",
			[]string{"send_confirmation", "schedule_appointment"},
		)
		got := DefaultToolSelectionEvaluator{}.Evaluate(ctx)
		if !got.RunInTandem {
			t.Fatalf("RunInTandem = false, want true")
		}
		if got.ReferenceTo != "schedule_appointment" {
			t.Fatalf("ReferenceTo = %q, want %q", got.ReferenceTo, "schedule_appointment")
		}
	})
}

func TestCoverageSignalHelpers(t *testing.T) {
	t.Parallel()

	if got := InstructionCoverageSignals("Apologize and mention the discount"); len(got) != 2 || got[0] != "apology" || got[1] != "discount" {
		t.Fatalf("InstructionCoverageSignals() = %#v, want apology+discount", got)
	}

	if got := HistoryCoverageSignals("I apologize, and I applied the discount already."); len(got) != 2 || got[0] != "apology" || got[1] != "discount" {
		t.Fatalf("HistoryCoverageSignals() = %#v, want apology+discount", got)
	}

	if got := ToolHistoryCoverageSignals("The return status shows it was delivered."); len(got) != 1 || got[0] != string(SignalReturnStatus) {
		t.Fatalf("ToolHistoryCoverageSignals() = %#v, want return_status", got)
	}
}

func TestResponseTextSatisfiesInstruction(t *testing.T) {
	t.Parallel()

	if !ResponseTextSatisfiesInstruction("I apologize and mention the discount", "Apologize and mention the discount") {
		t.Fatalf("ResponseTextSatisfiesInstruction() = false, want true")
	}
	if ResponseTextSatisfiesInstruction("I apologize only", "Apologize and mention the discount") {
		t.Fatalf("ResponseTextSatisfiesInstruction() = true, want false")
	}
}

func TestAnalyzeTextAndArgumentExtraction(t *testing.T) {
	t.Parallel()

	snapshot := AnalyzeText("Please book a flight to Jakarta airport tomorrow in business class")
	if !snapshot.HasLocation {
		t.Fatalf("HasLocation = false, want true")
	}
	if !snapshot.HasDate {
		t.Fatalf("HasDate = false, want true")
	}
	if !snapshot.HasTravelClass {
		t.Fatalf("HasTravelClass = false, want true")
	}

	t.Run("extract date marker", func(t *testing.T) {
		t.Parallel()
		got := DefaultArgumentExtractor{}.Extract(ArgumentExtractionContext{
			Field: "date",
			Text:  "please schedule it for tomorrow morning",
		})
		if got.Value != "tomorrow" {
			t.Fatalf("Value = %q, want %q", got.Value, "tomorrow")
		}
	})

	t.Run("extract choice", func(t *testing.T) {
		t.Parallel()
		got := DefaultArgumentExtractor{}.Extract(ArgumentExtractionContext{
			Field:   "drink",
			Choices: []string{"Pepsi", "Coke"},
			Text:    "I'd like Pepsi please",
		})
		if got.Value != "Pepsi" {
			t.Fatalf("Value = %q, want %q", got.Value, "Pepsi")
		}
	})

	t.Run("extract brand", func(t *testing.T) {
		t.Parallel()
		got := DefaultArgumentExtractor{}.Extract(ArgumentExtractionContext{
			Field: "brand",
			Text:  "show me samsung phones",
		})
		if got.Value != "Samsung" {
			t.Fatalf("Value = %q, want %q", got.Value, "Samsung")
		}
	})
}
