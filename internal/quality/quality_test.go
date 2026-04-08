package quality

import (
	"os"
	"strings"
	"testing"

	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func TestGradeFailsOutOfScopeAnswer(t *testing.T) {
	view := policyruntime.EngineResult{
		Bundle: &policy.Bundle{DomainBoundary: policy.DomainBoundary{
			BlockedTopics: []string{"human food", "cooking"},
		}},
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: "out_of_scope",
			Action:         "refuse",
			Reply:          "I can help with pet-store questions, but not cooking or human food.",
			Reasons:        []string{"blocked_topic:cooking"},
		},
	}

	card := Grade(view, "Here is a recipe for cooking human food.", nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure", card)
	}
	if got := card.Dimensions["topic_scope_compliance"]; got.Passed || got.Score != 0 {
		t.Fatalf("topic scope dimension = %#v, want failed zero score", got)
	}
}

func TestGradePassesConfiguredBoundaryReply(t *testing.T) {
	view := policyruntime.EngineResult{
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: "out_of_scope",
			Action:         "refuse",
			Reply:          "I can help with pet-store questions, but not cooking or human food.",
			Reasons:        []string{"blocked_topic:cooking"},
		},
	}

	card := Grade(view, view.ScopeBoundaryStage.Reply, nil)
	if HardFailed(card) {
		t.Fatalf("scorecard = %#v, want no hard failure", card)
	}
	if got := card.Dimensions["topic_scope_compliance"]; !got.Passed {
		t.Fatalf("topic scope dimension = %#v, want passed", got)
	}
}

func TestBuildResponsePlanIncludesQualityInputs(t *testing.T) {
	view := policyruntime.EngineResult{
		Bundle: &policy.Bundle{
			DomainBoundary: policy.DomainBoundary{BlockedTopics: []string{"cooking"}},
			Soul:           policy.Soul{DefaultLanguage: "id", StyleRules: []string{"be concise"}},
		},
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: "adjacent",
			Action:         "redirect",
			Reply:          "I can help with pet-safe food questions.",
		},
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_step",
			Instruction: "Verify the order number first.",
		},
		MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{
			ID:   "verify",
			Then: "Verify the order number first.",
		}}},
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			Citations: []knowledge.Citation{{URI: "kb://pet-food"}},
		}}},
		CustomerPreferences: []customer.Preference{{Key: "preferred_name", Value: "Rina"}},
	}

	plan := BuildResponsePlan(view)
	rendered := FormatResponsePlan(plan)
	for _, want := range []string{"Verify the order number first.", "cooking", "be concise", "preferred_name: Rina", "kb://pet-food", "adjacent"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("plan = %s, want %q", rendered, want)
		}
	}
	if !plan.RetrievalRequired {
		t.Fatalf("plan = %#v, want retrieval required", plan)
	}
	if len(plan.RequiredEvidenceClasses) == 0 || len(plan.RequiredVerificationSteps) == 0 {
		t.Fatalf("plan = %#v, want evidence classes and verification steps", plan)
	}
}

func TestGradeFlagsUnsupportedSpecificKnowledgeClaim(t *testing.T) {
	view := policyruntime.EngineResult{
		MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{
			ID:   "refunds",
			Then: "Verify the order before offering refund options.",
		}}},
	}

	card := Grade(view, "You are guaranteed an instant replacement within 30 days.", nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure for unsupported specific claim", card)
	}
	if got := card.Dimensions["knowledge_grounding"]; got.Passed {
		t.Fatalf("knowledge grounding dimension = %#v, want failed", got)
	}
	if len(card.Claims) == 0 || len(card.EvidenceMatches) == 0 {
		t.Fatalf("scorecard = %#v, want extracted claims and evidence matches", card)
	}
}

func TestGradeFlagsPrematureHighRiskCommitment(t *testing.T) {
	view := policyruntime.EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Verify the order details before offering refund or replacement options.",
		},
	}

	card := Grade(view, "You qualify for a replacement right away.", nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure for premature commitment", card)
	}
	if got := card.Dimensions["policy_adherence"]; got.Passed {
		t.Fatalf("policy adherence dimension = %#v, want failed", got)
	}
}

