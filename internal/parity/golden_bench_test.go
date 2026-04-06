package parity

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func BenchmarkRunParmesanGoldenScenarios(b *testing.B) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		b.Fatalf("LoadFixture() error = %v", err)
	}
	scenarioIDs := []string{
		"journey_custom_backtrack_and_fast_forward",
		"tool_schedule_confirmation_runs_in_tandem",
		"response_analysis_discount_partially_applied",
	}
	scenarios := make([]Scenario, 0, len(scenarioIDs))
	for _, id := range scenarioIDs {
		for _, item := range fx.Scenarios {
			if item.ID == id {
				scenarios = append(scenarios, item)
				break
			}
		}
	}
	if len(scenarios) != len(scenarioIDs) {
		b.Fatalf("resolved %d scenarios, want %d", len(scenarios), len(scenarioIDs))
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, scenario := range scenarios {
			if _, err := RunParmesan(ctx, scenario); err != nil {
				b.Fatalf("RunParmesan(%q) error = %v", scenario.ID, err)
			}
		}
	}
}

func BenchmarkGoldenSnapshotSerialization(b *testing.B) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		b.Fatalf("LoadFixture() error = %v", err)
	}
	snapshot, err := RunGoldenScenario(context.Background(), fx, "tool_from_entailed_guideline")
	if err != nil {
		b.Fatalf("RunGoldenScenario() error = %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(canonicalizeNormalizedResult(snapshot.Result)); err != nil {
			b.Fatalf("canonicalJSON() error = %v", err)
		}
	}
}
