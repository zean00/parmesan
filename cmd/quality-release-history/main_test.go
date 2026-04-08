package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildHistorySummaryCountsConsecutiveCleanRuns(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, generatedAt time.Time, passed bool, minOverall float64) {
		t.Helper()
		raw, err := json.Marshal(snapshot{
			GeneratedAt: generatedAt,
			Passed:      passed,
			Summary: struct {
				ReportCount         int       `json:"report_count"`
				ScorecardCount      int       `json:"scorecard_count"`
				HardFailedScorecard int       `json:"hard_failed_scorecards"`
				MinOverall          float64   `json:"min_overall"`
				LatestReportAt      time.Time `json:"latest_report_at"`
			}{
				ReportCount:         4,
				ScorecardCount:      12,
				HardFailedScorecard: boolToInt(!passed),
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

	write("20260408T010000Z.json", time.Date(2026, 4, 8, 1, 0, 0, 0, time.UTC), true, 0.82)
	write("20260408T020000Z.json", time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC), true, 0.84)
	write("20260408T030000Z.json", time.Date(2026, 4, 8, 3, 0, 0, 0, time.UTC), false, 0.65)

	summary, err := buildHistorySummary(dir, 2)
	if err != nil {
		t.Fatalf("buildHistorySummary() error = %v", err)
	}
	if summary.HistoryCount != 3 {
		t.Fatalf("HistoryCount = %d, want 3", summary.HistoryCount)
	}
	if summary.ConsecutiveCleanRuns != 0 {
		t.Fatalf("ConsecutiveCleanRuns = %d, want 0 because latest failed", summary.ConsecutiveCleanRuns)
	}
	if summary.MeetsConsecutiveTarget {
		t.Fatalf("MeetsConsecutiveTarget = %t, want false", summary.MeetsConsecutiveTarget)
	}
}

func TestBuildHistorySummaryLatestPassingSequence(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, generatedAt time.Time, passed bool) {
		t.Helper()
		raw, err := json.Marshal(snapshot{
			GeneratedAt: generatedAt,
			Passed:      passed,
			Summary: struct {
				ReportCount         int       `json:"report_count"`
				ScorecardCount      int       `json:"scorecard_count"`
				HardFailedScorecard int       `json:"hard_failed_scorecards"`
				MinOverall          float64   `json:"min_overall"`
				LatestReportAt      time.Time `json:"latest_report_at"`
			}{
				ReportCount:         4,
				ScorecardCount:      12,
				HardFailedScorecard: boolToInt(!passed),
				MinOverall:          0.85,
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

	write("20260408T010000Z.json", time.Date(2026, 4, 8, 1, 0, 0, 0, time.UTC), false)
	write("20260408T020000Z.json", time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC), true)
	write("20260408T030000Z.json", time.Date(2026, 4, 8, 3, 0, 0, 0, time.UTC), true)

	summary, err := buildHistorySummary(dir, 2)
	if err != nil {
		t.Fatalf("buildHistorySummary() error = %v", err)
	}
	if summary.ConsecutiveCleanRuns != 2 {
		t.Fatalf("ConsecutiveCleanRuns = %d, want 2", summary.ConsecutiveCleanRuns)
	}
	if !summary.MeetsConsecutiveTarget {
		t.Fatalf("MeetsConsecutiveTarget = %t, want true", summary.MeetsConsecutiveTarget)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
