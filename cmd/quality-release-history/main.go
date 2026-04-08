package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Passed      bool      `json:"passed"`
	Summary     struct {
		ReportCount         int       `json:"report_count"`
		ScorecardCount      int       `json:"scorecard_count"`
		HardFailedScorecard int       `json:"hard_failed_scorecards"`
		MinOverall          float64   `json:"min_overall"`
		LatestReportAt      time.Time `json:"latest_report_at"`
	} `json:"summary"`
}

type historySummary struct {
	Directory              string         `json:"directory"`
	RequiredConsecutive    int            `json:"required_consecutive"`
	HistoryCount           int            `json:"history_count"`
	LatestPassed           bool           `json:"latest_passed"`
	ConsecutiveCleanRuns   int            `json:"consecutive_clean_runs"`
	MeetsConsecutiveTarget bool           `json:"meets_consecutive_target"`
	LatestMinOverall       float64        `json:"latest_min_overall"`
	Recent                 []historyEntry `json:"recent"`
}

type historyEntry struct {
	Path         string    `json:"path"`
	GeneratedAt  time.Time `json:"generated_at"`
	Passed       bool      `json:"passed"`
	MinOverall   float64   `json:"min_overall"`
	Scorecards   int       `json:"scorecard_count"`
	HardFailures int       `json:"hard_failed_scorecards"`
}

func main() {
	var dir string
	var requireConsecutive int
	flag.StringVar(&dir, "dir", "artifacts/quality-release-history", "directory containing archived quality release snapshots")
	flag.IntVar(&requireConsecutive, "require-consecutive", 1, "required number of consecutive clean runs")
	flag.Parse()

	summary, err := buildHistorySummary(dir, requireConsecutive)
	if err != nil {
		log.Fatal(err)
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(raw))
	if !summary.MeetsConsecutiveTarget {
		log.Fatalf("quality release history only has %d consecutive clean runs, need %d", summary.ConsecutiveCleanRuns, summary.RequiredConsecutive)
	}
}

func buildHistorySummary(dir string, requireConsecutive int) (historySummary, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return historySummary{}, err
	}
	if len(paths) == 0 {
		return historySummary{}, fmt.Errorf("no release snapshots found in %s", dir)
	}
	var entries []historyEntry
	for _, path := range paths {
		item, err := readSnapshot(path)
		if err != nil {
			return historySummary{}, err
		}
		entries = append(entries, historyEntry{
			Path:         path,
			GeneratedAt:  item.GeneratedAt,
			Passed:       item.Passed,
			MinOverall:   item.Summary.MinOverall,
			Scorecards:   item.Summary.ScorecardCount,
			HardFailures: item.Summary.HardFailedScorecard,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].GeneratedAt.Equal(entries[j].GeneratedAt) {
			return entries[i].Path > entries[j].Path
		}
		return entries[i].GeneratedAt.After(entries[j].GeneratedAt)
	})
	consecutive := 0
	for _, entry := range entries {
		if !entry.Passed {
			break
		}
		consecutive++
	}
	summary := historySummary{
		Directory:              dir,
		RequiredConsecutive:    max(requireConsecutive, 1),
		HistoryCount:           len(entries),
		LatestPassed:           entries[0].Passed,
		ConsecutiveCleanRuns:   consecutive,
		MeetsConsecutiveTarget: consecutive >= max(requireConsecutive, 1),
		LatestMinOverall:       entries[0].MinOverall,
		Recent:                 entries,
	}
	return summary, nil
}

func readSnapshot(path string) (snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	var item snapshot
	if err := json.Unmarshal(raw, &item); err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	return item, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
