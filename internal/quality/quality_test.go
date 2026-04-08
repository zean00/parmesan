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

func TestProductionReadinessScenariosDefinesHundredCases(t *testing.T) {
	scenarios := ProductionReadinessScenarios()
	if len(scenarios) != 100 {
		t.Fatalf("scenario count = %d, want 100", len(scenarios))
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
		categories[scenario.Category] = struct{}{}
		if scenario.LiveGate {
			liveGate++
		}
	}
	if liveGate < 10 {
		t.Fatalf("live gate scenario count = %d, want at least 10", liveGate)
	}
	if len(categories) < 10 {
		t.Fatalf("categories = %#v, want broad platform coverage", categories)
	}
}

func TestProductionReadinessScenariosHaveDeterministicQualityCoverage(t *testing.T) {
	for _, scenario := range ProductionReadinessScenarios() {
		t.Run(scenario.ID, func(t *testing.T) {
			view, response := deterministicScenarioQualityCase(scenario)
			card := Grade(view, response, nil)
			if card.Dimensions == nil {
				t.Fatalf("scenario %s scorecard = %#v, want dimensions", scenario.ID, card)
			}
			for _, dimension := range scenario.ExpectedQuality {
				if _, ok := card.Dimensions[dimension]; !ok {
					t.Fatalf("scenario %s dimensions = %#v, want %s", scenario.ID, card.Dimensions, dimension)
				}
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
	if _, err := file.WriteString(`[{"id":"seed_custom_quality_case","domain":"support","category":"failure_modes","input":"seeded case","expected_quality":["policy_adherence"],"risk":"medium","minimum_overall":0.75}]`); err != nil {
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
		}
	}
	if !found {
		t.Fatalf("merged scenarios = %#v, want seed scenario", scenarios)
	}
}

func deterministicScenarioQualityCase(scenario ScenarioExpectation) (policyruntime.EngineResult, string) {
	switch scenario.Category {
	case "knowledge_grounding", "retrieval_quality":
		evidence := "Order support requires verification before refund or replacement review. Damaged items may qualify after policy review. Notifications can be sent by email."
		if scenario.Category == "retrieval_quality" && strings.Contains(strings.ToLower(scenario.Input), "citation") {
			evidence = "Policy support requires citation-backed retrieval before a replacement answer."
		}
		return policyruntime.EngineResult{
			RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
				RetrieverID: "wiki",
				Data:        evidence,
				ResultHash:  "scenario_evidence",
				Citations:   []knowledge.Citation{{URI: "kb://scenario"}},
			}}},
		}, strings.TrimSuffix(evidence, ".")
	case "topic_scope":
		reply := "I can help with pet-store questions, but not cooking or human food."
		if strings.Contains(scenario.Input, "pet food") || strings.Contains(scenario.Input, "pet-safe") {
			return policyruntime.EngineResult{
				Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{AllowedTopics: []string{"pet food", "pet-safe ingredients"}}},
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "in_scope", Action: "allow"},
			}, "I can help compare pet food options in the store catalog."
		}
		return policyruntime.EngineResult{
			Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{BlockedTopics: []string{"cooking", "human food", "finance"}}},
			ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "out_of_scope", Action: "refuse", Reply: reply, Reasons: []string{"scenario_scope"}},
		}, reply
	case "preference":
		return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_name", Key: "preferred_name", Value: "Rina"}}}, "Rina, I can help with that."
	case "multilingual":
		if strings.Contains(strings.ToLower(scenario.Input), "indonesian") || strings.Contains(strings.ToLower(scenario.Input), "mixed") {
			return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_language", Key: "preferred_language", Value: "indonesian"}}}, "Saya bisa membantu Anda dengan pilihan itu."
		}
		return policyruntime.EngineResult{Bundle: &policy.Bundle{Soul: policy.Soul{DefaultLanguage: "en"}}}, "I can help with that in English."
	case "journey_adherence":
		return policyruntime.EngineResult{
			ActiveJourneyState: &policy.JourneyNode{ID: "state_verify", Instruction: "Please share the order number before I review options."},
		}, "Please share the order number before I review options."
	case "tool_and_approval":
		return policyruntime.EngineResult{
			MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "approval", Then: "Request approval before changing an order."}}},
		}, "I need approval before changing the order."
	case "soul_persona":
		return policyruntime.EngineResult{Bundle: &policy.Bundle{Soul: policy.Soul{Tone: "warm", Verbosity: "concise"}}}, "I can help with that. I will keep this concise."
	case "refusal_escalation", "failure_modes":
		return policyruntime.EngineResult{
			MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "safe_next_step", Then: "Avoid overcommitting and ask for the missing detail."}}},
		}, "I need one more detail before I can continue safely."
	default:
		return policyruntime.EngineResult{}, "I can help with that."
	}
}
