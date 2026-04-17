package runner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
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
	if view.RetrieverStage.Outcome.GroundingRequired {
		return nil
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

func synthesizeToolBackedResponse(view resolvedView, toolOutput map[string]any) []string {
	capability, facts := responseCapabilityFacts(view, toolOutput)
	if capability != nil && len(facts) > 0 {
		if rendered := renderResponseCapabilityFallback(view, *capability, facts); len(rendered) > 0 {
			return rendered
		}
	}
	if tools := toolOutputs(toolOutput); len(tools) > 0 {
		return synthesizeGenericToolResponse(tools)
	}
	return nil
}

func toolOutputs(toolOutput map[string]any) map[string]any {
	if tools, ok := toolOutput["tools"].(map[string]any); ok {
		return tools
	}
	if typed, ok := toolOutput["tools"].(map[string]map[string]any); ok {
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	}
	if raw := normalizeSingleToolOutput(toolOutput); len(raw) > 0 {
		return raw
	}
	return nil
}

func normalizeSingleToolOutput(toolOutput map[string]any) map[string]any {
	toolID := strings.TrimSpace(stringValue(toolOutput["tool_id"]))
	if toolID == "" {
		return nil
	}
	normalized := map[string]any{}
	if output, ok := toolOutput["output"].(map[string]any); ok && len(output) > 0 {
		normalized[toolID] = output
		return normalized
	}
	if len(toolOutput) == 0 {
		return nil
	}
	copyMap := make(map[string]any, len(toolOutput))
	for key, value := range toolOutput {
		copyMap[key] = value
	}
	normalized[toolID] = copyMap
	return normalized
}

func responseCapabilityFacts(view resolvedView, toolOutput map[string]any) (*policy.ResponseCapability, map[string]any) {
	capability := selectedResponseCapability(view)
	if capability == nil {
		return nil, nil
	}
	tools := toolOutputs(toolOutput)
	if len(tools) == 0 {
		return capability, nil
	}
	facts := extractResponseCapabilityFacts(*capability, tools)
	if !responseCapabilityUsable(*capability, facts) {
		return capability, nil
	}
	return capability, facts
}

func selectedResponseCapability(view resolvedView) *policy.ResponseCapability {
	if view.Bundle == nil {
		return nil
	}
	capabilityID := strings.TrimSpace(view.ResponseAnalysisStage.Evaluation.ResponseCapabilityID)
	if capabilityID == "" {
		return nil
	}
	for i := range view.Bundle.ResponseCapabilities {
		item := &view.Bundle.ResponseCapabilities[i]
		if strings.TrimSpace(item.ID) == capabilityID {
			return item
		}
	}
	return nil
}

func selectedStyleProfile(view resolvedView) *policy.ResponseStyleProfile {
	if view.Bundle == nil {
		return nil
	}
	profileID := strings.TrimSpace(view.ResponseAnalysisStage.Evaluation.StyleProfileID)
	if profileID == "" {
		return nil
	}
	for i := range view.Bundle.ResponseStyleProfiles {
		item := &view.Bundle.ResponseStyleProfiles[i]
		if strings.TrimSpace(item.ID) == profileID {
			return item
		}
	}
	return nil
}

func extractResponseCapabilityFacts(capability policy.ResponseCapability, tools map[string]any) map[string]any {
	facts := map[string]any{}
	for _, fact := range capability.Facts {
		for _, source := range fact.Sources {
			toolOutput := responseToolOutput(tools, source.ToolID)
			if len(toolOutput) == 0 {
				continue
			}
			if value := delegatedLookup(toolOutput, source.Path); !isDelegatedEmpty(value) {
				facts[fact.Key] = value
				break
			}
		}
	}
	return facts
}

func responseCapabilityUsable(capability policy.ResponseCapability, facts map[string]any) bool {
	for _, fact := range capability.Facts {
		if fact.Required && isDelegatedEmpty(facts[fact.Key]) {
			return false
		}
	}
	return true
}

func responseToolOutput(tools map[string]any, toolID string) map[string]any {
	toolID = strings.TrimSpace(toolID)
	if toolID == "" {
		return nil
	}
	for key, raw := range tools {
		normalized := key
		if cut := strings.Index(normalized, "#"); cut >= 0 {
			normalized = normalized[:cut]
		}
		if strings.TrimSpace(normalized) != toolID {
			continue
		}
		output, _ := raw.(map[string]any)
		return output
	}
	return nil
}

func renderDeterministicResponseCapability(capability policy.ResponseCapability, facts map[string]any) []string {
	if len(capability.DeterministicFallback.Messages) == 0 {
		return nil
	}
	var messages []string
	for _, item := range capability.DeterministicFallback.Messages {
		if !allFactsPresent(facts, item.WhenPresent) {
			continue
		}
		rendered := item.Text
		for key, value := range facts {
			rendered = strings.ReplaceAll(rendered, "{{facts."+key+"}}", formatFactValue(value))
		}
		if strings.Contains(rendered, "{{facts.") {
			continue
		}
		if rendered = strings.TrimSpace(rendered); rendered != "" {
			messages = append(messages, rendered)
		}
	}
	if len(messages) > 0 {
		return messages
	}
	return nil
}

func renderResponseCapabilityFallback(view resolvedView, capability policy.ResponseCapability, facts map[string]any) []string {
	rendered := renderDeterministicResponseCapability(capability, facts)
	if len(rendered) == 0 {
		return nil
	}
	return rendered
}

func allFactsPresent(facts map[string]any, keys []string) bool {
	for _, key := range keys {
		if isDelegatedEmpty(facts[strings.TrimSpace(key)]) {
			return false
		}
	}
	return true
}

func synthesizeGenericToolResponse(tools map[string]any) []string {
	var keys []string
	for key := range tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		output, _ := tools[key].(map[string]any)
		content, _ := output["content"].([]any)
		for _, item := range content {
			if mapped, ok := item.(map[string]any); ok {
				if text := strings.TrimSpace(stringValue(mapped["text"])); text != "" {
					parts = append(parts, text)
					break
				}
			}
			if text := strings.TrimSpace(stringValue(item)); text != "" {
				parts = append(parts, text)
				break
			}
		}
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, " ")}
}

