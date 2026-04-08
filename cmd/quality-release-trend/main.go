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
	GeneratedAt time.Time      `json:"generated_at"`
	Passed      bool           `json:"passed"`
	MergedLive  []string       `json:"merged_live"`
	Providers   []providerStat `json:"providers"`
	Summary     snapshotMetric `json:"summary"`
}

type snapshotMetric struct {
	ReportCount         int       `json:"report_count"`
	ScenarioCount       int       `json:"scenario_count"`
	ScorecardCount      int       `json:"scorecard_count"`
	PassedScorecards    int       `json:"passed_scorecards"`
	HardFailedScorecard int       `json:"hard_failed_scorecards"`
	MinOverall          float64   `json:"min_overall"`
	LatestReportAt      time.Time `json:"latest_report_at"`
}

type providerStat struct {
	Name         string `json:"name"`
	Capability   string `json:"capability"`
	Healthy      bool   `json:"healthy"`
	SuccessCount int    `json:"success_count"`
	FailureCount int    `json:"failure_count"`
}

type trendSummary struct {
	Directory    string              `json:"directory"`
	HistoryCount int                 `json:"history_count"`
	Latest       trendSnapshotBrief  `json:"latest"`
	Previous     *trendSnapshotBrief `json:"previous,omitempty"`
	Delta        *trendDelta         `json:"delta,omitempty"`
}

type trendSnapshotBrief struct {
	Path               string    `json:"path"`
	GeneratedAt        time.Time `json:"generated_at"`
	Passed             bool      `json:"passed"`
	MinOverall         float64   `json:"min_overall"`
	ScorecardCount     int       `json:"scorecard_count"`
	HardFailedCount    int       `json:"hard_failed_scorecards"`
	MergedLiveCount    int       `json:"merged_live_count"`
	HealthyProviders   []string  `json:"healthy_providers,omitempty"`
	UnhealthyProviders []string  `json:"unhealthy_providers,omitempty"`
}

type trendDelta struct {
	PassedChanged             bool     `json:"passed_changed"`
	Passed                    string   `json:"passed"`
	MinOverallDelta           float64  `json:"min_overall_delta"`
	ScorecardCountDelta       int      `json:"scorecard_count_delta"`
	HardFailedCountDelta      int      `json:"hard_failed_scorecards_delta"`
	MergedLiveCountDelta      int      `json:"merged_live_count_delta"`
	AddedHealthyProviders     []string `json:"added_healthy_providers,omitempty"`
	RemovedHealthyProviders   []string `json:"removed_healthy_providers,omitempty"`
	AddedUnhealthyProviders   []string `json:"added_unhealthy_providers,omitempty"`
	RemovedUnhealthyProviders []string `json:"removed_unhealthy_providers,omitempty"`
}

type snapshotFile struct {
	Path  string
	Value snapshot
}

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "artifacts/quality-release-history", "directory containing archived quality release snapshots")
	flag.Parse()

	summary, err := buildTrendSummary(dir)
	if err != nil {
		log.Fatal(err)
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(raw))
}

func buildTrendSummary(dir string) (trendSummary, error) {
	files, err := loadSnapshots(dir)
	if err != nil {
		return trendSummary{}, err
	}
	if len(files) == 0 {
		return trendSummary{}, fmt.Errorf("no release snapshots found in %s", dir)
	}
	summary := trendSummary{
		Directory:    dir,
		HistoryCount: len(files),
		Latest:       snapshotBrief(files[0]),
	}
	if len(files) > 1 {
		prev := snapshotBrief(files[1])
		summary.Previous = &prev
		summary.Delta = &trendDelta{
			PassedChanged:             files[0].Value.Passed != files[1].Value.Passed,
			Passed:                    passDelta(files[0].Value.Passed, files[1].Value.Passed),
			MinOverallDelta:           files[0].Value.Summary.MinOverall - files[1].Value.Summary.MinOverall,
			ScorecardCountDelta:       files[0].Value.Summary.ScorecardCount - files[1].Value.Summary.ScorecardCount,
			HardFailedCountDelta:      files[0].Value.Summary.HardFailedScorecard - files[1].Value.Summary.HardFailedScorecard,
			MergedLiveCountDelta:      len(files[0].Value.MergedLive) - len(files[1].Value.MergedLive),
			AddedHealthyProviders:     diffStrings(healthyProviderKeys(files[0].Value.Providers), healthyProviderKeys(files[1].Value.Providers)),
			RemovedHealthyProviders:   diffStrings(healthyProviderKeys(files[1].Value.Providers), healthyProviderKeys(files[0].Value.Providers)),
			AddedUnhealthyProviders:   diffStrings(unhealthyProviderKeys(files[0].Value.Providers), unhealthyProviderKeys(files[1].Value.Providers)),
			RemovedUnhealthyProviders: diffStrings(unhealthyProviderKeys(files[1].Value.Providers), unhealthyProviderKeys(files[0].Value.Providers)),
		}
	}
	return summary, nil
}

func loadSnapshots(dir string) ([]snapshotFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var files []snapshotFile
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var item snapshot
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		files = append(files, snapshotFile{Path: path, Value: item})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Value.GeneratedAt.Equal(files[j].Value.GeneratedAt) {
			return files[i].Path > files[j].Path
		}
		return files[i].Value.GeneratedAt.After(files[j].Value.GeneratedAt)
	})
	return files, nil
}

func snapshotBrief(file snapshotFile) trendSnapshotBrief {
	return trendSnapshotBrief{
		Path:               file.Path,
		GeneratedAt:        file.Value.GeneratedAt,
		Passed:             file.Value.Passed,
		MinOverall:         file.Value.Summary.MinOverall,
		ScorecardCount:     file.Value.Summary.ScorecardCount,
		HardFailedCount:    file.Value.Summary.HardFailedScorecard,
		MergedLiveCount:    len(file.Value.MergedLive),
		HealthyProviders:   healthyProviderKeys(file.Value.Providers),
		UnhealthyProviders: unhealthyProviderKeys(file.Value.Providers),
	}
}

func healthyProviderKeys(items []providerStat) []string {
	var out []string
	for _, item := range items {
		if item.Healthy {
			out = append(out, providerKey(item))
		}
	}
	sort.Strings(out)
	return out
}

func unhealthyProviderKeys(items []providerStat) []string {
	var out []string
	for _, item := range items {
		if !item.Healthy {
			out = append(out, providerKey(item))
		}
	}
	sort.Strings(out)
	return out
}

func providerKey(item providerStat) string {
	if item.Capability == "" {
		return item.Name
	}
	return item.Name + ":" + item.Capability
}

func diffStrings(left, right []string) []string {
	seen := map[string]struct{}{}
	for _, item := range right {
		seen[item] = struct{}{}
	}
	var out []string
	for _, item := range left {
		if _, ok := seen[item]; !ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func passDelta(latest, previous bool) string {
	switch {
	case latest == previous && latest:
		return "stable_pass"
	case latest == previous && !latest:
		return "stable_fail"
	case latest && !previous:
		return "improved"
	default:
		return "regressed"
	}
}
