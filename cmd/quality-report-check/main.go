package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sahal/parmesan/internal/quality"
)

type report struct {
	TestName string          `json:"test_name"`
	Scenario string          `json:"scenario"`
	Sessions []reportSession `json:"sessions"`
}

type reportSession struct {
	ID         string                     `json:"id"`
	Scorecards map[string]reportScorecard `json:"scorecards"`
}

type reportScorecard struct {
	Overall      float64          `json:"overall"`
	Passed       bool             `json:"passed"`
	HardFailed   bool             `json:"hard_failed"`
	HardFailures []map[string]any `json:"hard_failures"`
}

func main() {
	var dir string
	var expectedTests string
	var expectedScenarios string
	var minOverall float64
	flag.StringVar(&dir, "dir", "/tmp/parmesan-platform-validation-live", "platform-validation report directory")
	flag.StringVar(&expectedTests, "expect-tests", "", "comma-separated list of expected test names")
	flag.StringVar(&expectedScenarios, "expect-scenarios", "", "comma-separated list of expected scenario ids")
	flag.Float64Var(&minOverall, "min-overall", 0.7, "minimum allowed response-quality overall score")
	flag.Parse()

	checked, err := checkReports(dir, reportExpectations{
		TestNames:  splitCSV(expectedTests),
		Scenarios:  splitCSV(expectedScenarios),
		MinOverall: minOverall,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("checked %d platform-validation scorecards in %s\n", checked, dir)
}

type reportExpectations struct {
	TestNames  []string
	Scenarios  []string
	MinOverall float64
}

func checkReports(dir string, expectations reportExpectations) (int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "TestPlatformValidation*.json"))
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, fmt.Errorf("no platform-validation reports found in %s", dir)
	}
	checked := 0
	var failures []error
	seenTests := map[string]struct{}{}
	seenScenarios := map[string]struct{}{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			continue
		}
		var item report
		if err := json.Unmarshal(raw, &item); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			continue
		}
		seenTests[item.TestName] = struct{}{}
		seenScenarios[item.Scenario] = struct{}{}
		scenarioThreshold := expectations.MinOverall
		if scenario, ok := quality.FindScenarioByID(item.Scenario); ok && scenario.MinimumOverall > scenarioThreshold {
			scenarioThreshold = scenario.MinimumOverall
		}
		for _, session := range item.Sessions {
			for executionID, scorecard := range session.Scorecards {
				checked++
				if scorecard.HardFailed || !scorecard.Passed {
					failures = append(failures, fmt.Errorf("%s session=%s execution=%s failed quality gate hard_failed=%t passed=%t failures=%v", item.TestName, session.ID, executionID, scorecard.HardFailed, scorecard.Passed, scorecard.HardFailures))
				}
				if scorecard.Overall < scenarioThreshold {
					failures = append(failures, fmt.Errorf("%s session=%s execution=%s overall score %.2f below minimum %.2f", item.TestName, session.ID, executionID, scorecard.Overall, scenarioThreshold))
				}
			}
		}
	}
	for _, testName := range expectations.TestNames {
		if _, ok := seenTests[testName]; !ok {
			failures = append(failures, fmt.Errorf("missing expected platform-validation report for %s", testName))
		}
	}
	for _, scenario := range expectations.Scenarios {
		if _, ok := seenScenarios[scenario]; !ok {
			failures = append(failures, fmt.Errorf("missing expected platform-validation report for scenario %s", scenario))
		}
	}
	if checked == 0 {
		failures = append(failures, fmt.Errorf("no scorecards found in %s", dir))
	}
	if len(failures) > 0 {
		return checked, errors.Join(failures...)
	}
	return checked, nil
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
