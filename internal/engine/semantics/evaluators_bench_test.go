package semantics

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func BenchmarkEvaluateConditionAcrossTexts(b *testing.B) {
	condition := "customer wants to book a flight and the traveler is under 21"
	texts := []string{
		"Hi, my name is John Smith and I'd like to book a flight from Ben Gurion airport to JFK.",
		"We leave on 12.10 and return on 17.10.",
		"I'm 19 if that affects anything.",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluateConditionAcrossTexts(condition, texts...)
	}
}

func BenchmarkEvaluateJourneyState(b *testing.B) {
	state := policy.JourneyNode{
		ID:          "ask_destination",
		Instruction: "What is your destination airport?",
	}
	customerHistory := []string{
		"I want to book a flight.",
		"I'm traveling from Tel Aviv.",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluateJourneyState(
			"I need to fly to Jakarta airport tomorrow",
			customerHistory,
			state,
			"",
			false,
			nil,
		)
	}
}

func BenchmarkEvaluateActionCoverage(b *testing.B) {
	history := "I'm sorry about the delay, and I'll apply a discount to this order."
	instruction := "Apologize and offer a discount."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluateActionCoverage(
			history,
			instruction,
			nil,
			nil,
			func(instruction string) []string { return InstructionCoverageSignals(instruction) },
			func(history, segment string) ([]string, bool) {
				if ResponseTextSatisfiesInstruction(history, segment) {
					return []string{segment}, true
				}
				return nil, false
			},
			func(parts []string) []string { return dedupeStrings(parts) },
		)
	}
}

func BenchmarkToolSelectionContextAndEvaluate(b *testing.B) {
	candidateIDs := []string{
		"check_motorcycle_price",
		"check_vehicle_price",
		"schedule_appointment",
		"send_confirmation_email",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := ToolSelectionContextFromIDs(
			"check_motorcycle_price",
			[]string{"check_vehicle_price"},
			"",
			candidateIDs,
		)
		_ = DefaultToolSelectionEvaluator{}.Evaluate(ctx)
	}
}