func buildResponseCapabilityPrompt(view resolvedView, events []session.Event, capability policy.ResponseCapability, facts map[string]any) string {
	parts := []string{
		"Customer message: " + latestText(events),
	}
	if prefs := customerPreferenceText(view.CustomerPreferences); prefs != "" {
		parts = append(parts, "Customer preferences (soft constraints):\n"+prefs)
	}
	if ctx := customerContextPromptText(view.CustomerContext, view.CustomerContextPromptSafeFields); ctx != "" {
		parts = append(parts, "Customer context:\n"+ctx)
	}
	if soul := soulPrompt(bundleSoul(view.Bundle)); soul != "" {
		parts = append(parts, "Agent SOUL style and brand rules:\n"+soul)
	}
	if style := styleProfilePrompt(selectedStyleProfile(view)); style != "" {
		parts = append(parts, "Active response style profile:\n"+style)
	}
	parts = append(parts, "Normalized facts:\n"+mustJSON(facts))
	if len(capability.Instructions) > 0 {
		parts = append(parts, "Response instructions:\n- "+strings.Join(capability.Instructions, "\n- "))
	}
	if len(capability.Examples) > 0 {
		var rendered []string
		for i, example := range capability.Examples {
			rendered = append(rendered, fmt.Sprintf("Example %d facts:\n%s\nExample %d messages:\n%s", i+1, mustJSON(example.Facts), i+1, strings.Join(example.Messages, "\n")))
		}
		parts = append(parts, strings.Join(rendered, "\n\n"))
	}
	parts = append(parts, `Use only the normalized facts provided. Do not invent missing facts. Return JSON only with this schema: {"messages":["first assistant message","optional follow-up message"]}.`)
	return strings.Join(parts, "\n\n")
}

