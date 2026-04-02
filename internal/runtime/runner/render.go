package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func renderResponse(view resolvedView, toolOutput map[string]any) string {
	if view.DisambiguationPrompt != "" {
		return view.DisambiguationPrompt
	}
	if strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate) != "" {
		return strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate)
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		if rendered := renderTemplate(view.CandidateTemplates, toolOutput); rendered != "" {
			return rendered
		}
		if strings.TrimSpace(view.NoMatch) != "" {
			return view.NoMatch
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		return view.ActiveJourneyState.Instruction
	}
	if rendered := renderTemplate(view.CandidateTemplates, toolOutput); rendered != "" {
		return rendered
	}
	if len(view.MatchedGuidelines) > 0 {
		parts := make([]string, 0, len(view.MatchedGuidelines))
		for _, item := range view.MatchedGuidelines {
			if strings.TrimSpace(item.Then) != "" {
				parts = append(parts, item.Then)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
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
	for key, value := range toolOutput {
		out = strings.ReplaceAll(out, "{{"+key+"}}", fmt.Sprint(value))
	}
	return strings.TrimSpace(out)
}
