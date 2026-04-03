package parity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func TestLoadFixtureParsesGoldenScenarios(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fixture, err := LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}
	if fixture.Version == "" {
		t.Fatalf("fixture version is empty")
	}
	if len(fixture.Scenarios) == 0 {
		t.Fatalf("fixture scenarios are empty")
	}
}

func TestEvaluateScenarioDetectsExpectationAndParityFailures(t *testing.T) {
	scenario := Scenario{
		ID: "example",
		Expect: Expectations{
			MatchedGuidelines: []string{"greet"},
			NoMatch:           boolPtr(false),
		},
	}
	left := NormalizedResult{
		MatchedGuidelines: []string{"greet"},
		NoMatch:           false,
	}
	right := NormalizedResult{
		MatchedGuidelines: []string{"other"},
		NoMatch:           true,
	}
	report := EvaluateScenario(scenario, left, right)
	if report.Passed {
		t.Fatalf("EvaluateScenario() passed, want failure")
	}
	if len(report.ExpectationErrors) == 0 {
		t.Fatalf("ExpectationErrors = 0, want > 0")
	}
	if len(report.DiffErrors) == 0 {
		t.Fatalf("DiffErrors = 0, want > 0")
	}
}

func TestEvaluateScenarioCanIgnoreParlantDrift(t *testing.T) {
	scenario := Scenario{
		ID:                "example",
		SkipEngineDiff:    true,
		SkipParlantExpect: true,
		Expect: Expectations{
			MatchedGuidelines: []string{"greet"},
		},
	}
	left := NormalizedResult{
		MatchedGuidelines: []string{"greet"},
	}
	right := NormalizedResult{
		MatchedGuidelines: []string{"other"},
	}
	report := EvaluateScenario(scenario, left, right)
	if !report.Passed {
		t.Fatalf("EvaluateScenario() failed, want pass when live Parlant drift is ignored: %#v", report)
	}
	if len(report.DiffErrors) != 0 {
		t.Fatalf("DiffErrors = %#v, want 0", report.DiffErrors)
	}
}

func TestIdsFromSuppressedIgnoresProjectedJourneyNodes(t *testing.T) {
	got := idsFromSuppressed([]policyruntime.SuppressedGuideline{
		{ID: "journey_node:Book Flight:ask_origin", Reason: "condition_conflict"},
		{ID: "age_21_or_older", Reason: "condition_conflict"},
	})
	if len(got) != 1 || got[0] != "age_21_or_older" {
		t.Fatalf("idsFromSuppressed() = %#v, want only age_21_or_older", got)
	}
}

func TestRunUsesScenarioTimeout(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	_, err := Run(ctx, Options{
		FixturePath:     filepath.Join("..", "..", "examples", "golden_scenarios.yaml"),
		ScenarioID:      "journey_reset_password_start",
		ParlantRoot:     "/definitely/missing/parlant",
		ScenarioTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Run() took too long, scenario timeout may not be applied")
	}
}

func boolPtr(v bool) *bool {
	return &v
}
