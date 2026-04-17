package quality

import (
	"os"
	"strings"
	"testing"

	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
	policyruntime "github.com/sahal/parmesan/internal/engine/policy"
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

func TestGradeFailsWeakEscalationReply(t *testing.T) {
	view := policyruntime.EngineResult{
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: "uncertain",
			Action:         "escalate",
			Reply:          "I will continue.",
			Reasons:        []string{"needs_operator"},
		},
	}

	card := Grade(view, view.ScopeBoundaryStage.Reply, nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure for weak escalation", card)
	}
	if got := card.Dimensions["refusal_escalation_quality"]; got.Passed {
		t.Fatalf("refusal escalation dimension = %#v, want failed", got)
	}
}

func TestGradeFailsWeakRefusalReply(t *testing.T) {
	view := policyruntime.EngineResult{
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Classification: "out_of_scope",
			Action:         "refuse",
			Reply:          "Okay.",
			Reasons:        []string{"blocked_topic"},
		},
	}

	card := Grade(view, view.ScopeBoundaryStage.Reply, nil)
	if !HardFailed(card) {
		t.Fatalf("scorecard = %#v, want hard failure for weak refusal", card)
	}
	if got := card.Dimensions["refusal_escalation_quality"]; got.Passed {
		t.Fatalf("refusal escalation dimension = %#v, want failed", got)
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

func TestGradeAllowsVerificationFirstOptionsLanguage(t *testing.T) {
	view := policyruntime.EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Verify the order details before offering refund or replacement options.",
		},
	}

	card := Grade(view, "Please share the order number and tell me what arrived damaged so I can review replacement or refund options.", nil)
	if HardFailed(card) {
		t.Fatalf("scorecard = %#v, want verification-first options language to pass", card)
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

func TestBuildResponsePlanIncludesHighRiskBlueprint(t *testing.T) {
	view := policyruntime.EngineResult{
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "verify_state",
			Instruction: "Please share the order number before I review refund or replacement options.",
		},
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "Damaged orders can be reviewed for replacement after verification.",
			ResultHash:  "hash_verify",
			Citations:   []knowledge.Citation{{URI: "kb://verify"}},
		}}},
	}

	plan := BuildResponsePlan(view)
	if plan.RiskTier != "high" {
		t.Fatalf("risk tier = %q, want high", plan.RiskTier)
	}
	if len(plan.DesiredStructure) == 0 {
		t.Fatalf("desired structure = %#v, want constrained blueprint", plan.DesiredStructure)
	}
	if !containsString(plan.DesiredStructure, []string{"When you rely on retrieved knowledge, cite the supporting source identifier or URI."}) {
		t.Fatalf("desired structure = %#v, want citation guidance", plan.DesiredStructure)
	}
	if !containsString(plan.DesiredStructure, []string{"Do not promise eligibility, approval, or timing before verification is complete."}) {
		t.Fatalf("desired structure = %#v, want verification-first guidance", plan.DesiredStructure)
	}
}

func TestBuildResponsePlanUsesQualityProfileOverrides(t *testing.T) {
	view := policyruntime.EngineResult{
		QualityProfile: policy.QualityProfile{
			ID:                        "support_profile",
			RiskTier:                  "high",
			AllowedCommitments:        []string{"evidence-backed support guidance"},
			RequiredEvidence:          []string{"retrieved_knowledge", "matched_guideline"},
			RequiredVerificationSteps: []string{"verify the account first"},
			BlueprintRules: map[string][]string{
				"default": {"Use the approved support blueprint."},
			},
		},
	}

	plan := BuildResponsePlan(view)
	if plan.RiskTier != "high" {
		t.Fatalf("risk tier = %q, want high", plan.RiskTier)
	}
	if !containsString(plan.AllowedCommitments, []string{"evidence-backed support guidance"}) {
		t.Fatalf("allowed commitments = %#v, want profile override", plan.AllowedCommitments)
	}
	if !containsString(plan.RequiredEvidence, []string{"retrieved_knowledge"}) {
		t.Fatalf("required evidence = %#v, want profile override", plan.RequiredEvidence)
	}
	if !containsString(plan.RequiredVerificationSteps, []string{"verify the account first"}) {
		t.Fatalf("verification steps = %#v, want profile override", plan.RequiredVerificationSteps)
	}
	if !containsString(plan.DesiredStructure, []string{"Use the approved support blueprint."}) {
		t.Fatalf("desired structure = %#v, want profile blueprint", plan.DesiredStructure)
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

func TestProductionReadinessScenariosDefinesCatalogCoverage(t *testing.T) {
	scenarios := ProductionReadinessScenarios()
	if len(scenarios) != 203 {
		t.Fatalf("scenario count = %d, want 203", len(scenarios))
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
	if liveGate != 103 {
		t.Fatalf("live gate scenario count = %d, want 103", liveGate)
	}
	if len(categories) < 10 {
		t.Fatalf("categories = %#v, want broad platform coverage", categories)
	}
}

func TestMatchClaimsDetectsContradictedEvidence(t *testing.T) {
	view := policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "Replacement decisions require review and never promise instant replacement before verification.",
			ResultHash:  "hash_contradiction",
			Citations:   []knowledge.Citation{{URI: "kb://contradiction"}},
		}}},
	}

	claims := ExtractClaims("You qualify for an instant replacement right away.", policy.QualityProfile{})
	matches := MatchClaims(view, claims)
	if len(matches) == 0 || matches[0].FailureReason != "contradicted_by_evidence" {
		t.Fatalf("matches = %#v, want contradicted_by_evidence", matches)
	}
}

