package parity

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/tool"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func TestLoadFixtureParsesGoldenScenarios(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fixture, err := LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}
	if fixture.Version == "" {
		t.Fatalf("fixture version is empty")
	}
	if len(fixture.Scenarios) == 0 {
		t.Fatalf("fixture scenarios are empty")
	}
}

func TestEvaluateScenarioDetectsExpectationAndParityFailures(t *testing.T) {
	scenario := Scenario{
		ID: "example",
		Expect: Expectations{
			MatchedGuidelines: []string{"greet"},
			NoMatch:           boolPtr(false),
		},
	}
	left := NormalizedResult{
		MatchedGuidelines: []string{"greet"},
		NoMatch:           false,
	}
	right := NormalizedResult{
		MatchedGuidelines: []string{"other"},
		NoMatch:           true,
	}
	report := EvaluateScenario(scenario, left, right)
	if report.Passed {
		t.Fatalf("EvaluateScenario() passed, want failure")
	}
	if len(report.ExpectationErrors) == 0 {
		t.Fatalf("ExpectationErrors = 0, want > 0")
	}
	if len(report.DiffErrors) == 0 {
		t.Fatalf("DiffErrors = 0, want > 0")
	}
}

func TestEvaluateScenarioCanIgnoreParlantDrift(t *testing.T) {
	scenario := Scenario{
		ID:                "example",
		SkipEngineDiff:    true,
		SkipParlantExpect: true,
		Expect: Expectations{
			MatchedGuidelines: []string{"greet"},
		},
	}
	left := NormalizedResult{
		MatchedGuidelines: []string{"greet"},
	}
	right := NormalizedResult{
		MatchedGuidelines: []string{"other"},
	}
	report := EvaluateScenario(scenario, left, right)
	if !report.Passed {
		t.Fatalf("EvaluateScenario() failed, want pass when live Parlant drift is ignored: %#v", report)
	}
	if len(report.DiffErrors) != 0 {
		t.Fatalf("DiffErrors = %#v, want 0", report.DiffErrors)
	}
}

func TestAuthoritativeParlantFallbackUsesExpectationSurface(t *testing.T) {
	scenario := Scenario{
		ID: "journey_dependency_guideline_under_21",
		Expect: Expectations{
			MatchedGuidelines:    []string{"under_21"},
			SuppressedGuidelines: []string{"age_21_or_older"},
			ResolutionRecords: []ResolutionExpectation{
				{EntityID: "age_21_or_older", Kind: "deprioritized"},
			},
			ActiveJourney:   &IDExpectation{ID: "Book Flight"},
			JourneyDecision: "advance",
			NextJourneyNode: "ask_origin",
		},
	}
	got := authoritativeParlantFallback(scenario)
	report := EvaluateScenario(scenario, got, got)
	if !report.Passed {
		t.Fatalf("authoritativeParlantFallback() failed expectation surface: %#v", report)
	}
}

func TestEvaluateScenarioChecksDecisionLevelFields(t *testing.T) {
	scenario := Scenario{
		ID: "decision_fields",
		Expect: Expectations{
			ResolutionRecords: []ResolutionExpectation{
				{EntityID: "greet_hi", Kind: "deprioritized"},
			},
			ProjectedFollowUps: map[string][]string{
				"journey_node:Book Flight:ask_origin": {"journey_node:Book Flight:ask_destination"},
			},
			ToolCandidates: []string{"local:lock_card", "local:try_unlock_card"},
			ToolCandidateStates: map[string]string{
				"local:lock_card":       "should_run",
				"local:try_unlock_card": "already_staged",
			},
			OverlappingToolGroups: [][]string{
				{"local:lock_card", "local:try_unlock_card"},
			},
		},
	}
	got := NormalizedResult{
		ResolutionRecords: []NormalizedResolution{
			{EntityID: "greet_hi", Kind: "deprioritized"},
		},
		ProjectedFollowUps: map[string][]string{
			"journey_node:Book Flight:ask_origin": {"journey_node:Book Flight:ask_destination"},
		},
		ToolCandidates: []string{"local:try_unlock_card", "local:lock_card"},
		ToolCandidateStates: map[string]string{
			"local:try_unlock_card": "already_staged",
			"local:lock_card":       "should_run",
		},
		OverlappingToolGroups: [][]string{
			{"local:try_unlock_card", "local:lock_card"},
		},
	}
	report := EvaluateScenario(scenario, got, got)
	if !report.Passed {
		t.Fatalf("EvaluateScenario() failed, want pass for matching decision-level fields: %#v", report)
	}
}

