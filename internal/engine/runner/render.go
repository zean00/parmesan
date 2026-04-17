package runner

import (
	"fmt"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func renderResponse(view resolvedView, toolOutput map[string]any) string {
	messages := renderResponseMessages(view, toolOutput)
	if len(messages) == 0 {
		return ""
	}
	return strings.Join(messages, "\n\n")
}

func renderResponseMessages(view resolvedView, toolOutput map[string]any) []string {
	if reply := strings.TrimSpace(view.ScopeBoundaryStage.Reply); reply != "" && (view.ScopeBoundaryStage.Action == "refuse" || view.ScopeBoundaryStage.Action == "redirect") {
		return []string{reply}
	}
	analysis := view.ResponseAnalysisStage.Analysis
	if view.DisambiguationPrompt != "" {
		return []string{view.DisambiguationPrompt}
	}
	if text := delegatedAgentResultText(toolOutput); text != "" {
		return []string{text}
	}
	if rendered := renderTemplateText(analysis.RecommendedTemplate, toolOutput); rendered != "" {
		return []string{rendered}
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		if rendered := renderTemplateMessages(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); len(rendered) > 0 {
			return rendered
		}
		if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
			return []string{view.ActiveJourneyState.Instruction}
		}
		return []string{strictNoMatchText(view.NoMatch)}
	}
	if rendered := renderTemplateMessages(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); len(rendered) > 0 {
		return rendered
	}
	if len(toolOutput) > 0 {
		return nil
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
			return []string{strings.Join(parts, " ")}
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		return []string{view.ActiveJourneyState.Instruction}
	}
	return nil
}

func delegatedAgentResultText(toolOutput map[string]any) string {
	delegated, _ := toolOutput["delegated_agent"].(map[string]any)
	if !delegatedAgentUsable(delegated) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(delegated["result_text"]))
}

func renderTemplate(templates []policy.Template, toolOutput map[string]any) string {
	return strings.Join(renderTemplateMessages(templates, toolOutput), "\n\n")
}

func renderTemplateMessages(templates []policy.Template, toolOutput map[string]any) []string {
	if len(templates) == 0 {
		return nil
	}
	template := templates[0]
	if len(template.Messages) > 0 {
		out := make([]string, 0, len(template.Messages))
		for _, message := range template.Messages {
			if rendered := renderTemplateText(message, toolOutput); rendered != "" {
				out = append(out, rendered)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if rendered := renderTemplateText(template.Text, toolOutput); rendered != "" {
		return []string{rendered}
	}
	return nil
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
