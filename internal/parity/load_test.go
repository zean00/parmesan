package parity

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	policyruntime "github.com/sahal/parmesan/internal/engine/policy"
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

func TestRunFixtureToolIncludesModulePathAndDocumentID(t *testing.T) {
	view := policyruntime.EngineResult{
		ToolDecisionStage: policyruntime.ToolDecisionStageResult{
			Decision: policyruntime.ToolDecision{
				SelectedTool: "lookup_doc",
				CanRun:       true,
				Arguments:    map[string]any{"query": "refund policy"},
			},
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

func TestNormalizeParmesanKeepsActiveJourneyNodeAsCurrentState(t *testing.T) {
	view := policyruntime.EngineResult{
		ActiveJourney: &policy.Journey{
			ID: "Reset Password Journey",
			States: []policy.JourneyNode{
				{ID: "ask_account_name", Instruction: "What is the name of your account?"},
				{ID: "ask_contact", Instruction: "What email or phone is attached to the account?"},
			},
		},
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "ask_account_name",
			Instruction: "What is the name of your account?",
		},
		JourneyProgressStage: policyruntime.JourneyProgressStageResult{
			Decision: policyruntime.JourneyDecision{
				Action:    "start",
				NextState: "ask_contact",
			},
		},
		ResponseAnalysisStage: policyruntime.ResponseAnalysisStageResult{
			CandidateTemplates: nil,
		},
		ResolutionRecords: []policyruntime.ResolutionRecord{
			{EntityID: "journey:Reset Password Journey", Kind: policyruntime.ResolutionNone},
		},
	}

	got := normalizeParmesan(view, nil, "What is the name of your account?", nil, false, "pass")
	if got.ActiveJourneyNode != "ask_account_name" {
		t.Fatalf("ActiveJourneyNode = %q, want %q", got.ActiveJourneyNode, "ask_account_name")
	}
	if got.NextJourneyNode != "ask_contact" {
		t.Fatalf("NextJourneyNode = %q, want %q", got.NextJourneyNode, "ask_contact")
	}
}

func TestEvaluateScenarioMustNotIncludeAllowsNegatedMention(t *testing.T) {
	scenario := Scenario{
		ID: "negated_mention",
		Expect: Expectations{
			ResponseSemantics: ResponseSemantics{
				MustNotInclude: []string{"pineapple"},
			},
		},
	}
	got := NormalizedResult{
		ResponseText: "Unfortunately, pineapple isn't available right now.",
	}
	report := EvaluateScenario(scenario, got, got)
	if !report.Passed {
		t.Fatalf("EvaluateScenario() = %#v, want pass for negated mention", report)
	}
}

func TestEvaluateScenarioMustNotIncludeRejectsAffirmativeMention(t *testing.T) {
	scenario := Scenario{
		ID: "affirmative_mention",
		Expect: Expectations{
			ResponseSemantics: ResponseSemantics{
				MustNotInclude: []string{"pineapple"},
			},
		},
	}
	got := NormalizedResult{
		ResponseText: "You should try pineapple on your pizza.",
	}
	report := EvaluateScenario(scenario, got, got)
	if report.Passed {
		t.Fatalf("EvaluateScenario() passed, want failure for affirmative mention")
	}
}

func TestEvaluateScenarioMustNotIncludeRejectsAffirmativeMentionAfterNegatedMention(t *testing.T) {
	scenario := Scenario{
		ID: "mixed_mention",
		Expect: Expectations{
			ResponseSemantics: ResponseSemantics{
				MustNotInclude: []string{"pineapple"},
			},
		},
	}
	got := NormalizedResult{
		ResponseText: "Pineapple isn't available, but pineapple juice is available.",
	}
	report := EvaluateScenario(scenario, got, got)
	if report.Passed {
		t.Fatalf("EvaluateScenario() passed, want failure when a later affirmative mention exists")
	}
}

func TestNormalizeParmesanKeepsSuppressionWhenLaterResolutionDeprioritizesEntity(t *testing.T) {
	got := normalizedSuppressedGuidelines(
		[]policyruntime.SuppressedGuideline{
			{ID: "winner", Reason: "deprioritized"},
		},
		[]policyruntime.ResolutionRecord{
			{EntityID: "winner", Kind: policyruntime.ResolutionNone},
			{EntityID: "winner", Kind: policyruntime.ResolutionDeprioritized},
		},
		nil,
	)
	if len(got) != 1 || got[0] != "winner" {
		t.Fatalf("normalizedSuppressedGuidelines() = %#v, want winner suppression preserved", got)
	}
}

func TestNormalizeParmesanDropsSuppressionForFinallyMatchedGuideline(t *testing.T) {
	got := normalizedSuppressedGuidelines(
		[]policyruntime.SuppressedGuideline{
			{ID: "under_21", Reason: "unmet_dependency"},
			{ID: "age_21_or_older", Reason: "deprioritized"},
		},
		[]policyruntime.ResolutionRecord{
			{EntityID: "under_21", Kind: policyruntime.ResolutionNone},
			{EntityID: "under_21", Kind: policyruntime.ResolutionUnmetDependency},
			{EntityID: "age_21_or_older", Kind: policyruntime.ResolutionDeprioritized},
		},
		[]policy.Guideline{
			{ID: "under_21"},
		},
	)
	if len(got) != 1 || got[0] != "age_21_or_older" {
		t.Fatalf("normalizedSuppressedGuidelines() = %#v, want only age_21_or_older", got)
	}
}

func TestNormalizeResolutionRecordsDedupesIdenticalEntries(t *testing.T) {
	got := normalizeResolutionRecords([]policyruntime.ResolutionRecord{
		{EntityID: "under_21", Kind: policyruntime.ResolutionNone},
		{EntityID: "under_21", Kind: policyruntime.ResolutionNone},
		{EntityID: "under_21", Kind: policyruntime.ResolutionUnmetDependency},
		{EntityID: "under_21", Kind: policyruntime.ResolutionUnmetDependency},
	})
	want := []NormalizedResolution{
		{EntityID: "under_21", Kind: string(policyruntime.ResolutionNone)},
		{EntityID: "under_21", Kind: string(policyruntime.ResolutionUnmetDependency)},
	}
	if !sameResolutionSet(wantToResolutionExpectations(want), got) {
		t.Fatalf("normalizeResolutionRecords() = %#v, want %#v", got, want)
	}
}

func wantToResolutionExpectations(items []NormalizedResolution) []ResolutionExpectation {
	out := make([]ResolutionExpectation, 0, len(items))
	for _, item := range items {
		out = append(out, ResolutionExpectation{EntityID: item.EntityID, Kind: item.Kind})
	}
	return out
}

func boolPtr(v bool) *bool {
	return &v
}