func TestGradeAllowsHighRiskCommitmentAfterVerificationLanguage(t *testing.T) {
	view := policyruntime.EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Verify the order details before offering refund or replacement options.",
		},
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "Damaged orders can be reviewed for replacement after verification.",
			ResultHash:  "hash_verify",
			Citations:   []knowledge.Citation{{URI: "kb://verify"}},
		}}},
	}

	card := Grade(view, "Your order can be reviewed for replacement after verification.", nil)
	if HardFailed(card) {
		t.Fatalf("scorecard = %#v, want pass", card)
	}
}

func TestGradeSupportsSpecificKnowledgeClaimFromRetrievedEvidence(t *testing.T) {
	view := policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "Electronics purchased within 30 days qualify for an instant replacement before refund review.",
			ResultHash:  "hash_1",
			Citations:   []knowledge.Citation{{URI: "kb://electronics"}},
		}}},
	}

	card := Grade(view, "Electronics purchased within 30 days qualify for an instant replacement before refund review.", nil)
	if HardFailed(card) {
		t.Fatalf("scorecard = %#v, want supported claim to pass", card)
	}
	if got := card.Dimensions["knowledge_grounding"]; !got.Passed {
		t.Fatalf("knowledge grounding dimension = %#v, want passed", got)
	}
	if len(card.EvidenceMatches) == 0 || !card.EvidenceMatches[0].Supported {
		t.Fatalf("evidence matches = %#v, want supported match", card.EvidenceMatches)
	}
	if got := card.Dimensions["retrieval_quality"]; !got.Passed {
		t.Fatalf("retrieval quality dimension = %#v, want passed", got)
	}
}

func TestGradeFlagsNoisyUnusedRetrieval(t *testing.T) {
	view := policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        strings.Repeat("Long irrelevant catalog text without matching the answer. ", 60),
			ResultHash:  "hash_irrelevant",
			Citations:   []knowledge.Citation{{URI: "kb://irrelevant"}},
		}}},
	}

	card := Grade(view, "I can help with that.", nil)
	if got := card.Dimensions["retrieval_quality"]; got.Score >= 1 || HardFailed(card) {
		t.Fatalf("retrieval quality dimension = %#v, want warning-only degraded score", got)
	}
}

func TestGradeFailsWhenRetrievalIsRequiredButMissing(t *testing.T) {
	view := policyruntime.EngineResult{
		MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{
			ID:   "refund_verify",
			Then: "Verify the order before promising a refund or replacement.",
		}}},
	}

	card := Grade(view, "According to the policy, you qualify for a refund after review.", nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure", card)
	}
	if got := card.Dimensions["retrieval_quality"]; got.Passed {
		t.Fatalf("retrieval quality dimension = %#v, want failed", got)
	}
}

func TestGradeFlagsMissedIndonesianPreference(t *testing.T) {
	view := policyruntime.EngineResult{
		CustomerPreferences: []customer.Preference{{
			ID:    "pref_language",
			Key:   "preferred_language",
			Value: "indonesian",
		}},
	}

	card := Grade(view, "I can help with your notification options.", nil)
	if HardFailed(card) {
		t.Fatalf("scorecard = %#v, want warning-only language finding", card)
	}
	if got := card.Dimensions["multilingual_quality"]; !got.Passed || got.Score >= 1 {
		t.Fatalf("multilingual dimension = %#v, want warning score", got)
	}
}

func TestSoftDimensionUpdatesDoNotLowerOverall(t *testing.T) {
	card := Scorecard{
		Overall:    1,
		Passed:     true,
		Dimensions: map[string]DimensionScore{},
	}
	updateSoftDimension(&card, "tone_persona", 0.2, []string{"too terse"})
	if card.Overall != 1 {
		t.Fatalf("overall = %v, want unchanged by subjective warning", card.Overall)
	}
	if got := card.Dimensions["tone_persona"]; got.Score != 0.2 || !got.Passed {
		t.Fatalf("tone dimension = %#v, want warning-only score", got)
	}
}

