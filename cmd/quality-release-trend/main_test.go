package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildTrendSummaryCalculatesDelta(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, generatedAt time.Time, passed bool, minOverall float64, hardFailed int, providers []providerStat) {
		t.Helper()
		raw, err := json.Marshal(snapshot{
			GeneratedAt: generatedAt,
			Passed:      passed,
			MergedLive:  []string{"scenario_a", "scenario_b"},
			Providers:   providers,
			Summary: snapshotMetric{
				ReportCount:         4,
				ScenarioCount:       10,
				ScorecardCount:      12,
				PassedScorecards:    12 - hardFailed,
				HardFailedScorecard: hardFailed,
				MinOverall:          minOverall,
				LatestReportAt:      generatedAt,
			},
		})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	write("20260408T010000Z.json", time.Date(2026, 4, 8, 1, 0, 0, 0, time.UTC), false, 0.70, 2, []providerStat{{Name: "openrouter", Capability: "reasoning", Healthy: false}})
	write("20260408T020000Z.json", time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC), true, 0.84, 0, []providerStat{{Name: "openrouter", Capability: "reasoning", Healthy: true}})

	summary, err := buildTrendSummary(dir)
	if err != nil {
		t.Fatalf("buildTrendSummary() error = %v", err)
	}
	if summary.HistoryCount != 2 {
		t.Fatalf("HistoryCount = %d, want 2", summary.HistoryCount)
	}
	if summary.Delta == nil {
		t.Fatalf("Delta = nil, want computed delta")
	}
	if summary.Delta.Passed != "improved" {
		t.Fatalf("Passed delta = %q, want improved", summary.Delta.Passed)
	}
	if summary.Delta.MinOverallDelta <= 0 {
		t.Fatalf("MinOverallDelta = %.2f, want positive", summary.Delta.MinOverallDelta)
	}
	if len(summary.Delta.AddedHealthyProviders) != 1 || summary.Delta.AddedHealthyProviders[0] != "openrouter:reasoning" {
		t.Fatalf("AddedHealthyProviders = %#v, want openrouter:reasoning", summary.Delta.AddedHealthyProviders)
	}
}
