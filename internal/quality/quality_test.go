package quality

import (
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