func TestProductionReadinessScenariosDefinesTwoHundredCases(t *testing.T) {
	scenarios := ProductionReadinessScenarios()
	if len(scenarios) != 200 {
		t.Fatalf("scenario count = %d, want 200", len(scenarios))
	}
	liveGate := 0
	categories := map[string]struct{}{}
	for _, scenario := range scenarios {
		if scenario.ID == "" || scenario.Domain == "" || scenario.Category == "" || scenario.Input == "" {
			t.Fatalf("scenario = %#v, want required fields", scenario)
		}
		if scenario.MinimumOverall <= 0 || scenario.MinimumOverall > 1 {
			t.Fatalf("scenario = %#v, want valid minimum overall", scenario)
		}
		if scenario.RiskTier == "" {
			t.Fatalf("scenario = %#v, want risk tier", scenario)
		}
		if len(scenario.RequiredEvidenceClasses) == 0 && (scenario.Category == "knowledge_grounding" || scenario.Category == "retrieval_quality") {
			t.Fatalf("scenario = %#v, want required evidence classes", scenario)
		}
		categories[scenario.Category] = struct{}{}
		if scenario.LiveGate {
			liveGate++
		}
	}
	if liveGate != 30 {
		t.Fatalf("live gate scenario count = %d, want 30", liveGate)
	}
	if len(categories) < 10 {
		t.Fatalf("categories = %#v, want broad platform coverage", categories)
	}
}

func TestProductionReadinessScenariosHaveDeterministicQualityCoverage(t *testing.T) {
	for _, scenario := range ProductionReadinessScenarios() {
		t.Run(scenario.ID, func(t *testing.T) {
			view, response, ok := ScenarioFixture(scenario)
			if !ok {
				t.Fatalf("scenario %s has no deterministic fixture", scenario.ID)
			}
			card := Grade(view, response, nil)
			if card.Dimensions == nil {
				t.Fatalf("scenario %s scorecard = %#v, want dimensions", scenario.ID, card)
			}
			for _, dimension := range scenario.ExpectedQuality {
				if _, ok := card.Dimensions[dimension]; !ok {
					t.Fatalf("scenario %s dimensions = %#v, want %s", scenario.ID, card.Dimensions, dimension)
				}
			}
			if scenario.ExpectedRefusalMode != "" && scenario.ExpectedRefusalMode != view.ScopeBoundaryStage.Action && scenario.ExpectedRefusalMode != "allow" {
				t.Fatalf("scenario %s action = %q, want %q", scenario.ID, view.ScopeBoundaryStage.Action, scenario.ExpectedRefusalMode)
			}
			if scenario.ExpectedLanguage != "" && scenario.Category == "multilingual" && scenario.ExpectedLanguage == "id" && !looksIndonesian(response) {
				t.Fatalf("scenario %s response = %q, want Indonesian", scenario.ID, response)
			}
			if HardFailed(card) {
				t.Fatalf("scenario %s scorecard = %#v, want deterministic passing baseline", scenario.ID, card)
			}
		})
	}
}

func TestProductionReadinessScenariosMergesSeedFileFromEnv(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "scenario-seeds-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(`[{"id":"seed_custom_quality_case","domain":"support","category":"failure_modes","input":"seeded case","expected_quality":["policy_adherence"],"risk":"medium","risk_tier":"medium","minimum_overall":0.75,"required_verification_steps":["confirm missing detail"],"allowed_commitments":["cautious policy-backed guidance"]}]`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUALITY_SCENARIO_SEEDS", file.Name())

	scenarios := ProductionReadinessScenarios()
	found := false
	for _, scenario := range scenarios {
		if scenario.ID == "seed_custom_quality_case" {
			found = true
			if scenario.MinimumOverall != 0.75 {
				t.Fatalf("scenario = %#v, want merged minimum overall", scenario)
			}
			if scenario.RiskTier != "medium" || len(scenario.RequiredVerificationSteps) == 0 {
				t.Fatalf("scenario = %#v, want merged richer scenario fields", scenario)
			}
		}
	}
	if !found {
		t.Fatalf("merged scenarios = %#v, want seed scenario", scenarios)
	}
}
