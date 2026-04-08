package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildSnapshotAggregatesReports(t *testing.T) {
	dir := t.TempDir()
	writeReport := func(name string, item report) {
		t.Helper()
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	writeReport("TestPlatformValidationOne.json", report{
		TestName:    "TestPlatformValidationOne",
		Scenario:    "scenario_one",
		GeneratedAt: time.Date(2026, 4, 8, 1, 0, 0, 0, time.UTC),
		ProviderStats: []providerStats{{
			Name:         "openrouter",
			Capability:   "reasoning",
			Healthy:      true,
			SuccessCount: 2,
		}},
		Sessions: []reportSession{{
			ID: "sess_1",
			Scorecards: map[string]reportScorecard{
				"exec_1": {Overall: 0.9, Passed: true},
			},
		}},
	})
	writeReport("TestPlatformValidationTwo.json", report{
		TestName:    "TestPlatformValidationTwo",
		Scenario:    "scenario_two",
		GeneratedAt: time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC),
		ProviderStats: []providerStats{{
			Name:         "openrouter",
			Capability:   "reasoning",
			Healthy:      true,
			SuccessCount: 3,
			FailureCount: 1,
		}},
		Sessions: []reportSession{{
			ID: "sess_2",
			Scorecards: map[string]reportScorecard{
				"exec_2": {Overall: 0.8, Passed: true},
				"exec_3": {Overall: 0.7, Passed: true, HardFailed: true},
			},
		}},
	})

	item, err := buildSnapshot(dir)
	if err != nil {
		t.Fatalf("buildSnapshot() error = %v", err)
	}
	if item.Summary.ReportCount != 2 {
		t.Fatalf("ReportCount = %d, want 2", item.Summary.ReportCount)
	}
	if item.Summary.ScenarioCount != 2 {
		t.Fatalf("ScenarioCount = %d, want 2", item.Summary.ScenarioCount)
	}
	if item.Summary.ScorecardCount != 3 {
		t.Fatalf("ScorecardCount = %d, want 3", item.Summary.ScorecardCount)
	}
	if item.Summary.PassedScorecards != 3 {
		t.Fatalf("PassedScorecards = %d, want 3", item.Summary.PassedScorecards)
	}
	if item.Summary.HardFailedScorecard != 1 {
		t.Fatalf("HardFailedScorecard = %d, want 1", item.Summary.HardFailedScorecard)
	}
	if item.Summary.MinOverall != 0.7 {
		t.Fatalf("MinOverall = %.2f, want 0.7", item.Summary.MinOverall)
	}
	if len(item.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(item.Providers))
	}
	if got := item.Providers[0].SuccessCount; got != 5 {
		t.Fatalf("provider success_count = %d, want 5", got)
	}
	if got := item.Providers[0].FailureCount; got != 1 {
		t.Fatalf("provider failure_count = %d, want 1", got)
	}
	if got := item.Summary.Scenarios; len(got) != 2 || got[0] != "scenario_one" || got[1] != "scenario_two" {
		t.Fatalf("Scenarios = %#v, want sorted scenarios", got)
	}
}
