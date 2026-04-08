package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckReportsPassesExpectedReports(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "TestPlatformValidationExample", true, false, 0.9)

	checked, err := checkReports(dir, reportExpectations{TestNames: []string{"TestPlatformValidationExample"}, MinOverall: 0.7})
	if err != nil {
		t.Fatalf("checkReports returned error: %v", err)
	}
	if checked != 1 {
		t.Fatalf("checked = %d, want 1", checked)
	}
}

func TestCheckReportsFailsMissingExpectedReport(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "TestPlatformValidationExample", true, false, 0.9)

	_, err := checkReports(dir, reportExpectations{TestNames: []string{"TestPlatformValidationExample", "TestPlatformValidationMissing"}, MinOverall: 0.7})
	if err == nil || !strings.Contains(err.Error(), "TestPlatformValidationMissing") {
		t.Fatalf("error = %v, want missing expected report", err)
	}
}

func TestCheckReportsFailsHardFailedScorecard(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "TestPlatformValidationExample", false, true, 0.9)

	_, err := checkReports(dir, reportExpectations{TestNames: []string{"TestPlatformValidationExample"}, MinOverall: 0.7})
	if err == nil || !strings.Contains(err.Error(), "failed quality gate") {
		t.Fatalf("error = %v, want quality gate failure", err)
	}
}

func TestCheckReportsFailsLowOverallScore(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "TestPlatformValidationExample", true, false, 0.6)

	_, err := checkReports(dir, reportExpectations{TestNames: []string{"TestPlatformValidationExample"}, MinOverall: 0.7})
	if err == nil || !strings.Contains(err.Error(), "below minimum") {
		t.Fatalf("error = %v, want minimum-score failure", err)
	}
}

func TestCheckReportsFailsMissingExpectedScenario(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "TestPlatformValidationExample", true, false, 0.9)

	_, err := checkReports(dir, reportExpectations{Scenarios: []string{"missing_scenario"}, MinOverall: 0.7})
	if err == nil || !strings.Contains(err.Error(), "missing_scenario") {
		t.Fatalf("error = %v, want missing scenario failure", err)
	}
}

func TestCheckReportsUsesScenarioMinimumOverallWhenHigherThanGlobal(t *testing.T) {
	dir := t.TempDir()
	writeScenarioReport(t, dir, "TestPlatformValidationScenario", "ecommerce_knowledge_grounding_damaged_toaster_replacem", true, false, 0.82)

	_, err := checkReports(dir, reportExpectations{Scenarios: []string{"ecommerce_knowledge_grounding_damaged_toaster_replacem"}, MinOverall: 0.7})
	if err == nil || !strings.Contains(err.Error(), "below minimum") {
		t.Fatalf("error = %v, want scenario-specific threshold failure", err)
	}
}

func writeReport(t *testing.T, dir, testName string, passed, hardFailed bool, overall float64) {
	t.Helper()
	raw := `{
  "test_name": "` + testName + `",
  "scenario": "` + testName + `_scenario",
  "sessions": [{
    "id": "sess_1",
    "scorecards": {
      "exec_1": {
        "overall": ` + floatJSON(overall) + `,
        "passed": ` + boolJSON(passed) + `,
        "hard_failed": ` + boolJSON(hardFailed) + `,
        "hard_failures": []
      }
    }
  }]
}`
	if err := os.WriteFile(filepath.Join(dir, testName+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeScenarioReport(t *testing.T, dir, testName, scenario string, passed, hardFailed bool, overall float64) {
	t.Helper()
	raw := `{
  "test_name": "` + testName + `",
  "scenario": "` + scenario + `",
  "sessions": [{
    "id": "sess_1",
    "scorecards": {
      "exec_1": {
        "overall": ` + floatJSON(overall) + `,
        "passed": ` + boolJSON(passed) + `,
        "hard_failed": ` + boolJSON(hardFailed) + `,
        "hard_failures": []
      }
    }
  }]
}`
	if err := os.WriteFile(filepath.Join(dir, testName+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func floatJSON(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}