func TestMatchClaimsUsesSemanticEvidenceConcepts(t *testing.T) {
	view := policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "The support policy says reimbursement and exchange eligibility are available only after order validation.",
			ResultHash:  "hash_semantic_support",
			Citations:   []knowledge.Citation{{URI: "kb://semantic-support"}},
		}}},
	}

	claims := ExtractClaims("Refund and replacement eligibility can be reviewed after verification.", policy.QualityProfile{})
	matches := MatchClaims(view, claims)
	if len(matches) == 0 || !matches[0].Supported {
		t.Fatalf("matches = %#v, want semantic support", matches)
	}
	if matches[0].MatchedSourceType != "retrieved_knowledge" {
		t.Fatalf("matches = %#v, want retrieved knowledge source", matches)
	}
}

func TestMatchClaimsDetectsSemanticContradiction(t *testing.T) {
	view := policyruntime.EngineResult{
		RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
			RetrieverID: "wiki",
			Data:        "The policy says exchange decisions require validation and must not promise immediate replacement.",
			ResultHash:  "hash_semantic_contradiction",
			Citations:   []knowledge.Citation{{URI: "kb://semantic-contradiction"}},
		}}},
	}

	claims := ExtractClaims("You qualify for an instant replacement right away.", policy.QualityProfile{})
	matches := MatchClaims(view, claims)
	if len(matches) == 0 || matches[0].FailureReason != "contradicted_by_evidence" {
		t.Fatalf("matches = %#v, want semantic contradiction", matches)
	}
}

func TestExtractClaimsUsesQualityProfileClaimProfiles(t *testing.T) {
	profile := policy.QualityProfile{
		ClaimProfiles: []policy.QualityClaimProfile{
			{
				ID:                   "tracking_update",
				MatchTerms:           []string{"tracking update", "shipping status"},
				Risk:                 "medium",
				RequiredEvidence:     []string{"retrieved_knowledge"},
				RequiredVerification: []string{"tracking_check"},
				AllowedCommitments:   []string{"status_only"},
			},
		},
	}
	claims := ExtractClaims("I can share a tracking update once the shipping status is checked.", profile)
	if len(claims) == 0 {
		t.Fatal("expected extracted claim")
	}
	if claims[0].Type != "tracking_update" || claims[0].Risk != "medium" {
		t.Fatalf("claims = %#v", claims)
	}
	if len(claims[0].RequiredEvidenceKinds) == 0 || claims[0].RequiredEvidenceKinds[0] != "retrieved_knowledge" {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestRefusalFindingsUsesQualityProfileSignals(t *testing.T) {
	view := policyruntime.EngineResult{
		QualityProfile: policy.QualityProfile{
			RefusalSignals: []string{"unsupported", "safe alternative"},
		},
		ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
			Action: "redirect",
			Reply:  "That request is unsupported. I can offer a safe alternative.",
		},
	}
	findings := refusalFindings(view, "That request is unsupported. I can offer a safe alternative.")
	if len(findings) != 0 {
		t.Fatalf("findings = %#v, want no refusal failures", findings)
	}
}

func TestProductionReadinessScenariosHaveDeterministicQualityCoverage(t *testing.T) {
	for _, scenario := range ProductionReadinessScenarios() {
		t.Run(scenario.ID, func(t *testing.T) {
			if strings.EqualFold(strings.TrimSpace(scenario.ExecutionMode), "platform_flow") {
				t.Skip("platform-flow scenarios are validated through end-to-end live platform tests, not deterministic fixtures")
			}
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
			if scenario.ExpectedLanguage != "" && scenario.Category == "multilingual" {
				switch scenario.ExpectedLanguage {
				case "id":
					if !looksIndonesian(response) {
						t.Fatalf("scenario %s response = %q, want Indonesian", scenario.ID, response)
					}
				case "en":
					if !looksEnglish(response) {
						t.Fatalf("scenario %s response = %q, want English", scenario.ID, response)
					}
				}
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
