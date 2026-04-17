package runner

import (
	"strings"
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

func TestSynthesizeToolBackedResponseUsesResponseCapabilityFallback(t *testing.T) {
	view := resolvedView{
		Bundle: &policy.Bundle{
			ResponseCapabilities: []policy.ResponseCapability{{
				ID:   "product_lookup_and_lead_response",
				Mode: "always",
				Facts: []policy.ResponseFact{
					{Key: "product_name", Required: true, Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.name"}}},
					{Key: "item_type", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.item_type"}}},
					{Key: "unit_price", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.record.values.unit_price"}}},
					{Key: "status", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.status"}}},
					{Key: "inventory_tracking", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.inventory_tracking_mode"}}},
					{Key: "replenishment_mode", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.commercial_core.item.get", Path: "structuredContent.replenishment_mode"}}},
					{Key: "lead_number", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.crm.lead.find_or_create_for_product_interest", Path: "structuredContent.lead.values.lead_number"}}},
					{Key: "customer_name", Sources: []policy.ResponseFactSource{{ToolID: "orbyte_full.crm.lead.find_or_create_for_product_interest", Path: "structuredContent.lead.values.party_name"}}},
				},
				DeterministicFallback: policy.ResponseDeterministicFallback{
					Messages: []policy.ResponseDeterministicMessage{
						{Text: "{{facts.product_name}} is type {{facts.item_type}}, price {{facts.unit_price}}, status {{facts.status}}.", WhenPresent: []string{"product_name", "item_type", "unit_price", "status"}},
						{Text: "For a compact counter setup, it uses inventory tracking {{facts.inventory_tracking}} and replenishment {{facts.replenishment_mode}}.", WhenPresent: []string{"inventory_tracking", "replenishment_mode"}},
						{Text: "I also created sales follow-up lead {{facts.lead_number}} for {{facts.customer_name}}.", WhenPresent: []string{"lead_number", "customer_name"}},
					},
				},
			}},
		},
		ResponseAnalysisStage: policyruntime.ResponseAnalysisStageResult{
			Evaluation: policyruntime.ResponseAnalysisEvaluation{
				ResponseCapabilityID: "product_lookup_and_lead_response",
			},
		},
	}
	got := synthesizeToolBackedResponse(view, map[string]any{
		"tools": map[string]any{
			"orbyte_full.commercial_core.item.get#item": map[string]any{
				"structuredContent": map[string]any{
					"name":                    "Espresso Double",
					"item_type":               "product",
					"description":             "Repeat breakfast beverage",
					"inventory_tracking_mode": "none",
					"replenishment_mode":      "manual",
					"status":                  "active",
					"record": map[string]any{
						"values": map[string]any{
							"unit_price": 28000,
						},
					},
				},
			},
			"orbyte_full.crm.lead.find_or_create_for_product_interest#lead": map[string]any{
				"structuredContent": map[string]any{
					"action": "created",
					"lead": map[string]any{
						"values": map[string]any{
							"lead_number":  "LEAD-20260417121117.221",
							"party_name":   "CRM Demo Customer",
							"product_name": "Espresso Double",
						},
					},
				},
			},
		},
	})
	if len(got) != 3 {
		t.Fatalf("messages len = %d, want 3 (%#v)", len(got), got)
	}
	for _, want := range []string{
		"Espresso Double is type product, price 28,000, status active.",
		"compact counter setup",
		"inventory tracking none",
		"replenishment manual",
		"LEAD-20260417121117.221",
	} {
		found := false
		for _, item := range got {
			if strings.Contains(item, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("messages = %#v, want substring %q", got, want)
		}
	}
}

func TestSynthesizeToolBackedResponseWithoutCapabilityFallsBackToToolContent(t *testing.T) {
	got := synthesizeToolBackedResponse(resolvedView{}, map[string]any{
		"tools": map[string]any{
			"provider.tool#a": map[string]any{
				"content": []any{map[string]any{"text": "First tool summary."}},
			},
			"provider.tool#b": map[string]any{
				"content": []any{map[string]any{"text": "Second tool summary."}},
			},
		},
	})
	if len(got) != 1 || got[0] != "First tool summary. Second tool summary." {
		t.Fatalf("messages = %#v, want combined generic tool summary without a configured response capability", got)
	}
}

func TestSynthesizeToolBackedResponseSupportsSingleToolOutputShape(t *testing.T) {
	view := resolvedView{
		Bundle: &policy.Bundle{
			ResponseCapabilities: []policy.ResponseCapability{{
				ID: "single_tool_response",
				Facts: []policy.ResponseFact{
					{Key: "summary", Required: true, Sources: []policy.ResponseFactSource{{ToolID: "provider.tool", Path: "content_text"}}},
				},
				DeterministicFallback: policy.ResponseDeterministicFallback{
					Messages: []policy.ResponseDeterministicMessage{{Text: "{{facts.summary}}", WhenPresent: []string{"summary"}}},
				},
			}},
		},
		ResponseAnalysisStage: policyruntime.ResponseAnalysisStageResult{
			Evaluation: policyruntime.ResponseAnalysisEvaluation{ResponseCapabilityID: "single_tool_response"},
		},
	}
	got := synthesizeToolBackedResponse(view, map[string]any{
		"tool_id": "provider.tool#a",
		"output": map[string]any{
			"content_text": "Single tool summary.",
		},
	})
	if len(got) != 1 || got[0] != "Single tool summary." {
		t.Fatalf("messages = %#v, want synthesized output from single-tool shape", got)
	}
}
