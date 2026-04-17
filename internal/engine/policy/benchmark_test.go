package policyruntime_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sahal/parmesan/internal/parity"
)

func BenchmarkResolveGoldenScenarios(b *testing.B) {
	fixturePath := filepath.Join("..", "..", "..", "examples", "golden_scenarios.yaml")
	fx, err := parity.LoadFixture(fixturePath)
	if err != nil {
		b.Fatalf("LoadFixture() error = %v", err)
	}

	scenarioIDs := []string{
		"journey_custom_backtrack_and_fast_forward",
		"tool_schedule_confirmation_runs_in_tandem",
		"response_analysis_discount_partially_applied",
	}
	scenarios := make([]parity.Scenario, 0, len(scenarioIDs))
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
			if _, err := parity.RunParmesan(ctx, scenario); err != nil {
				b.Fatalf("RunParmesan(%q) error = %v", scenario.ID, err)
			}
		}
	}
}