func TestEvaluateScenarioIgnoresExtraResolutionNoneOutsideExpectationScope(t *testing.T) {
	scenario := Scenario{
		ID: "resolution_scope",
		Expect: Expectations{
			ResolutionRecords: []ResolutionExpectation{
				{EntityID: "journey:flow", Kind: "deprioritized"},
			},
		},
	}
	got := NormalizedResult{
		ResolutionRecords: []NormalizedResolution{
			{EntityID: "journey:flow", Kind: "deprioritized"},
			{EntityID: "winner", Kind: "none"},
		},
	}
	report := EvaluateScenario(scenario, got, got)
	if !report.Passed {
		t.Fatalf("EvaluateScenario() failed, want pass when extra none records are outside expectation scope: %#v", report)
	}
}

func TestIdsFromSuppressedIncludesProjectedJourneyNodes(t *testing.T) {
	got := idsFromSuppressed([]policyruntime.SuppressedGuideline{
		{ID: "journey_node:Book Flight:ask_origin", Reason: "condition_conflict"},
		{ID: "age_21_or_older", Reason: "condition_conflict"},
	})
	if len(got) != 2 || got[0] != "age_21_or_older" || got[1] != "journey_node:Book Flight:ask_origin" {
		t.Fatalf("idsFromSuppressed() = %#v, want both suppressed ids including projected journey nodes", got)
	}
}

func TestRunUsesScenarioTimeout(t *testing.T) {
	t.Setenv("EMCIE_API_KEY", "")
	ctx := context.Background()
	start := time.Now()
	_, err := Run(ctx, Options{
		FixturePath:     filepath.Join("..", "..", "examples", "golden_scenarios.yaml"),
		ScenarioID:      "journey_reset_password_start",
		ParlantRoot:     "/definitely/missing/parlant",
		ScenarioTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Run() took too long, scenario timeout may not be applied")
	}
}

func TestRunFallsBackToAuthoritativeWhenParlantLiveUnavailable(t *testing.T) {
	t.Setenv("EMCIE_API_KEY", "")
	ctx := context.Background()
	report, err := Run(ctx, Options{
		FixturePath: filepath.Join("..", "..", "examples", "golden_scenarios.yaml"),
		ScenarioID:  "relational_inactive_priority_journey_does_not_suppress_active_journey",
		ParlantRoot: "/definitely/missing/parlant",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Scenarios) != 1 {
		t.Fatalf("scenario count = %d, want 1", len(report.Scenarios))
	}
	if !report.Scenarios[0].Passed {
		t.Fatalf("report = %#v, want authoritative fallback to pass", report.Scenarios[0])
	}
}

func TestRunFixtureToolIncludesModulePathAndDocumentID(t *testing.T) {
	view := policyruntime.ResolvedView{
		ToolDecision: policyruntime.ToolDecision{
			SelectedTool: "lookup_doc",
			CanRun:       true,
			Arguments:    map[string]any{"query": "refund policy"},
		},
	}
	metadata, err := json.Marshal(map[string]any{
		"module_path": "tests.tool_utilities",
		"document_id": "doc_123",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	catalog := []tool.CatalogEntry{{
		ID:           "local:lookup_doc",
		ProviderID:   "local",
		Name:         "lookup_doc",
		MetadataJSON: string(metadata),
	}}

	_, calls := runFixtureTool(view, catalog)
	if len(calls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(calls))
	}
	if calls[0].DocumentID != "doc_123" {
		t.Fatalf("DocumentID = %q, want %q", calls[0].DocumentID, "doc_123")
	}
	if calls[0].ModulePath != "tests.tool_utilities" {
		t.Fatalf("ModulePath = %q, want %q", calls[0].ModulePath, "tests.tool_utilities")
	}
}

func boolPtr(v bool) *bool {
	return &v
}
