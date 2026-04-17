package runner

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
	policyruntime "github.com/sahal/parmesan/internal/engine/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
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

func TestRenderResponseUsesDelegatedTicketLifecycleStatusAsUsable(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "delegate",
				Then: "delegate the workflow",
			}},
		},
	}

	got := renderResponseMessages(view, map[string]any{
		"delegated_agent": map[string]any{
			"status":      "new",
			"result_text": "Your complaint ticket CRM-20260416121123.135 has been opened.",
		},
	})
	if len(got) != 1 || got[0] != "Your complaint ticket CRM-20260416121123.135 has been opened." {
		t.Fatalf("messages = %#v, want delegated response text", got)
	}
}

func TestRenderResponseDefersToComposerWhenToolOutputExists(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "product_lookup_and_lead",
				Then: "retrieve the product information from Orbyte and create or update the CRM lead for follow-up.",
			}},
		},
	}

	got := renderResponseMessages(view, map[string]any{
		"tools": map[string]any{
			"orbyte_full_commercial_core.business.records.search": map[string]any{
				"content": []map[string]any{{"text": "Found Espresso Double."}},
			},
		},
	})
	if got != nil {
		t.Fatalf("messages = %#v, want nil so compose_response uses tool outputs instead of guideline text", got)
	}
}

func TestRenderResponseDefersToComposerWhenRetrievedKnowledgeExists(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "grounded_answer",
				Then: "answer from retrieved knowledge, include the supporting source citation, and keep the answer factual.",
			}},
		},
		RetrieverStage: policyruntime.RetrieverStageResult{
			Outcome: policyruntime.RetrievalOutcome{
				Attempted:         true,
				State:             "evidence_available",
				HasUsableEvidence: true,
				GroundingRequired: true,
			},
			Results: []knowledgeretriever.Result{{
				RetrieverID: "validated_corpus",
				Data:        "Espresso machine article: pump-driven machines use an electric pump to provide brewing pressure.",
			}},
		},
	}

	got := renderResponseMessages(view, nil)
	if got != nil {
		t.Fatalf("messages = %#v, want nil so compose_response uses retrieved knowledge", got)
	}
}

func TestRenderResponseDefersToComposerWhenGroundedRetrievalMisses(t *testing.T) {
	view := resolvedView{
		CompositionMode: "guided",
		MatchFinalizeStage: policyruntime.FinalizeStageResult{
			MatchedGuidelines: []policy.Guideline{{
				ID:   "grounded_answer",
				Then: "answer from retrieved knowledge, include the supporting source citation, and keep the answer factual.",
			}},
		},
		RetrieverStage: policyruntime.RetrieverStageResult{
			Outcome: policyruntime.RetrievalOutcome{
				Attempted:         true,
				State:             "no_results",
				GroundingRequired: true,
			},
		},
	}

	got := renderResponseMessages(view, nil)
	if got != nil {
		t.Fatalf("messages = %#v, want nil so compose_response can return an honest grounded miss", got)
	}
}