func styleProfilePrompt(profile *policy.ResponseStyleProfile) string {
	if profile == nil {
		return ""
	}
	var parts []string
	if desc := strings.TrimSpace(profile.Description); desc != "" {
		parts = append(parts, "Description: "+desc)
	}
	if usage := strings.TrimSpace(profile.UsageContext); usage != "" {
		parts = append(parts, "Usage context: "+usage)
	}
	if summary := styleProfileSummary(*profile); summary != "" {
		parts = append(parts, "Structured guidance:\n"+summary)
	}
	if len(profile.Examples) > 0 {
		rendered := make([]string, 0, len(profile.Examples))
		for i, example := range profile.Examples {
			if len(example.Messages) == 0 {
				continue
			}
			rendered = append(rendered, fmt.Sprintf("Style example %d:\n%s", i+1, strings.Join(example.Messages, "\n")))
		}
		if len(rendered) > 0 {
			parts = append(parts, strings.Join(rendered, "\n\n"))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, "Apply this profile as local response-shape guidance. Do not let it override hard policy, moderation, strict templates, or explicit customer constraints.")
	return strings.Join(parts, "\n")
}

func styleProfileSummary(profile policy.ResponseStyleProfile) string {
	var lines []string
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	add("Tone formality", profile.Tone.Formality)
	add("Tone warmth", profile.Tone.Warmth)
	add("Tone directness", profile.Tone.Directness)
	add("Tone empathy", profile.Tone.Empathy)
	add("Verbosity overall", profile.Verbosity.Overall)
	add("Explanation depth", profile.Verbosity.ExplanationDepth)
	if profile.Structure.MaxMessages > 0 {
		lines = append(lines, fmt.Sprintf("Max messages: %d", profile.Structure.MaxMessages))
	}
	add("Paragraph style", profile.Structure.ParagraphStyle)
	add("Opening style", profile.Structure.OpeningStyle)
	add("Closing style", profile.Structure.ClosingStyle)
	if profile.Structure.PrefersLists {
		lines = append(lines, "Prefers lists: true")
	}
	if profile.Wording.Contractions {
		lines = append(lines, "Use contractions: true")
	}
	if profile.Wording.AvoidsJargon {
		lines = append(lines, "Avoid jargon: true")
	}
	add("Hedging level", profile.Wording.HedgingLevel)
	if profile.Interaction.AsksAtMostOneQuestion {
		lines = append(lines, "Ask at most one question: true")
	}
	if profile.Interaction.StatesLimitsExplicitly {
		lines = append(lines, "State limits explicitly: true")
	}
	if profile.Interaction.ConfirmsBeforeCommitment {
		lines = append(lines, "Confirm before commitment: true")
	}
	if profile.Grounding.CiteFactsExplicitly {
		lines = append(lines, "Cite facts explicitly: true")
	}
	if profile.Grounding.MentionMissingInfoPlainly {
		lines = append(lines, "Mention missing information plainly: true")
	}
	return strings.Join(lines, "\n")
}

func applyStyleProfileMessageLimit(messages []string, profile *policy.ResponseStyleProfile) []string {
	if profile == nil || profile.Structure.MaxMessages <= 0 || len(messages) <= profile.Structure.MaxMessages {
		return messages
	}
	return append([]string(nil), messages[:profile.Structure.MaxMessages]...)
}

func formatFactValue(value any) string {
	switch typed := value.(type) {
	case int:
		return formatInt(typed)
	case int64:
		return formatInt(int(typed))
	case float64:
		if typed == float64(int(typed)) {
			return formatInt(int(typed))
		}
		return strings.TrimSpace(fmt.Sprint(typed))
	case jsonNumberLike:
		text := strings.TrimSpace(typed.String())
		if parsed, err := strconv.Atoi(text); err == nil {
			return formatInt(parsed)
		}
		return text
	default:
		return stringValue(value)
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

type jsonNumberLike interface {
	String() string
}

func formatInt(value int) string {
	raw := strconv.Itoa(value)
	if value < 1000 {
		return raw
	}
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	return strings.Join(parts, ",")
}
