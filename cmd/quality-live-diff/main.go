package main

import (
	"encoding/json"
	"log"
	"os"
	"sort"

	"github.com/sahal/parmesan/internal/quality"
)

type diffReport struct {
	BuiltInLive []string `json:"built_in_live"`
	MergedLive  []string `json:"merged_live"`
	AddedLive   []string `json:"added_live"`
	RemovedLive []string `json:"removed_live"`
}

func main() {
	builtIn := liveScenarioIDs(quality.BuiltInProductionReadinessScenarios())
	merged := liveScenarioIDs(quality.ProductionReadinessScenarios())
	report := diffReport{
		BuiltInLive: builtIn,
		MergedLive:  merged,
		AddedLive:   diffIDs(merged, builtIn),
		RemovedLive: diffIDs(builtIn, merged),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		log.Fatal(err)
	}
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
