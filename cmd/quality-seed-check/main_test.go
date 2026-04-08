package main

import (
	"testing"

	"github.com/sahal/parmesan/internal/quality"
)

func TestValidateScenarioSeeds(t *testing.T) {
	errs := quality.ValidateScenarioSeeds([]quality.ScenarioExpectation{{
		ID:              "seed_ok",
		Domain:          "support",
		Category:        "failure_modes",
		Input:           "seeded case",
		ExpectedQuality: []string{"policy_adherence"},
		MinimumOverall:  0.7,
	}})
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
}

func TestValidateScenarioSeedsRejectsUnknownDimensionAndDuplicateID(t *testing.T) {
	errs := quality.ValidateScenarioSeeds([]quality.ScenarioExpectation{
		{ID: "dup", Input: "one", ExpectedQuality: []string{"unknown_dimension"}, MinimumOverall: 0.7},
		{ID: "dup", Input: "two", ExpectedQuality: []string{"policy_adherence"}, MinimumOverall: 0.7},
	})
	if len(errs) < 2 {
		t.Fatalf("errs = %v, want duplicate-id and unknown-dimension failures", errs)
	}
}
