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
	Overall         float64                   `json:"overall"`
	Passed          bool                      `json:"passed"`
	HardFailed      bool                      `json:"hard_failed"`
	Dimensions      map[string]DimensionScore `json:"dimensions,omitempty"`
	HardFailures    []Finding                 `json:"hard_failures,omitempty"`
	Warnings        []Finding                 `json:"warnings,omitempty"`
	Claims          []ResponseClaim           `json:"claims,omitempty"`
	EvidenceMatches []EvidenceMatch           `json:"evidence_matches,omitempty"`
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

type ResponseClaim struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Risk       string   `json:"risk"`
	Indicators []string `json:"indicators,omitempty"`
}

type EvidenceMatch struct {
	Claim        ResponseClaim `json:"claim"`
	Supported    bool          `json:"supported"`
	Source       string        `json:"source,omitempty"`
	EvidenceRefs []string      `json:"evidence_refs,omitempty"`
	Severity     string        `json:"severity,omitempty"`
}

type ScenarioExpectation struct {
	ID              string   `json:"id"`
	Domain          string   `json:"domain"`
	Category        string   `json:"category"`
	Input           string   `json:"input"`
	ExpectedQuality []string `json:"expected_quality,omitempty"`
	Risk            string   `json:"risk,omitempty"`
	LiveGate        bool     `json:"live_gate,omitempty"`
}

