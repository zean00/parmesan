package runner

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func TestRenderResponsePrefersMatchedGuidelineInstructionOverJourneyPrompt(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{
				{
					ID:   "under_21",
					Then: "inform the customer that only economy class is available",
				},
			},
		},
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "ask_origin",
			Instruction: "From where are you looking to fly?",
		},
	}

	got := renderResponse(view, nil)
	want := "inform the customer that only economy class is available"
	if got != want {
		t.Fatalf("renderResponse() = %q, want %q", got, want)
	}
}

func TestRenderResponseUsesJourneyInstructionInStrictModeWithoutTemplate(t *testing.T) {
	view := resolvedView{
		CompositionMode: "strict",
		ActiveJourneyState: &policy.JourneyNode{
			ID:          "ask_origin",
			Instruction: "From where are you looking to fly?",
		},
	}

	got := renderResponse(view, nil)
	want := "From where are you looking to fly?"
	if got != want {
		t.Fatalf("renderResponse() = %q, want %q", got, want)
	}
}

func TestRenderResponseEmitsTemplateMessageSequence(t *testing.T) {
	view := resolvedView{
		CompositionMode: "strict",
		ResponseAnalysisStage: policyruntime.ResponseAnalysisStageResult{
			CandidateTemplates: []policy.Template{{
				ID:   "handoff_sequence",
				Mode: "strict",
				Messages: []string{
					"I can help with that.",
					"First, please share your order number.",
				},
			}},
		},
	}

	got := renderResponseMessages(view, nil)
	want := []string{"I can help with that.", "First, please share your order number."}
	if len(got) != len(want) {
		t.Fatalf("messages len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderResponsePrefersDelegatedAgentResultOverPolicyText(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		ResponseAnalysisStage: policyruntime.ResponseAnalysisStageResult{
			CandidateTemplates: []policy.Template{{
				ID:   "delegate_template",
				Text: "delegate the workflow",
			}},
		},
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "delegate",
				Then: "delegate the workflow",
			}},
		},
	}

	got := renderResponseMessages(view, map[string]any{
		"delegated_agent": map[string]any{
			"status":      "completed",
			"result_text": "The delegated answer should reach the customer.",
		},
	})
	want := []string{"The delegated answer should reach the customer."}
	if len(got) != len(want) {
		t.Fatalf("messages len = %d, want %d (%#v)", len(got), len(want), got)
	}
	if got[0] != want[0] {
		t.Fatalf("message[0] = %q, want %q", got[0], want[0])
	}
}
