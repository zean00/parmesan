package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sahal/parmesan/internal/quality"
)

func main() {
	inPath := flag.String("in", envOrDefault("QUALITY_SCENARIO_SEEDS", "artifacts/regression-scenario-seeds.json"), "scenario seed JSON to validate")
	flag.Parse()

	raw, err := os.ReadFile(*inPath)
	if err != nil {
		fatalf("read seed file: %v", err)
	}
	var seeds []quality.ScenarioExpectation
	if err := json.Unmarshal(raw, &seeds); err != nil {
		fatalf("decode seed file: %v", err)
	}
	if errs := quality.ValidateScenarioSeeds(seeds); len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	fmt.Printf("validated %d scenario seeds from %s\n", len(seeds), *inPath)
}

func envOrDefault(key, fallback string) string {
	if got := strings.TrimSpace(os.Getenv(key)); got != "" {
		return got
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
