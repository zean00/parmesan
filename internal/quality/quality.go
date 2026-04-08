package quality

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sahal/parmesan/internal/model"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

type Finding struct {
	Kind        string   `json:"kind"`
	Severity    string   `json:"severity"`
	Message     string   `json:"message"`
	EvidenceRef []string `json:"evidence_refs,omitempty"`
}

type DimensionScore struct {
	Name     string    `json:"name"`
	Score    float64   `json:"score"`
	Passed   bool      `json:"passed"`
	Severity string    `json:"severity,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
}

type Scorecard struct {
	Overall      float64                   `json:"overall"`
	Passed       bool                      `json:"passed"`
	HardFailed   bool                      `json:"hard_failed"`
	Dimensions   map[string]DimensionScore `json:"dimensions,omitempty"`
	HardFailures []Finding                 `json:"hard_failures,omitempty"`
	Warnings     []Finding                 `json:"warnings,omitempty"`
}

type ResponsePlan struct {
	RequiredFacts       []string `json:"required_facts,omitempty"`
	ForbiddenClaims     []string `json:"forbidden_claims,omitempty"`
	VerificationSteps   []string `json:"verification_steps,omitempty"`
	DesiredStructure    []string `json:"desired_structure,omitempty"`
	Language            string   `json:"language,omitempty"`
	StyleConstraints    []string `json:"style_constraints,omitempty"`
	PreferenceHints     []string `json:"preference_hints,omitempty"`
	ScopeClassification string   `json:"scope_classification,omitempty"`
	ScopeAction         string   `json:"scope_action,omitempty"`
	ScopeReply          string   `json:"scope_reply,omitempty"`
	Citations           []string `json:"citations,omitempty"`
}

var failureLabelDimensions = map[string]string{
	"answered_out_of_scope": "topic_scope_compliance",
	"hallucinated_policy":   "policy_adherence",
	"tone_mismatch":         "tone_persona",
	"missed_preference":     "customer_preference",
	"bad_language":          "multilingual_quality",
	"bad_refusal":           "refusal_escalation_quality",
	"bad_escalation":        "refusal_escalation_quality",
}

func FailureLabelDimensions(labels []string) map[string]string {
	out := map[string]string{}
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if dimension, ok := failureLabelDimensions[normalized]; ok {
			out[normalized] = dimension
		}
	}
	return out
}

func BuildResponsePlan(view policyruntime.EngineResult) ResponsePlan {
	var plan ResponsePlan
	plan.ScopeClassification = view.ScopeBoundaryStage.Classification
	plan.ScopeAction = view.ScopeBoundaryStage.Action
	plan.ScopeReply = strings.TrimSpace(view.ScopeBoundaryStage.Reply)
	if view.Bundle != nil {
		plan.Language = strings.TrimSpace(view.Bundle.Soul.DefaultLanguage)
		plan.StyleConstraints = append(plan.StyleConstraints, view.Bundle.Soul.StyleRules...)
		plan.StyleConstraints = append(plan.StyleConstraints, view.Bundle.Soul.AvoidRules...)
		plan.ForbiddenClaims = append(plan.ForbiddenClaims, view.Bundle.DomainBoundary.BlockedTopics...)
	}
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if text := strings.TrimSpace(guideline.Then); text != "" {
			plan.RequiredFacts = append(plan.RequiredFacts, text)
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		plan.VerificationSteps = append(plan.VerificationSteps, strings.TrimSpace(view.ActiveJourneyState.Instruction))
	}
	for _, pref := range view.CustomerPreferences {
		if strings.TrimSpace(pref.Key) != "" && strings.TrimSpace(pref.Value) != "" {
			plan.PreferenceHints = append(plan.PreferenceHints, strings.TrimSpace(pref.Key)+": "+strings.TrimSpace(pref.Value))
		}
	}
	for _, result := range view.RetrieverStage.Results {
		for _, citation := range result.Citations {
			if strings.TrimSpace(citation.URI) != "" {
				plan.Citations = append(plan.Citations, strings.TrimSpace(citation.URI))
			}
		}
	}
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		plan.DesiredStructure = append(plan.DesiredStructure, "Use the configured domain-boundary refusal or redirect response exactly.")
	}
	return plan
}

func FormatResponsePlan(plan ResponsePlan) string {
	raw, err := json.Marshal(plan)
	if err != nil || string(raw) == "{}" {
		return ""
	}
	return string(raw)
}

func Grade(view policyruntime.EngineResult, response string, toolOutput map[string]any) Scorecard {
	response = strings.TrimSpace(response)
	card := Scorecard{
		Passed:     true,
		Overall:    1,
		Dimensions: map[string]DimensionScore{},
	}
	addDimension := func(name string, score float64, findings []Finding) {
		passed := true
		severity := ""
		for _, finding := range findings {
			if finding.Severity == "hard" || finding.Severity == "high" {
				passed = false
				severity = finding.Severity
				card.HardFailures = append(card.HardFailures, finding)
			} else {
				card.Warnings = append(card.Warnings, finding)
				if severity == "" {
					severity = finding.Severity
				}
			}
		}
		if !passed {
			card.Passed = false
			card.HardFailed = true
		}
		card.Dimensions[name] = DimensionScore{Name: name, Score: score, Passed: passed, Severity: severity, Findings: findings}
		if score < card.Overall {
			card.Overall = score
		}
	}

	policy := policyFindings(view, response, toolOutput)
	scope := scopeFindings(view, response)
	journey := journeyFindings(view, response)
	grounding := groundingFindings(view, response)
	preferences := preferenceFindings(view, response)
	multilingual := multilingualFindings(view, response)
	refusal := refusalFindings(view, response)
	hallucination := hallucinationFindings(view, response)
	addDimension("policy_adherence", scoreForFindings(policy), policy)
	addDimension("topic_scope_compliance", scoreForFindings(scope), scope)
	addDimension("journey_adherence", scoreForFindings(journey), journey)
	addDimension("knowledge_grounding", scoreForFindings(grounding), grounding)
	addDimension("tone_persona", 1, nil)
	addDimension("customer_preference", scoreForFindings(preferences), preferences)
	addDimension("multilingual_quality", scoreForFindings(multilingual), multilingual)
	addDimension("refusal_escalation_quality", scoreForFindings(refusal), refusal)
	addDimension("hallucination_risk", scoreForFindings(hallucination), hallucination)
	return card
}

func GradeWithLLM(ctx context.Context, router *model.Router, view policyruntime.EngineResult, response string, toolOutput map[string]any) Scorecard {
	card := Grade(view, response, toolOutput)
	if router == nil || strings.TrimSpace(response) == "" {
		return card
	}
	var structured struct {
		TonePersona         float64  `json:"tone_persona"`
		MultilingualQuality float64  `json:"multilingual_quality"`
		RefusalQuality      float64  `json:"refusal_escalation_quality"`
		Warnings            []string `json:"warnings"`
	}
	prompt := "Return only JSON. Schema: {\"tone_persona\":1,\"multilingual_quality\":1,\"refusal_escalation_quality\":1,\"warnings\":[\"string\"]}\nGrade subjective response quality from 0 to 1.\nResponse: " + response + "\nPlan: " + FormatResponsePlan(BuildResponsePlan(view))
	resp, err := router.Generate(ctx, model.CapabilityStructured, model.Request{Prompt: prompt})
	if err != nil || strings.HasPrefix(strings.TrimSpace(resp.Text), "provider stub: ") {
		return card
	}
	if err := json.Unmarshal([]byte(extractJSONObject(resp.Text)), &structured); err != nil {
		return card
	}
	updateSoftDimension(&card, "tone_persona", structured.TonePersona, structured.Warnings)
	updateSoftDimension(&card, "multilingual_quality", structured.MultilingualQuality, structured.Warnings)
	updateSoftDimension(&card, "refusal_escalation_quality", structured.RefusalQuality, structured.Warnings)
	return card
}

func HardFailed(card Scorecard) bool {
	return card.HardFailed || len(card.HardFailures) > 0
}

func policyFindings(view policyruntime.EngineResult, response string, toolOutput map[string]any) []Finding {
	verification := policyruntime.VerifyDraft(view, response, toolOutput)
	if (verification.Status == "revise" || verification.Status == "block") && strings.TrimSpace(verification.Replacement) != "" && normalize(response) != normalize(verification.Replacement) {
		return []Finding{{Kind: "draft_verification_failed", Severity: "hard", Message: "Response did not satisfy deterministic policy verification.", EvidenceRef: verification.Reasons}}
	}
	return nil
}

func scopeFindings(view policyruntime.EngineResult, response string) []Finding {
	var findings []Finding
	boundary := view.ScopeBoundaryStage
	if shouldUseBoundaryReply(boundary) && normalize(response) != normalize(boundary.Reply) {
		findings = append(findings, Finding{Kind: "scope_boundary_reply_mismatch", Severity: "hard", Message: "Out-of-scope or redirected turn did not use the configured boundary reply.", EvidenceRef: boundary.Reasons})
	}
	if shouldUseBoundaryReply(boundary) && (len(view.RetrieverStage.Results) > 0 || len(view.ToolExposureStage.ExposedTools) > 0 || view.ToolDecisionStage.Decision.SelectedTool != "") {
		findings = append(findings, Finding{Kind: "scope_boundary_side_effect", Severity: "hard", Message: "Out-of-scope or redirected turn exposed retrievers or tools."})
	}
	if view.Bundle != nil && shouldUseBoundaryReply(boundary) && normalize(response) != normalize(boundary.Reply) {
		lower := strings.ToLower(response)
		for _, topic := range view.Bundle.DomainBoundary.BlockedTopics {
			if strings.TrimSpace(topic) != "" && strings.Contains(lower, strings.ToLower(topic)) {
				findings = append(findings, Finding{Kind: "answered_blocked_topic", Severity: "hard", Message: "Response appears to answer a blocked topic.", EvidenceRef: []string{topic}})
				break
			}
		}
	}
	return findings
}

func journeyFindings(view policyruntime.EngineResult, response string) []Finding {
	if view.ActiveJourneyState == nil || strings.TrimSpace(view.ActiveJourneyState.Instruction) == "" || shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return nil
	}
	instruction := strings.TrimSpace(view.ActiveJourneyState.Instruction)
	if strings.EqualFold(view.CompositionMode, "strict") && normalize(response) != normalize(instruction) {
		return []Finding{{Kind: "strict_journey_instruction_mismatch", Severity: "hard", Message: "Strict journey response did not match the active journey instruction.", EvidenceRef: []string{view.ActiveJourneyState.ID}}}
	}
	return nil
}

func groundingFindings(view policyruntime.EngineResult, response string) []Finding {
	lower := strings.ToLower(response)
	var findings []Finding
	if strings.Contains(lower, "according to") && len(view.RetrieverStage.Results) == 0 {
		findings = append(findings, Finding{Kind: "unsupported_grounding_phrase", Severity: "medium", Message: "Response uses grounding language without retrieved knowledge."})
	}
	for _, phrase := range []string{"within 30 days", "instant replacement", "guarantee", "guaranteed"} {
		if strings.Contains(lower, phrase) && !containsText(view, phrase) {
			findings = append(findings, Finding{Kind: "unsupported_specific_claim", Severity: "high", Message: "Response contains a specific claim not supported by retrieved knowledge or matched policy.", EvidenceRef: []string{phrase}})
		}
	}
	if len(view.RetrieverStage.Results) > 0 && strings.Contains(lower, "according to") && !responseMentionsCitation(view, response) {
		findings = append(findings, Finding{Kind: "missing_citation_reference", Severity: "medium", Message: "Response uses retrieved-knowledge framing without referencing an available citation."})
	}
	return findings
}

func preferenceFindings(view policyruntime.EngineResult, response string) []Finding {
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return nil
	}
	lower := strings.ToLower(response)
	var findings []Finding
	for _, pref := range view.CustomerPreferences {
		if strings.EqualFold(pref.Key, "preferred_name") && strings.TrimSpace(pref.Value) != "" && !strings.Contains(lower, strings.ToLower(pref.Value)) {
			findings = append(findings, Finding{Kind: "missed_preferred_name", Severity: "medium", Message: "Response did not reflect the customer's preferred name.", EvidenceRef: []string{pref.ID}})
		}
	}
	return findings
}

func refusalFindings(view policyruntime.EngineResult, response string) []Finding {
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) && strings.TrimSpace(response) == "" {
		return []Finding{{Kind: "empty_refusal", Severity: "hard", Message: "Boundary refusal or redirect response was empty."}}
	}
	return nil
}

func hallucinationFindings(view policyruntime.EngineResult, response string) []Finding {
	lower := strings.ToLower(response)
	if strings.Contains(lower, "guarantee") && strings.Contains(lower, "refund") && !containsText(view, "guarantee") {
		return []Finding{{Kind: "unsupported_guarantee", Severity: "high", Message: "Response appears to make an unsupported guarantee."}}
	}
	return nil
}

func multilingualFindings(view policyruntime.EngineResult, response string) []Finding {
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return nil
	}
	for _, pref := range view.CustomerPreferences {
		if !strings.EqualFold(pref.Key, "preferred_language") || !strings.Contains(strings.ToLower(pref.Value), "indonesian") {
			continue
		}
		if !looksIndonesian(response) {
			return []Finding{{Kind: "missed_language_preference", Severity: "medium", Message: "Response does not appear to follow the customer's Indonesian language preference.", EvidenceRef: []string{pref.ID}}}
		}
	}
	if view.Bundle != nil && strings.EqualFold(view.Bundle.Soul.DefaultLanguage, "id") && !looksIndonesian(response) {
		return []Finding{{Kind: "missed_default_language", Severity: "medium", Message: "Response does not appear to follow the agent's Indonesian default language."}}
	}
	return nil
}

func scoreForFindings(findings []Finding) float64 {
	score := 1.0
	for _, finding := range findings {
		switch finding.Severity {
		case "hard", "high":
			return 0
		case "medium":
			if score > 0.7 {
				score = 0.7
			}
		case "low":
			if score > 0.85 {
				score = 0.85
			}
		}
	}
	return score
}

func shouldUseBoundaryReply(boundary policyruntime.ScopeBoundaryStageResult) bool {
	switch boundary.Action {
	case "refuse", "redirect", "escalate":
		return strings.TrimSpace(boundary.Reply) != ""
	default:
		return false
	}
}

func updateSoftDimension(card *Scorecard, name string, score float64, warnings []string) {
	if score <= 0 || score > 1 {
		return
	}
	var findings []Finding
	for _, warning := range warnings {
		if strings.TrimSpace(warning) != "" {
			findings = append(findings, Finding{Kind: "llm_quality_warning", Severity: "low", Message: strings.TrimSpace(warning)})
		}
	}
	card.Dimensions[name] = DimensionScore{Name: name, Score: score, Passed: true, Severity: "low", Findings: findings}
	if score < card.Overall {
		card.Overall = score
	}
	card.Warnings = append(card.Warnings, findings...)
}

func containsText(view policyruntime.EngineResult, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, result := range view.RetrieverStage.Results {
		if strings.Contains(strings.ToLower(result.Data), needle) {
			return true
		}
	}
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if strings.Contains(strings.ToLower(guideline.Then), needle) {
			return true
		}
	}
	return false
}

func responseMentionsCitation(view policyruntime.EngineResult, response string) bool {
	lower := strings.ToLower(response)
	for _, result := range view.RetrieverStage.Results {
		for _, citation := range result.Citations {
			for _, value := range []string{citation.URI, citation.Title, citation.SourceID, citation.Anchor} {
				value = strings.TrimSpace(value)
				if value != "" && strings.Contains(lower, strings.ToLower(value)) {
					return true
				}
			}
		}
	}
	return false
}

func looksIndonesian(response string) bool {
	lower := strings.ToLower(response)
	markers := []string{"saya", "anda", "kami", "bisa", "membantu", "terima kasih", "silakan", "pesanan", "pertanyaan", "pilihan", "untuk", "dengan", "tidak"}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			hits++
		}
	}
	return hits >= 2
}

func normalize(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return raw[start : end+1]
}

func ResponseFromView(view policyruntime.EngineResult) string {
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return strings.TrimSpace(view.ScopeBoundaryStage.Reply)
	}
	if strings.TrimSpace(view.ResponseAnalysisStage.Analysis.RecommendedTemplate) != "" {
		return strings.TrimSpace(view.ResponseAnalysisStage.Analysis.RecommendedTemplate)
	}
	if strings.EqualFold(view.CompositionMode, "strict") && len(view.ResponseAnalysisStage.CandidateTemplates) > 0 {
		return strings.TrimSpace(view.ResponseAnalysisStage.CandidateTemplates[0].Text)
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		return strings.TrimSpace(view.ActiveJourneyState.Instruction)
	}
	var parts []string
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if strings.TrimSpace(guideline.Then) != "" {
			parts = append(parts, strings.TrimSpace(guideline.Then))
		}
	}
	return strings.Join(parts, " ")
}