var failureLabelDimensions = map[string]string{
	"answered_out_of_scope": "topic_scope_compliance",
	"hallucinated_policy":   "policy_adherence",
	"unsupported_claim":     "knowledge_grounding",
	"tone_mismatch":         "tone_persona",
	"missed_preference":     "customer_preference",
	"bad_language":          "multilingual_quality",
	"bad_refusal":           "refusal_escalation_quality",
	"bad_escalation":        "refusal_escalation_quality",
	"retrieval_miss":        "knowledge_grounding",
	"premature_commitment":  "policy_adherence",
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
	card.Claims = ExtractClaims(response)
	card.EvidenceMatches = MatchClaims(view, card.Claims)
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
	for _, match := range MatchClaims(view, ExtractClaims(response)) {
		if match.Supported || match.Severity == "" {
			continue
		}
		findings = append(findings, Finding{
			Kind:        "unsupported_claim",
			Severity:    match.Severity,
			Message:     "Response contains a claim not supported by retrieved knowledge, matched policy, preferences, or tool evidence.",
			EvidenceRef: append([]string{match.Claim.Text}, match.Claim.Indicators...),
		})
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

func ExtractClaims(response string) []ResponseClaim {
	var claims []ResponseClaim
	seen := map[string]struct{}{}
	for _, sentence := range splitSentences(response) {
		claim := claimFromSentence(sentence)
		if strings.TrimSpace(claim.Text) == "" {
			continue
		}
		key := normalize(claim.Text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		claim.ID = "claim_" + shortStableID(key)
		claims = append(claims, claim)
	}
	return claims
}

func MatchClaims(view policyruntime.EngineResult, claims []ResponseClaim) []EvidenceMatch {
	evidence := evidenceTexts(view)
	var matches []EvidenceMatch
	for _, claim := range claims {
		if claim.Risk == "" {
			matches = append(matches, EvidenceMatch{Claim: claim, Supported: true, Source: "low_risk"})
			continue
		}
		match := EvidenceMatch{Claim: claim, Severity: claim.Risk}
		for _, item := range evidence {
			if evidenceSupportsClaim(item.text, claim) {
				match.Supported = true
				match.Source = item.source
				match.EvidenceRefs = append(match.EvidenceRefs, item.ref)
				match.Severity = ""
				break
			}
		}
		matches = append(matches, match)
	}
	return matches
}

func splitSentences(response string) []string {
	return strings.FieldsFunc(response, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})
}

func claimFromSentence(sentence string) ResponseClaim {
	text := strings.TrimSpace(sentence)
	lower := strings.ToLower(text)
	var indicators []string
	risk := ""
	for _, phrase := range highRiskClaimIndicators() {
		if strings.Contains(lower, phrase) {
			indicators = append(indicators, phrase)
			risk = "high"
		}
	}
	if risk == "" && containsNumericSpecificity(lower) {
		risk = "medium"
	}
	return ResponseClaim{Text: text, Risk: risk, Indicators: indicators}
}

func highRiskClaimIndicators() []string {
	return []string{
		"within 30 days",
		"instant replacement",
		"guarantee",
		"guaranteed",
		"refund",
		"replacement",
		"approved",
		"eligible",
		"qualify",
		"qualifies",
	}
}

func containsNumericSpecificity(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return strings.Contains(value, "day") || strings.Contains(value, "hour") || strings.Contains(value, "percent")
}

type evidenceText struct {
	source string
	ref    string
	text   string
}

func evidenceTexts(view policyruntime.EngineResult) []evidenceText {
	var out []evidenceText
	for _, result := range view.RetrieverStage.Results {
		if strings.TrimSpace(result.Data) != "" {
			ref := result.ResultHash
			if ref == "" {
				ref = result.RetrieverID
			}
			out = append(out, evidenceText{source: "retrieved_knowledge", ref: ref, text: result.Data})
		}
	}
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if strings.TrimSpace(guideline.Then) != "" {
			out = append(out, evidenceText{source: "matched_guideline", ref: guideline.ID, text: guideline.Then})
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		out = append(out, evidenceText{source: "journey_state", ref: view.ActiveJourneyState.ID, text: view.ActiveJourneyState.Instruction})
	}
	for _, pref := range view.CustomerPreferences {
		if strings.TrimSpace(pref.Key) != "" || strings.TrimSpace(pref.Value) != "" {
			out = append(out, evidenceText{source: "customer_preference", ref: pref.ID, text: pref.Key + ": " + pref.Value})
		}
	}
	return out
}

func evidenceSupportsClaim(evidence string, claim ResponseClaim) bool {
	evidence = strings.ToLower(evidence)
	claimText := strings.ToLower(claim.Text)
	if strings.Contains(evidence, claimText) {
		return true
	}
	matched := 0
	for _, indicator := range claim.Indicators {
		if strings.Contains(evidence, indicator) {
			matched++
		}
	}
	if len(claim.Indicators) > 0 {
		return matched == len(claim.Indicators)
	}
	tokens := tokenSet(claimText)
	if len(tokens) == 0 {
		return true
	}
	hits := 0
	for token := range tokens {
		if strings.Contains(evidence, token) {
			hits++
		}
	}
	return hits >= 4 && hits*2 >= len(tokens)
}

func tokenSet(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if len(token) > 2 {
			out[token] = struct{}{}
		}
	}
	return out
}

func shortStableID(value string) string {
	value = normalize(value)
	if len(value) > 24 {
		value = value[:24]
	}
	value = strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_", ",", "").Replace(value)
	if value == "" {
		return "empty"
	}
	return value
}

func ProductionReadinessScenarios() []ScenarioExpectation {
	categories := []struct {
		domain   string
		category string
		inputs   []string
		quality  []string
		risk     string
		live     bool
	}{
		{"ecommerce", "knowledge_grounding", []string{"damaged toaster replacement eligibility", "refund timing question", "warranty article lookup", "shipping notification policy", "missing order number"}, []string{"knowledge_grounding", "policy_adherence"}, "high", true},
		{"ecommerce", "journey_adherence", []string{"damaged item return", "wrong item received", "late delivery", "cancel order", "exchange request"}, []string{"journey_adherence", "policy_adherence"}, "medium", false},
		{"pet_store", "topic_scope", []string{"pet food question", "human cooking question", "pet-safe ingredient question", "finance question", "Indonesian cooking request"}, []string{"topic_scope_compliance"}, "high", true},
		{"support", "preference", []string{"call me Rina", "prefer email", "prefer SMS", "be concise", "use formal tone"}, []string{"customer_preference"}, "medium", true},
		{"support", "multilingual", []string{"respond in Indonesian", "English fallback", "mixed Indonesian-English request", "language change mid-session", "unsupported language fallback"}, []string{"multilingual_quality"}, "medium", true},
		{"support", "refusal_escalation", []string{"unsafe request", "operator handoff", "policy missing", "uncertain scope", "blocked topic"}, []string{"refusal_escalation_quality", "topic_scope_compliance"}, "high", true},
		{"support", "retrieval_quality", []string{"noisy knowledge source", "empty retrieval", "irrelevant retrieval", "citation required", "overstuffed context"}, []string{"knowledge_grounding"}, "high", false},
		{"support", "tool_and_approval", []string{"approval required", "tool denied", "tool timeout", "manual takeover", "post-approval answer"}, []string{"policy_adherence"}, "high", false},
		{"support", "soul_persona", []string{"warm concise tone", "avoid over-apology", "brand voice", "handoff style", "tone conflict"}, []string{"tone_persona"}, "low", false},
		{"support", "failure_modes", []string{"ambiguous input", "provider timeout", "conflicting preference", "missing required info", "learning regression"}, []string{"policy_adherence", "hallucination_risk"}, "high", false},
	}
	var out []ScenarioExpectation
	for _, category := range categories {
		for i, input := range category.inputs {
			out = append(out, ScenarioExpectation{
				ID:              category.domain + "_" + category.category + "_" + shortStableID(input),
				Domain:          category.domain,
				Category:        category.category,
				Input:           input,
				ExpectedQuality: append([]string(nil), category.quality...),
				Risk:            category.risk,
				LiveGate:        category.live && i < 2,
			})
		}
	}
	return out
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
