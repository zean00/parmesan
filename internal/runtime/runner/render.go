package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func renderResponse(view resolvedView, toolOutput map[string]any) string {
	analysis := view.ResponseAnalysisStage.Analysis
	if view.DisambiguationPrompt != "" {
		return view.DisambiguationPrompt
	}
	if rendered := renderTemplateText(analysis.RecommendedTemplate, toolOutput); rendered != "" {
		return rendered
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		if rendered := renderTemplate(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); rendered != "" {
			return rendered
		}
		if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
			return view.ActiveJourneyState.Instruction
		}
		return strictNoMatchText(view.NoMatch)
	}
	if rendered := renderTemplate(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); rendered != "" {
		return rendered
	}
	guidelines := view.MatchFinalizeStage.MatchedGuidelines
	if len(guidelines) > 0 {
		parts := make([]string, 0, len(guidelines))
		for _, item := range guidelines {
			if strings.TrimSpace(item.Then) != "" {
				parts = append(parts, item.Then)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		return view.ActiveJourneyState.Instruction
	}
	if len(toolOutput) > 0 {
		raw, _ := json.Marshal(toolOutput)
		return fmt.Sprintf("I checked the tool result: %s", string(raw))
	}
	return ""
}

func renderTemplate(templates []policy.Template, toolOutput map[string]any) string {
	if len(templates) == 0 {
		return ""
	}
	out := templates[0].Text
	return renderTemplateText(out, toolOutput)
}

func renderTemplateText(text string, toolOutput map[string]any) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	out := text
	for key, value := range toolOutput {
		out = strings.ReplaceAll(out, "{{"+key+"}}", fmt.Sprint(value))
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return ""
	}
	return strings.TrimSpace(out)
}

func strictNoMatchText(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}
	return "Not sure I understand. Could you please say that another way?"
}
