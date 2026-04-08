package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sahal/parmesan/internal/quality"
)

type exportEnvelope struct {
	Items []fixtureExport `json:"items"`
}

type fixtureExport struct {
	ID                string   `json:"id"`
	Input             string   `json:"input"`
	ExpectedQuality   []string `json:"expected_quality"`
	Risk              string   `json:"risk"`
	ExpectedBehavior  string   `json:"expected_behavior"`
	QualityDimensions []string `json:"quality_dimensions"`
	ReviewStatus      string   `json:"review_status"`
}

func main() {
	inPath := flag.String("in", envOrDefault("REGRESSION_SEED_IN", "artifacts/regression-fixtures.json"), "accepted regression fixture export JSON")
	outPath := flag.String("out", envOrDefault("REGRESSION_SEED_OUT", "artifacts/regression-scenario-seeds.json"), "scenario seed output JSON")
	promoteLive := flag.String("promote-live", envOrDefault("REGRESSION_SEED_PROMOTE_LIVE_IDS", ""), "comma-separated scenario ids to mark live_gate=true")
	flag.Parse()
	liveIDs := csvSet(*promoteLive)

	raw, err := os.ReadFile(*inPath)
	if err != nil {
		fatalf("read input: %v", err)
	}
	var payload exportEnvelope
	if err := json.Unmarshal(raw, &payload); err != nil {
		fatalf("decode input: %v", err)
	}
	var seeds []quality.ScenarioExpectation
	for _, item := range payload.Items {
		if strings.TrimSpace(item.ReviewStatus) != "accepted" {
			continue
		}
		seeds = append(seeds, quality.ScenarioExpectation{
			ID:              strings.TrimSpace(item.ID),
			Domain:          inferDomain(item.ID),
			Category:        inferCategory(item.QualityDimensions, item.ExpectedQuality),
			Input:           strings.TrimSpace(item.Input),
			ExpectedQuality: nonEmpty(item.ExpectedQuality, item.QualityDimensions),
			Risk:            fallback(item.Risk, "high"),
			RequiredClaims:  nonEmpty([]string{strings.TrimSpace(item.ExpectedBehavior)}, nil),
			MinimumOverall:  minimumOverallForRisk(item.Risk),
			LiveGate:        liveIDs[strings.TrimSpace(item.ID)],
		})
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	f, err := os.Create(*outPath)
	if err != nil {
		fatalf("create output: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(seeds); err != nil {
		fatalf("write output: %v", err)
	}
	fmt.Printf("wrote %d regression scenario seeds to %s\n", len(seeds), *outPath)
}

func inferDomain(id string) string {
	switch {
	case strings.Contains(id, "pet"):
		return "pet_store"
	case strings.Contains(id, "ecommerce"), strings.Contains(id, "refund"), strings.Contains(id, "replacement"):
		return "ecommerce"
	default:
		return "support"
	}
}

func inferCategory(dimensions, expected []string) string {
	items := nonEmpty(expected, dimensions)
	for _, item := range items {
		switch strings.TrimSpace(item) {
		case "topic_scope_compliance":
			return "topic_scope"
		case "multilingual_quality":
			return "multilingual"
		case "customer_preference":
			return "preference"
		case "refusal_escalation_quality":
			return "refusal_escalation"
		case "knowledge_grounding", "retrieval_quality":
			return "knowledge_grounding"
		case "tone_persona":
			return "soul_persona"
		}
	}
	return "failure_modes"
}

func nonEmpty(primary, secondary []string) []string {
	var out []string
	source := primary
	if len(source) == 0 {
		source = secondary
	}
	for _, item := range source {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func fallback(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func minimumOverallForRisk(risk string) float64 {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "medium":
		return 0.72
	default:
		return 0.7
	}
}

func envOrDefault(key, fallback string) string {
	if got := strings.TrimSpace(os.Getenv(key)); got != "" {
		return got
	}
	return fallback
}

func csvSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = true
		}
	}
	return out
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
