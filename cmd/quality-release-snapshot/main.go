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

	"github.com/sahal/parmesan/internal/quality"
)

type report struct {
	TestName      string          `json:"test_name"`
	Scenario      string          `json:"scenario"`
	GeneratedAt   time.Time       `json:"generated_at"`
	LiveProvider  bool            `json:"live_provider"`
	ProviderStats []providerStats `json:"provider_stats,omitempty"`
	Sessions      []reportSession `json:"sessions"`
}

type providerStats struct {
	Name         string `json:"name"`
	Capability   string `json:"capability"`
	Healthy      bool   `json:"healthy"`
	SuccessCount int    `json:"success_count"`
	FailureCount int    `json:"failure_count"`
}

type reportSession struct {
	ID         string                     `json:"id"`
	Scorecards map[string]reportScorecard `json:"scorecards"`
}

type reportScorecard struct {
	Overall    float64 `json:"overall"`
	Passed     bool    `json:"passed"`
	HardFailed bool    `json:"hard_failed"`
}

type snapshot struct {
	GeneratedAt time.Time             `json:"generated_at"`
	ReportDir   string                `json:"report_dir"`
	BuiltInLive []string              `json:"built_in_live"`
	MergedLive  []string              `json:"merged_live"`
	AddedLive   []string              `json:"added_live"`
	RemovedLive []string              `json:"removed_live"`
	Summary     snapshotSummary       `json:"summary"`
	Providers   []providerStats       `json:"providers"`
	Reports     []snapshotReportBrief `json:"reports"`
}

type snapshotSummary struct {
	ReportCount         int       `json:"report_count"`
	ScenarioCount       int       `json:"scenario_count"`
	ScorecardCount      int       `json:"scorecard_count"`
	PassedScorecards    int       `json:"passed_scorecards"`
	HardFailedScorecard int       `json:"hard_failed_scorecards"`
	MinOverall          float64   `json:"min_overall"`
	Scenarios           []string  `json:"scenarios"`
	LatestReportAt      time.Time `json:"latest_report_at,omitempty"`
}

type snapshotReportBrief struct {
	TestName        string    `json:"test_name"`
	Scenario        string    `json:"scenario"`
	GeneratedAt     time.Time `json:"generated_at"`
	LiveProvider    bool      `json:"live_provider"`
	ScorecardCount  int       `json:"scorecard_count"`
	MinOverall      float64   `json:"min_overall"`
	HardFailed      bool      `json:"hard_failed"`
	ProviderTouched []string  `json:"provider_touched,omitempty"`
}

func main() {
	var dir string
	var out string
	flag.StringVar(&dir, "dir", "/tmp/parmesan-platform-validation-live", "platform-validation report directory")
	flag.StringVar(&out, "out", "artifacts/quality-release-snapshot.json", "output JSON path")
	flag.Parse()

	item, err := buildSnapshot(dir)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatal(err)
	}
	raw, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote quality release snapshot to %s\n", out)
}

func buildSnapshot(dir string) (snapshot, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "TestPlatformValidation*.json"))
	if err != nil {
		return snapshot{}, err
	}
	if len(paths) == 0 {
		return snapshot{}, fmt.Errorf("no platform-validation reports found in %s", dir)
	}

	builtInLive := liveScenarioIDs(quality.BuiltInProductionReadinessScenarios())
	mergedLive := liveScenarioIDs(quality.ProductionReadinessScenarios())
	out := snapshot{
		GeneratedAt: time.Now().UTC(),
		ReportDir:   dir,
		BuiltInLive: builtInLive,
		MergedLive:  mergedLive,
		AddedLive:   diffIDs(mergedLive, builtInLive),
		RemovedLive: diffIDs(builtInLive, mergedLive),
		Summary: snapshotSummary{
			MinOverall: 1,
		},
	}

	providers := map[string]providerStats{}
	scenarios := map[string]struct{}{}
	for _, path := range paths {
		item, err := readReport(path)
		if err != nil {
			return snapshot{}, err
		}
		if item.GeneratedAt.After(out.Summary.LatestReportAt) {
			out.Summary.LatestReportAt = item.GeneratedAt
		}
		reportBrief := snapshotReportBrief{
			TestName:     item.TestName,
			Scenario:     item.Scenario,
			GeneratedAt:  item.GeneratedAt,
			LiveProvider: item.LiveProvider,
			MinOverall:   1,
		}
		for _, stat := range item.ProviderStats {
			key := providerKey(stat.Name, stat.Capability)
			current := providers[key]
			if current.Name == "" {
				current = stat
			} else {
				current.SuccessCount += stat.SuccessCount
				current.FailureCount += stat.FailureCount
				current.Healthy = current.Healthy && stat.Healthy
			}
			providers[key] = current
			reportBrief.ProviderTouched = append(reportBrief.ProviderTouched, stat.Name)
		}
		sort.Strings(reportBrief.ProviderTouched)
		reportBrief.ProviderTouched = compactStrings(reportBrief.ProviderTouched)
		for _, session := range item.Sessions {
			for _, scorecard := range session.Scorecards {
				out.Summary.ScorecardCount++
				reportBrief.ScorecardCount++
				if scorecard.Passed {
					out.Summary.PassedScorecards++
				}
				if scorecard.HardFailed {
					out.Summary.HardFailedScorecard++
					reportBrief.HardFailed = true
				}
				if scorecard.Overall < out.Summary.MinOverall {
					out.Summary.MinOverall = scorecard.Overall
				}
				if scorecard.Overall < reportBrief.MinOverall {
					reportBrief.MinOverall = scorecard.Overall
				}
			}
		}
		if reportBrief.ScorecardCount == 0 {
			reportBrief.MinOverall = 0
		}
		out.Reports = append(out.Reports, reportBrief)
		if item.Scenario != "" {
			scenarios[item.Scenario] = struct{}{}
		}
	}
	if out.Summary.ScorecardCount == 0 {
		out.Summary.MinOverall = 0
	}
	out.Summary.ReportCount = len(out.Reports)
	out.Summary.Scenarios = mapKeysSorted(scenarios)
	out.Summary.ScenarioCount = len(out.Summary.Scenarios)
	out.Providers = providerValues(providers)
	sort.Slice(out.Reports, func(i, j int) bool {
		if out.Reports[i].Scenario == out.Reports[j].Scenario {
			return out.Reports[i].TestName < out.Reports[j].TestName
		}
		return out.Reports[i].Scenario < out.Reports[j].Scenario
	})
	return out, nil
}

func readReport(path string) (report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return report{}, fmt.Errorf("%s: %w", path, err)
	}
	var item report
	if err := json.Unmarshal(raw, &item); err != nil {
		return report{}, fmt.Errorf("%s: %w", path, err)
	}
	return item, nil
}

func liveScenarioIDs(items []quality.ScenarioExpectation) []string {
	var out []string
	for _, item := range items {
		if item.LiveGate {
			out = append(out, item.ID)
		}
	}
	sort.Strings(out)
	return out
}

func diffIDs(left, right []string) []string {
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

func providerKey(name, capability string) string {
	return name + "|" + capability
}

func providerValues(items map[string]providerStats) []providerStats {
	out := make([]providerStats, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Capability < out[j].Capability
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func mapKeysSorted(items map[string]struct{}) []string {
	out := make([]string, 0, len(items))
	for item := range items {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func compactStrings(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
