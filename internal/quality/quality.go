package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
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
	RequiredFacts             []string `json:"required_facts,omitempty"`
	ForbiddenClaims           []string `json:"forbidden_claims,omitempty"`
	VerificationSteps         []string `json:"verification_steps,omitempty"`
	RequiredVerificationSteps []string `json:"required_verification_steps,omitempty"`
	DesiredStructure          []string `json:"desired_structure,omitempty"`
	Language                  string   `json:"language,omitempty"`
	RiskTier                  string   `json:"risk_tier,omitempty"`
	AllowedCommitments        []string `json:"allowed_commitments,omitempty"`
	RequiredEvidence          []string `json:"required_evidence,omitempty"`
	RequiredEvidenceClasses   []string `json:"required_evidence_classes,omitempty"`
	RetrievalRequired         bool     `json:"retrieval_required,omitempty"`
	ExpectedRefusalMode       string   `json:"expected_refusal_mode,omitempty"`
	StyleConstraints          []string `json:"style_constraints,omitempty"`
	PreferenceHints           []string `json:"preference_hints,omitempty"`
	ScopeClassification       string   `json:"scope_classification,omitempty"`
	ScopeAction               string   `json:"scope_action,omitempty"`
	ScopeReply                string   `json:"scope_reply,omitempty"`
	Citations                 []string `json:"citations,omitempty"`
}

type ResponseClaim struct {
	ID                    string   `json:"id"`
	Text                  string   `json:"text"`
	Type                  string   `json:"type,omitempty"`
	Risk                  string   `json:"risk"`
	Indicators            []string `json:"indicators,omitempty"`
	RequiredEvidenceKinds []string `json:"required_evidence_kinds,omitempty"`
	RequiredVerification  []string `json:"required_verification,omitempty"`
	AllowedCommitments    []string `json:"allowed_commitments,omitempty"`
}

type EvidenceMatch struct {
	Claim                 ResponseClaim `json:"claim"`
	ClaimType             string        `json:"claim_type,omitempty"`
	Supported             bool          `json:"supported"`
	Source                string        `json:"source,omitempty"`
	MatchedSourceType     string        `json:"matched_source_type,omitempty"`
	RequiredEvidenceKinds []string      `json:"required_evidence_kinds,omitempty"`
	EvidenceRefs          []string      `json:"evidence_refs,omitempty"`
	MatchedRefs           []string      `json:"matched_refs,omitempty"`
	Severity              string        `json:"severity,omitempty"`
	FailureReason         string        `json:"failure_reason,omitempty"`
}

type ScenarioExpectation struct {
	ID                        string   `json:"id"`
	Domain                    string   `json:"domain"`
	Category                  string   `json:"category"`
	Input                     string   `json:"input"`
	ExpectedQuality           []string `json:"expected_quality,omitempty"`
	Risk                      string   `json:"risk,omitempty"`
	RiskTier                  string   `json:"risk_tier,omitempty"`
	ExpectedScope             string   `json:"expected_scope,omitempty"`
	ExpectedLanguage          string   `json:"expected_language,omitempty"`
	RequiredClaims            []string `json:"required_claims,omitempty"`
	ForbiddenClaims           []string `json:"forbidden_claims,omitempty"`
	RequiredEvidenceKinds     []string `json:"required_evidence_kinds,omitempty"`
	RequiredEvidenceClasses   []string `json:"required_evidence_classes,omitempty"`
	RequiredVerificationSteps []string `json:"required_verification_steps,omitempty"`
	AllowedCommitments        []string `json:"allowed_commitments,omitempty"`
	ExpectedRetrievalRequired bool     `json:"expected_retrieval_required,omitempty"`
	ExpectedRefusalMode       string   `json:"expected_refusal_mode,omitempty"`
	MinimumOverall            float64  `json:"minimum_overall,omitempty"`
	LiveGate                  bool     `json:"live_gate,omitempty"`
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

var allowedQualityDimensions = map[string]struct{}{
	"policy_adherence":           {},
	"topic_scope_compliance":     {},
	"journey_adherence":          {},
	"knowledge_grounding":        {},
	"retrieval_quality":          {},
	"tone_persona":               {},
	"customer_preference":        {},
	"multilingual_quality":       {},
	"refusal_escalation_quality": {},
	"hallucination_risk":         {},
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
	plan.RiskTier = inferRiskTier(view)
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
	plan.RequiredEvidence = requiredEvidenceKindsForView(view)
	plan.RequiredEvidenceClasses = append([]string(nil), plan.RequiredEvidence...)
	plan.AllowedCommitments = allowedCommitmentsForView(view)
	plan.RequiredVerificationSteps = append([]string(nil), plan.VerificationSteps...)
	plan.RetrievalRequired = len(view.RetrieverStage.Results) > 0 || containsString(plan.RequiredEvidence, []string{"retrieved_knowledge"})
	plan.ExpectedRefusalMode = view.ScopeBoundaryStage.Action
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		plan.DesiredStructure = append(plan.DesiredStructure, "Use the configured domain-boundary refusal or redirect response exactly.")
	}
	plan.DesiredStructure = append(plan.DesiredStructure, highRiskBlueprintForView(view, plan)...)
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
	retrieval := retrievalFindings(view, response, card.EvidenceMatches)
	preferences := preferenceFindings(view, response)
	multilingual := multilingualFindings(view, response)
	refusal := refusalFindings(view, response)
	hallucination := hallucinationFindings(view, response)
	addDimension("policy_adherence", scoreForFindings(policy), policy)
	addDimension("topic_scope_compliance", scoreForFindings(scope), scope)
	addDimension("journey_adherence", scoreForFindings(journey), journey)
	addDimension("knowledge_grounding", scoreForFindings(grounding), grounding)
	addDimension("retrieval_quality", scoreForFindings(retrieval), retrieval)
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
	var findings []Finding
	verification := policyruntime.VerifyDraft(view, response, toolOutput)
	if (verification.Status == "revise" || verification.Status == "block") && strings.TrimSpace(verification.Replacement) != "" && normalize(response) != normalize(verification.Replacement) {
		findings = append(findings, Finding{Kind: "draft_verification_failed", Severity: "hard", Message: "Response did not satisfy deterministic policy verification.", EvidenceRef: verification.Reasons})
	}
	findings = append(findings, prematureCommitmentFindings(view, response)...)
	return findings
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
	if !shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		for _, forbidden := range BuildResponsePlan(view).ForbiddenClaims {
			forbidden = strings.TrimSpace(forbidden)
			if forbidden != "" && strings.Contains(lower, strings.ToLower(forbidden)) {
				findings = append(findings, Finding{Kind: "forbidden_claim_answered", Severity: "hard", Message: "Response appears to include a forbidden or blocked claim.", EvidenceRef: []string{forbidden}})
				break
			}
		}
	}
	return findings
}

func retrievalFindings(view policyruntime.EngineResult, response string, matches []EvidenceMatch) []Finding {
	var findings []Finding
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return nil
	}
	plan := BuildResponsePlan(view)
	hasRetrievedSupport := false
	for _, match := range matches {
		if match.Supported && match.Source == "retrieved_knowledge" {
			hasRetrievedSupport = true
			break
		}
	}
	totalRetrieved := 0
	hasCitations := false
	for _, result := range view.RetrieverStage.Results {
		totalRetrieved += len(strings.TrimSpace(result.Data))
		hasCitations = hasCitations || len(result.Citations) > 0
	}
	lower := strings.ToLower(response)
	if plan.RetrievalRequired && len(view.RetrieverStage.Results) == 0 {
		findings = append(findings, Finding{Kind: "retrieval_required_missing", Severity: "hard", Message: "Response required retrieved evidence but no retrieval results were available."})
	}
	if len(view.RetrieverStage.Results) == 0 && strings.Contains(lower, "according to") {
		severity := "medium"
		if containsAny(lower, []string{"refund", "replacement", "qualify", "eligible", "approved"}) {
			severity = "hard"
		}
		findings = append(findings, Finding{Kind: "missing_required_retrieval", Severity: severity, Message: "Response uses retrieval framing without any retrieved knowledge."})
	}
	if len(view.RetrieverStage.Results) > 0 && hasRetrievedSupport && !responseMentionsCitation(view, response) {
		findings = append(findings, Finding{Kind: "retrieval_citation_missing", Severity: "medium", Message: "Response used retrieved knowledge without surfacing an available citation."})
	}
	if len(view.RetrieverStage.Results) > 0 && !hasCitations {
		findings = append(findings, Finding{Kind: "retrieval_missing_citations", Severity: "medium", Message: "Retrieved knowledge did not include any citations."})
	}
	if totalRetrieved > 1500 && !hasRetrievedSupport {
		findings = append(findings, Finding{Kind: "retrieval_noise_unused", Severity: "medium", Message: "Large retrieved context was included but not used to support the response."})
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

func prematureCommitmentFindings(view policyruntime.EngineResult, response string) []Finding {
	plan := BuildResponsePlan(view)
	if len(plan.VerificationSteps) == 0 {
		return nil
	}
	lower := strings.ToLower(response)
	if !containsAny(lower, []string{"refund", "replacement", "eligible", "approval", "approved", "qualify"}) {
		return nil
	}
	if containsAny(lower, []string{"need approval", "need review", "requires approval", "await approval", "before changing", "before i review"}) {
		return nil
	}
	if containsAny(lower, []string{"after verification", "once verified", "after review", "pending review", "after approval", "once approved"}) {
		return nil
	}
	return []Finding{{
		Kind:        "premature_commitment",
		Severity:    "hard",
		Message:     "Response makes a high-risk commitment before the required verification or review step is reflected in the answer.",
		EvidenceRef: append([]string(nil), plan.VerificationSteps...),
	}}
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

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		needle = strings.TrimSpace(strings.ToLower(needle))
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
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
			matches = append(matches, EvidenceMatch{Claim: claim, ClaimType: claim.Type, Supported: true, Source: "low_risk"})
			continue
		}
		match := EvidenceMatch{
			Claim:                 claim,
			ClaimType:             claim.Type,
			Severity:              claim.Risk,
			RequiredEvidenceKinds: append([]string(nil), claim.RequiredEvidenceKinds...),
		}
		for _, item := range evidence {
			if len(claim.RequiredEvidenceKinds) > 0 && !containsString(item.supports, claim.RequiredEvidenceKinds) {
				continue
			}
			if evidenceSupportsClaim(item.text, claim) {
				match.Supported = true
				match.Source = item.source
				match.MatchedSourceType = item.source
				match.EvidenceRefs = append(match.EvidenceRefs, item.ref)
				match.MatchedRefs = append(match.MatchedRefs, item.ref)
				match.Severity = ""
				match.FailureReason = ""
				break
			}
		}
		if !match.Supported {
			match.FailureReason = "no_supported_evidence"
			for _, item := range evidence {
				if len(claim.RequiredEvidenceKinds) > 0 && !containsString(item.supports, claim.RequiredEvidenceKinds) {
					continue
				}
				if evidenceContradictsClaim(strings.ToLower(item.text), claim) {
					match.FailureReason = "contradicted_by_evidence"
					match.Source = item.source
					match.MatchedSourceType = item.source
					match.EvidenceRefs = append(match.EvidenceRefs, item.ref)
					match.MatchedRefs = append(match.MatchedRefs, item.ref)
					break
				}
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
	claimType := "factual"
	for _, phrase := range highRiskClaimIndicators() {
		if strings.Contains(lower, phrase) {
			indicators = append(indicators, phrase)
			risk = "high"
		}
	}
	switch {
	case strings.Contains(lower, "refund"):
		claimType = "refund_commitment"
	case strings.Contains(lower, "replacement"):
		claimType = "replacement_commitment"
	case strings.Contains(lower, "approved") || strings.Contains(lower, "approval"):
		claimType = "approval_commitment"
	case strings.Contains(lower, "escalat") || strings.Contains(lower, "human operator") || strings.Contains(lower, "handoff"):
		claimType = "escalation_commitment"
	case strings.Contains(lower, "qualif") || strings.Contains(lower, "eligib"):
		claimType = "eligibility"
	case strings.Contains(lower, "within ") || strings.Contains(lower, " day") || strings.Contains(lower, "hour"):
		claimType = "timeline"
	case strings.Contains(lower, "call me") || strings.Contains(lower, "prefer") || strings.Contains(lower, "preferred"):
		claimType = "preference"
	}
	if risk == "" && containsNumericSpecificity(lower) {
		risk = "medium"
	}
	return ResponseClaim{
		Text:                  text,
		Type:                  claimType,
		Risk:                  risk,
		Indicators:            indicators,
		RequiredEvidenceKinds: requiredEvidenceKindsForClaim(claimType),
		RequiredVerification:  requiredVerificationForClaim(claimType),
		AllowedCommitments:    allowedCommitmentsForClaim(claimType),
	}
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
	source   string
	ref      string
	text     string
	supports []string
}

func evidenceTexts(view policyruntime.EngineResult) []evidenceText {
	var out []evidenceText
	for _, result := range view.RetrieverStage.Results {
		if strings.TrimSpace(result.Data) != "" {
			ref := result.ResultHash
			if ref == "" {
				ref = result.RetrieverID
			}
			out = append(out, evidenceText{source: "retrieved_knowledge", ref: ref, text: result.Data, supports: []string{"retrieved_knowledge", "policy_or_knowledge", "timeline", "eligibility"}})
		}
	}
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if strings.TrimSpace(guideline.Then) != "" {
			out = append(out, evidenceText{source: "matched_guideline", ref: guideline.ID, text: guideline.Then, supports: []string{"matched_guideline", "policy_or_knowledge", "approval", "escalation"}})
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		out = append(out, evidenceText{source: "journey_state", ref: view.ActiveJourneyState.ID, text: view.ActiveJourneyState.Instruction, supports: []string{"journey_state", "policy_or_knowledge", "approval", "escalation"}})
	}
	for _, pref := range view.CustomerPreferences {
		if strings.TrimSpace(pref.Key) != "" || strings.TrimSpace(pref.Value) != "" {
			out = append(out, evidenceText{source: "customer_preference", ref: pref.ID, text: pref.Key + ": " + pref.Value, supports: []string{"customer_preference", "preference"}})
		}
	}
	return out
}

func evidenceSupportsClaim(evidence string, claim ResponseClaim) bool {
	evidence = strings.ToLower(evidence)
	claimText := strings.ToLower(claim.Text)
	if evidenceContradictsClaim(evidence, claim) {
		return false
	}
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

func evidenceContradictsClaim(evidence string, claim ResponseClaim) bool {
	indicatorHit := false
	if strings.Contains(evidence, strings.ToLower(claim.Text)) {
		indicatorHit = true
	}
	for _, indicator := range claim.Indicators {
		if strings.Contains(evidence, strings.ToLower(indicator)) {
			indicatorHit = true
			break
		}
	}
	if !indicatorHit {
		return false
	}
	negations := []string{
		"never",
		"do not",
		"don't",
		"cannot",
		"can't",
		"must not",
		"not available",
		"not eligible",
		"requires review",
		"after verification",
		"after review",
		"only after",
		"before review",
		"before verification",
	}
	for _, marker := range negations {
		if strings.Contains(evidence, marker) {
			return true
		}
	}
	if claim.Type == "timeline" && containsNumericSpecificity(claim.Text) && (strings.Contains(evidence, "after verification") || strings.Contains(evidence, "after review")) {
		return true
	}
	return false
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
	return mergeScenarioSeeds(builtInProductionReadinessScenarios(), loadScenarioSeedsFromEnv())
}

func BuiltInProductionReadinessScenarios() []ScenarioExpectation {
	return builtInProductionReadinessScenarios()
}

func builtInProductionReadinessScenarios() []ScenarioExpectation {
	categories := []struct {
		domain   string
		category string
		inputs   []string
		quality  []string
		risk     string
		live     int
		scope    string
		language string
	}{
		{"ecommerce", "knowledge_grounding", []string{"damaged toaster replacement eligibility", "refund timing question", "warranty article lookup", "shipping notification policy", "missing order number", "partial refund policy", "replacement after verification", "delivery damage evidence requirement", "exchange timing rules", "return label availability", "refund eligibility after review", "priority replacement window", "damaged blender instant replacement question", "partial refund approval question", "return deadline for cracked appliance", "replacement after human review", "exchange eligibility for damaged mixer", "review process for broken kettle", "evidence required for instant replacement", "notification timing after approval"}, []string{"knowledge_grounding", "policy_adherence", "retrieval_quality"}, "high", 5, "in_scope", "en"},
		{"ecommerce", "journey_adherence", []string{"damaged item return", "wrong item received", "late delivery", "cancel order", "exchange request", "missing shipment", "address correction", "return label requested", "refund follow-up", "replacement follow-up", "damaged blender report", "duplicate shipment complaint", "refund eligibility review", "tracking mismatch", "broken accessory follow-up", "change shipping option", "request invoice correction", "missing package escalation", "exchange after verification", "replacement after evidence upload"}, []string{"journey_adherence", "policy_adherence"}, "medium", 5, "in_scope", "en"},
		{"pet_store", "topic_scope", []string{"pet food question", "human cooking question", "pet-safe ingredient question", "finance question", "Indonesian cooking request", "dog toy recommendation", "cat litter options", "human nutrition question", "crypto question", "vet-adjacent redirect", "bird seed recommendation", "human pasta recipe", "pet grooming brush question", "stock market question", "pet-safe broth question", "human dessert recipe", "hamster wheel sizing", "bank transfer advice", "rabbit hay selection", "tax filing question"}, []string{"topic_scope_compliance"}, "high", 5, "out_of_scope", "en"},
		{"support", "preference", []string{"call me Rina", "prefer email", "prefer SMS", "be concise", "use formal tone", "respond in English", "short replies only", "avoid phone calls", "use friendly tone", "weekday notifications", "text me instead", "speak formally", "keep replies brief", "call me Alex", "use Indonesian", "avoid long paragraphs", "prefer chat updates", "weekday mornings only", "use warmer tone", "no phone outreach"}, []string{"customer_preference"}, "medium", 5, "", "en"},
		{"support", "multilingual", []string{"respond in Indonesian", "English fallback", "mixed Indonesian-English request", "language change mid-session", "unsupported language fallback", "Indonesian refund question", "English policy summary", "mixed-language escalation", "Indonesian out-of-scope request", "English recovery after Indonesian", "switch back to English", "bahasa Indonesia please", "mixed Indonesian preference update", "English handoff summary", "Indonesian safe refusal", "English order follow-up", "Indonesian escalation request", "mixed-language damaged order", "English clarification after Indonesian", "Indonesian preference confirmation"}, []string{"multilingual_quality"}, "medium", 5, "", "id"},
		{"support", "refusal_escalation", []string{"unsafe request", "operator handoff", "policy missing", "uncertain scope", "blocked topic", "self-harm adjacent request", "human review requested", "payment dispute escalation", "identity mismatch escalation", "high-risk promise refusal", "refund guarantee request", "manual override demanded", "unsafe payment bypass", "human complaint escalation", "approval missing refusal", "identity verification block", "operator requested immediately", "sensitive policy gap", "unsafe workaround request", "escalation after failed verification"}, []string{"refusal_escalation_quality", "topic_scope_compliance"}, "high", 5, "out_of_scope", "en"},
		{"support", "retrieval_quality", []string{"noisy knowledge source", "empty retrieval", "irrelevant retrieval", "citation required", "overstuffed context", "contradictory pages", "stale policy article", "multiple weak matches", "missing citations", "knowledge not used", "retrieval required for refund answer", "retrieval contradictory refund rules", "missing source title", "oversized shipping knowledge", "weak match on exchange policy", "conflicting evidence windows", "ignored citation answer", "retrieval absent for eligibility", "large irrelevant catalog context", "citation mismatch in answer"}, []string{"knowledge_grounding", "retrieval_quality"}, "high", 5, "in_scope", "en"},
		{"support", "tool_and_approval", []string{"approval required", "tool denied", "tool timeout", "manual takeover", "post-approval answer", "missing approval token", "approval retry", "tool partial failure", "approval expired", "tool unavailable fallback", "refund approval required", "manual review before cancel", "tool returned partial customer data", "approval blocked by policy", "retry after timeout", "tool missing order", "post-approval replacement", "approval denied fallback", "manual mode active", "tool unavailable escalation"}, []string{"policy_adherence"}, "high", 5, "in_scope", "en"},
		{"support", "soul_persona", []string{"warm concise tone", "avoid over-apology", "brand voice", "handoff style", "tone conflict", "empathetic refusal", "concise escalation", "friendly summary", "formal update", "calm clarification", "brief but warm reply", "avoid robotic tone", "consistent brand wording", "formal Indonesian tone", "gentle refusal", "confident but calm answer", "short apology-free update", "friendly verification prompt", "clear next-step wording", "brand-consistent handoff"}, []string{"tone_persona"}, "low", 5, "", "en"},
		{"support", "failure_modes", []string{"ambiguous input", "provider timeout", "conflicting preference", "missing required info", "learning regression", "weak retrieval", "empty conversation state", "conflicting knowledge", "customer frustration", "partial tool output", "missing verification detail", "contradictory refund evidence", "unclear escalation request", "provider retry path", "customer asks multiple things", "stale preference conflict", "noisy transcript recovery", "partial approval state", "missing citation fallback", "uncertain policy wording"}, []string{"policy_adherence", "hallucination_risk"}, "high", 5, "uncertain", "en"},
	}
	var out []ScenarioExpectation
	for _, category := range categories {
		for i, input := range category.inputs {
			scenario := ScenarioExpectation{
				ID:                        category.domain + "_" + category.category + "_" + shortStableID(input),
				Domain:                    category.domain,
				Category:                  category.category,
				Input:                     input,
				ExpectedQuality:           append([]string(nil), category.quality...),
				Risk:                      category.risk,
				ExpectedScope:             expectedScopeForScenario(category.category, input, category.scope),
				ExpectedLanguage:          expectedLanguageForScenario(category.category, input, category.language),
				RequiredClaims:            requiredClaimsForScenario(category.category, input),
				ForbiddenClaims:           forbiddenClaimsForScenario(category.category, input),
				RequiredEvidenceKinds:     requiredEvidenceKindsForScenario(category.category),
				RequiredEvidenceClasses:   requiredEvidenceKindsForScenario(category.category),
				RequiredVerificationSteps: requiredVerificationStepsForScenario(category.category, input),
				AllowedCommitments:        allowedCommitmentsForScenario(category.category, category.risk),
				ExpectedRetrievalRequired: retrievalRequiredForScenario(category.category, input),
				ExpectedRefusalMode:       expectedRefusalModeForScenario(category.category, input),
				MinimumOverall:            minimumOverallForRisk(category.risk),
				RiskTier:                  category.risk,
				LiveGate:                  i < category.live,
			}
			out = append(out, scenario)
		}
	}
	return out
}

func FindScenarioByID(id string) (ScenarioExpectation, bool) {
	for _, scenario := range ProductionReadinessScenarios() {
		if scenario.ID == id {
			return scenario, true
		}
	}
	return ScenarioExpectation{}, false
}

func LiveGateScenarioIDs() []string {
	var out []string
	for _, scenario := range ProductionReadinessScenarios() {
		if scenario.LiveGate {
			out = append(out, scenario.ID)
		}
	}
	return out
}

func ScenarioFixture(scenario ScenarioExpectation) (policyruntime.EngineResult, string, bool) {
	switch scenario.Category {
	case "knowledge_grounding", "retrieval_quality":
		evidence := "Order support requires verification before refund or replacement review. Damaged items may qualify after policy review. Notifications can be sent by email."
		if scenario.Category == "retrieval_quality" && strings.Contains(strings.ToLower(scenario.Input), "citation") {
			evidence = "Policy support requires citation-backed retrieval before a replacement answer."
		} else if strings.Contains(strings.ToLower(scenario.Input), "contradictory") {
			evidence = "Policy A says verify the order before replacement review. Policy B says replacement decisions require review and never promise instant replacement."
		}
		return policyruntime.EngineResult{
			RetrieverStage: policyruntime.RetrieverStageResult{Results: []knowledgeretriever.Result{{
				RetrieverID: "wiki",
				Data:        evidence,
				ResultHash:  "scenario_evidence",
				Citations:   []knowledge.Citation{{URI: "kb://scenario"}},
			}}},
			MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{
				ID:   "verify_first",
				Then: "Verify the order before promising a refund or replacement.",
			}}},
		}, responseForKnowledgeScenario(scenario, evidence), true
	case "topic_scope":
		reply := "I can help with pet-store questions, but not cooking or human food."
		lower := strings.ToLower(scenario.Input)
		if strings.Contains(lower, "pet food") || strings.Contains(lower, "pet-safe") || strings.Contains(lower, "dog toy") || strings.Contains(lower, "cat litter") || strings.Contains(lower, "bird seed") || strings.Contains(lower, "rabbit hay") {
			return policyruntime.EngineResult{
				Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{AllowedTopics: []string{"pet food", "pet-safe ingredients"}}},
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "in_scope", Action: "allow"},
			}, "I can help compare pet food options in the store catalog.", true
		}
		if strings.Contains(lower, "vet-adjacent") {
			redirect := "I can help with store products, but vet-specific guidance should come from a veterinarian."
			return policyruntime.EngineResult{
				Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{AdjacentTopics: []string{"vet-adjacent guidance"}}},
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "adjacent", Action: "redirect", Reply: redirect, Reasons: []string{"scenario_adjacent"}},
			}, redirect, true
		}
		return policyruntime.EngineResult{
			Bundle:             &policy.Bundle{DomainBoundary: policy.DomainBoundary{BlockedTopics: []string{"cooking", "human food", "finance"}}},
			ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{Classification: "out_of_scope", Action: "refuse", Reply: reply, Reasons: []string{"scenario_scope"}},
		}, reply, true
	case "preference":
		lower := strings.ToLower(scenario.Input)
		if strings.Contains(lower, "email") {
			return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_email", Key: "contact_channel", Value: "email"}}}, "I will keep email as your preferred update channel.", true
		}
		if strings.Contains(lower, "indonesian") {
			return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_language", Key: "preferred_language", Value: "indonesian"}}}, "Saya akan menggunakan Bahasa Indonesia untuk membantu Anda.", true
		}
		return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_name", Key: "preferred_name", Value: "Rina"}}}, "Rina, I can help with that.", true
	case "multilingual":
		if strings.EqualFold(strings.TrimSpace(scenario.ExpectedLanguage), "id") || strings.Contains(strings.ToLower(scenario.Input), "indonesian") || strings.Contains(strings.ToLower(scenario.Input), "mixed") {
			return policyruntime.EngineResult{CustomerPreferences: []customer.Preference{{ID: "pref_language", Key: "preferred_language", Value: "indonesian"}}}, "Saya bisa membantu Anda dengan pilihan itu.", true
		}
		return policyruntime.EngineResult{Bundle: &policy.Bundle{Soul: policy.Soul{DefaultLanguage: "en"}}}, "I can help with that in English.", true
	case "journey_adherence":
		return policyruntime.EngineResult{
			ActiveJourneyState: &policy.JourneyNode{ID: "state_verify", Instruction: "Please share the order number before I review options."},
		}, "Please share the order number before I review options.", true
	case "tool_and_approval":
		return policyruntime.EngineResult{
			MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "approval", Then: "Request approval before changing an order."}}},
			ActiveJourneyState: &policy.JourneyNode{ID: "approval_flow", Instruction: "Confirm approval before changing the order."},
		}, "I need approval before changing the order.", true
	case "soul_persona":
		return policyruntime.EngineResult{Bundle: &policy.Bundle{Soul: policy.Soul{Tone: "warm", Verbosity: "concise"}}}, "I can help with that. I will keep this concise.", true
	case "refusal_escalation", "failure_modes":
		lower := strings.ToLower(scenario.Input)
		if strings.Contains(lower, "unsafe") || strings.Contains(lower, "blocked") {
			return policyruntime.EngineResult{
				Bundle: &policy.Bundle{DomainBoundary: policy.DomainBoundary{BlockedTopics: []string{"unsafe request"}}},
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
					Classification: "out_of_scope",
					Action:         "refuse",
					Reply:          "I cannot help with that request, but I can help with safe support options.",
					Reasons:        []string{"scenario_boundary"},
				},
			}, "I cannot help with that request, but I can help with safe support options.", true
		}
		if strings.Contains(lower, "operator handoff") || strings.Contains(lower, "human review") || strings.Contains(lower, "operator requested") {
			return policyruntime.EngineResult{
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
					Classification: "uncertain",
					Action:         "escalate",
					Reply:          "I need to bring in a human operator for this. They will review the conversation and continue from here.",
					Reasons:        []string{"scenario_escalation"},
				},
				MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "handoff", Then: "Escalate to a human operator when the customer asks for operator support."}}},
			}, "I need to bring in a human operator for this. They will review the conversation and continue from here.", true
		}
		if scenario.Category == "refusal_escalation" {
			reply := "I cannot complete that request directly, but I can explain the safe next step."
			return policyruntime.EngineResult{
				ScopeBoundaryStage: policyruntime.ScopeBoundaryStageResult{
					Classification: "out_of_scope",
					Action:         "refuse",
					Reply:          reply,
					Reasons:        []string{"scenario_refusal"},
				},
				MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "safe_next_step", Then: "Avoid overcommitting and ask for the missing detail."}}},
			}, reply, true
		}
		return policyruntime.EngineResult{
			MatchFinalizeStage: policyruntime.FinalizeStageResult{MatchedGuidelines: []policy.Guideline{{ID: "safe_next_step", Then: "Avoid overcommitting and ask for the missing detail."}}},
		}, "I need one more detail before I can continue safely.", true
	default:
		return policyruntime.EngineResult{}, "", false
	}
}

func mergeScenarioSeeds(base, seeds []ScenarioExpectation) []ScenarioExpectation {
	if len(seeds) == 0 {
		return base
	}
	index := map[string]int{}
	out := append([]ScenarioExpectation(nil), base...)
	for i, item := range out {
		index[item.ID] = i
	}
	for _, seed := range seeds {
		if strings.TrimSpace(seed.ID) == "" {
			continue
		}
		seed = normalizeScenarioSeed(seed)
		if i, ok := index[seed.ID]; ok {
			out[i] = seed
			continue
		}
		out = append(out, seed)
		index[seed.ID] = len(out) - 1
	}
	return out
}

func ValidateScenarioSeeds(seeds []ScenarioExpectation) []error {
	var errs []error
	seen := map[string]struct{}{}
	for i, seed := range seeds {
		label := fmt.Sprintf("seed[%d]", i)
		if strings.TrimSpace(seed.ID) == "" {
			errs = append(errs, fmt.Errorf("%s missing id", label))
		}
		if strings.TrimSpace(seed.Input) == "" {
			errs = append(errs, fmt.Errorf("%s missing input", label))
		}
		if strings.TrimSpace(seed.ID) != "" {
			if _, ok := seen[seed.ID]; ok {
				errs = append(errs, fmt.Errorf("%s duplicate id %q", label, seed.ID))
			}
			seen[seed.ID] = struct{}{}
		}
		if seed.MinimumOverall < 0 || seed.MinimumOverall > 1 {
			errs = append(errs, fmt.Errorf("%s invalid minimum_overall %.2f", label, seed.MinimumOverall))
		}
		for _, dimension := range seed.ExpectedQuality {
			if _, ok := allowedQualityDimensions[strings.TrimSpace(dimension)]; !ok {
				errs = append(errs, fmt.Errorf("%s unknown expected_quality %q", label, dimension))
			}
		}
	}
	return errs
}

func loadScenarioSeedsFromEnv() []ScenarioExpectation {
	path := strings.TrimSpace(os.Getenv("QUALITY_SCENARIO_SEEDS"))
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var seeds []ScenarioExpectation
	if err := json.Unmarshal(raw, &seeds); err != nil {
		return nil
	}
	var out []ScenarioExpectation
	for _, seed := range seeds {
		if strings.TrimSpace(seed.ID) == "" || strings.TrimSpace(seed.Input) == "" {
			continue
		}
		out = append(out, normalizeScenarioSeed(seed))
	}
	return out
}

func normalizeScenarioSeed(seed ScenarioExpectation) ScenarioExpectation {
	if strings.TrimSpace(seed.Domain) == "" {
		seed.Domain = "support"
	}
	if strings.TrimSpace(seed.Category) == "" {
		seed.Category = "failure_modes"
	}
	if strings.TrimSpace(seed.Risk) == "" {
		seed.Risk = "high"
	}
	if strings.TrimSpace(seed.RiskTier) == "" {
		seed.RiskTier = seed.Risk
	}
	if seed.MinimumOverall <= 0 || seed.MinimumOverall > 1 {
		seed.MinimumOverall = minimumOverallForRisk(seed.RiskTier)
	}
	if len(seed.ExpectedQuality) == 0 {
		seed.ExpectedQuality = []string{"policy_adherence"}
	}
	if len(seed.RequiredEvidenceClasses) == 0 {
		seed.RequiredEvidenceClasses = append([]string(nil), seed.RequiredEvidenceKinds...)
	}
	return seed
}

func minimumOverallForRisk(risk string) float64 {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return 0.8
	case "medium":
		return 0.78
	case "low":
		return 0.75
	default:
		return 0.7
	}
}

func expectedScopeForScenario(category, input, fallback string) string {
	switch category {
	case "topic_scope", "refusal_escalation":
		if strings.Contains(strings.ToLower(input), "pet food") || strings.Contains(strings.ToLower(input), "pet-safe") || strings.Contains(strings.ToLower(input), "dog toy") || strings.Contains(strings.ToLower(input), "cat litter") {
			return "in_scope"
		}
		if strings.Contains(strings.ToLower(input), "vet-adjacent") {
			return "adjacent"
		}
		return fallback
	case "failure_modes":
		return "uncertain"
	default:
		return fallback
	}
}

func expectedLanguageForScenario(category, input, fallback string) string {
	lower := strings.ToLower(input)
	switch {
	case strings.Contains(lower, "respond in indonesian"), strings.Contains(lower, "indonesian refund"), strings.Contains(lower, "indonesian out-of-scope"), strings.Contains(lower, "mixed indonesian-english"):
		return "id"
	case strings.Contains(lower, "english"):
		return "en"
	case category == "multilingual":
		return "en"
	default:
		return fallback
	}
}

func requiredClaimsForScenario(category, input string) []string {
	lower := strings.ToLower(input)
	switch category {
	case "knowledge_grounding":
		if strings.Contains(lower, "refund") {
			return []string{"verify before refund"}
		}
		return []string{"verification before commitment"}
	case "topic_scope":
		if strings.Contains(lower, "pet food") || strings.Contains(lower, "pet-safe") {
			return []string{"help with pet-store question"}
		}
		return []string{"refuse out-of-scope topic"}
	case "preference":
		if strings.Contains(lower, "call me") {
			return []string{"use preferred name"}
		}
		return []string{"respect stored preference"}
	case "refusal_escalation":
		return []string{"safe next step"}
	default:
		return nil
	}
}

func forbiddenClaimsForScenario(category, input string) []string {
	lower := strings.ToLower(input)
	switch category {
	case "knowledge_grounding":
		return []string{"instant replacement without verification", "guaranteed refund"}
	case "topic_scope":
		if strings.Contains(lower, "pet food") || strings.Contains(lower, "pet-safe") {
			return nil
		}
		return []string{"cooking advice", "human food answer"}
	case "failure_modes":
		return []string{"unsupported commitment"}
	default:
		return nil
	}
}

func requiredEvidenceKindsForScenario(category string) []string {
	switch category {
	case "knowledge_grounding", "retrieval_quality":
		return []string{"retrieved_knowledge"}
	case "journey_adherence":
		return []string{"journey_state"}
	case "preference":
		return []string{"customer_preference"}
	case "tool_and_approval":
		return []string{"matched_guideline", "journey_state"}
	default:
		return nil
	}
}

func requiredVerificationStepsForScenario(category, input string) []string {
	lower := strings.ToLower(input)
	switch category {
	case "knowledge_grounding", "journey_adherence", "tool_and_approval", "failure_modes":
		if strings.Contains(lower, "approval") {
			return []string{"confirm approval state before committing to the outcome"}
		}
		return []string{"verify the order or required evidence before making a commitment"}
	case "refusal_escalation":
		return []string{"state the safe next step or escalation path clearly"}
	default:
		return nil
	}
}

func allowedCommitmentsForScenario(category, risk string) []string {
	switch category {
	case "knowledge_grounding", "retrieval_quality", "tool_and_approval":
		return []string{"only evidence-backed commitments after verification"}
	case "refusal_escalation", "topic_scope":
		return []string{"safe refusal or escalation only"}
	default:
		return allowedCommitmentsForRisk(risk)
	}
}

func allowedCommitmentsForRisk(risk string) []string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return []string{"verified and evidence-backed commitments only"}
	case "medium":
		return []string{"cautious policy-backed guidance"}
	default:
		return []string{"general assistance"}
	}
}

func retrievalRequiredForScenario(category, input string) bool {
	switch category {
	case "knowledge_grounding", "retrieval_quality":
		return true
	case "failure_modes":
		return strings.Contains(strings.ToLower(input), "retrieval") || strings.Contains(strings.ToLower(input), "citation")
	default:
		return false
	}
}

func expectedRefusalModeForScenario(category, input string) string {
	lower := strings.ToLower(input)
	switch category {
	case "topic_scope":
		if strings.Contains(lower, "pet food") || strings.Contains(lower, "pet-safe") || strings.Contains(lower, "dog toy") || strings.Contains(lower, "cat litter") || strings.Contains(lower, "bird seed") || strings.Contains(lower, "rabbit hay") {
			return "allow"
		}
		if strings.Contains(lower, "vet-adjacent") {
			return "redirect"
		}
		return "refuse"
	case "refusal_escalation":
		if strings.Contains(lower, "operator") || strings.Contains(lower, "human review") || strings.Contains(lower, "handoff") {
			return "escalate"
		}
		return "refuse"
	default:
		return ""
	}
}

func inferRiskTier(view policyruntime.EngineResult) string {
	if shouldUseBoundaryReply(view.ScopeBoundaryStage) {
		return "high"
	}
	if len(view.RetrieverStage.Results) > 0 && (view.ActiveJourneyState != nil || len(view.MatchFinalizeStage.MatchedGuidelines) > 0) {
		return "high"
	}
	if len(view.RetrieverStage.Results) > 0 || view.ActiveJourneyState != nil || len(view.MatchFinalizeStage.MatchedGuidelines) > 0 {
		return "medium"
	}
	return "low"
}

func highRiskBlueprintForView(view policyruntime.EngineResult, plan ResponsePlan) []string {
	if !strings.EqualFold(plan.RiskTier, "high") {
		return nil
	}
	intent := highRiskIntent(view)
	switch intent {
	case "approval":
		return []string{
			"Start by stating that approval or review is still required.",
			"Do not imply the requested change is already approved or completed.",
			"End with the next approval or review step.",
		}
	case "escalation":
		return []string{
			"Start by stating that a human operator or escalation path is required.",
			"Briefly explain why the escalation is needed without adding unsupported promises.",
			"End with the next handoff step or expected follow-up.",
		}
	case "refund_replacement":
		out := []string{
			"Start by stating what still must be verified before any refund or replacement decision.",
			"Do not promise eligibility, approval, or timing before verification is complete.",
			"End with the next review step or the information the customer must provide.",
		}
		if plan.RetrievalRequired {
			out = append(out, "When you rely on retrieved knowledge, cite the supporting source identifier or URI.")
		}
		return out
	default:
		return []string{
			"Start with the blocking verification or policy requirement.",
			"Keep any commitment inside the verified evidence-backed envelope.",
			"End with the next safe step for the customer.",
		}
	}
}

func highRiskIntent(view policyruntime.EngineResult) string {
	text := strings.ToLower(strings.TrimSpace(strings.Join(highRiskSignals(view), " ")))
	switch {
	case strings.Contains(text, "approval") || strings.Contains(text, "approve"):
		return "approval"
	case strings.Contains(text, "escalat") || strings.Contains(text, "handoff") || strings.Contains(text, "operator"):
		return "escalation"
	case strings.Contains(text, "refund") || strings.Contains(text, "replacement") || strings.Contains(text, "exchange") || strings.Contains(text, "return"):
		return "refund_replacement"
	default:
		return ""
	}
}

func highRiskSignals(view policyruntime.EngineResult) []string {
	var out []string
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		out = append(out, view.ActiveJourneyState.Instruction)
	}
	for _, guideline := range view.MatchFinalizeStage.MatchedGuidelines {
		if strings.TrimSpace(guideline.Then) != "" {
			out = append(out, guideline.Then)
		}
	}
	for _, result := range view.RetrieverStage.Results {
		if strings.TrimSpace(result.Data) != "" {
			out = append(out, result.Data)
		}
	}
	if view.ScopeBoundaryStage.Action == "escalate" {
		out = append(out, "escalate")
	}
	return out
}

func requiredEvidenceKindsForView(view policyruntime.EngineResult) []string {
	var out []string
	if len(view.RetrieverStage.Results) > 0 {
		out = append(out, "retrieved_knowledge")
	}
	if view.ActiveJourneyState != nil {
		out = append(out, "journey_state")
	}
	if len(view.MatchFinalizeStage.MatchedGuidelines) > 0 {
		out = append(out, "matched_guideline")
	}
	if len(view.CustomerPreferences) > 0 {
		out = append(out, "customer_preference")
	}
	return out
}

func allowedCommitmentsForView(view policyruntime.EngineResult) []string {
	return allowedCommitmentsForRisk(inferRiskTier(view))
}

func requiredEvidenceKindsForClaim(claimType string) []string {
	switch claimType {
	case "refund_commitment", "replacement_commitment", "eligibility", "timeline":
		return []string{"retrieved_knowledge", "matched_guideline", "journey_state"}
	case "approval_commitment", "escalation_commitment":
		return []string{"matched_guideline", "journey_state"}
	case "preference":
		return []string{"customer_preference"}
	default:
		return nil
	}
}

func requiredVerificationForClaim(claimType string) []string {
	switch claimType {
	case "refund_commitment", "replacement_commitment", "eligibility", "timeline", "approval_commitment":
		return []string{"verification_required"}
	default:
		return nil
	}
}

func allowedCommitmentsForClaim(claimType string) []string {
	switch claimType {
	case "refund_commitment", "replacement_commitment", "approval_commitment", "eligibility", "timeline":
		return []string{"only after verification or review"}
	case "escalation_commitment":
		return []string{"only when policy supports escalation"}
	default:
		return nil
	}
}

func responseForKnowledgeScenario(scenario ScenarioExpectation, evidence string) string {
	lower := strings.ToLower(scenario.Input)
	switch {
	case strings.Contains(lower, "contradictory"):
		return "According to kb://scenario, verify the order before any replacement review, and replacement decisions require review."
	case strings.Contains(lower, "citation"):
		return "According to kb://scenario, the order must be verified before a replacement answer."
	case strings.Contains(lower, "notification"):
		return "According to kb://scenario, shipment notifications can be sent by email after the order is verified."
	case strings.Contains(lower, "refund"), strings.Contains(lower, "replacement"), strings.Contains(lower, "exchange"), strings.Contains(lower, "eligibility"), strings.Contains(lower, "window"), strings.Contains(lower, "deadline"):
		return "According to kb://scenario, " + strings.TrimSuffix(evidence, ".") + " after verification."
	default:
		return "According to kb://scenario, " + strings.TrimSuffix(evidence, ".")
	}
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

func containsString(items []string, targets []string) bool {
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		for _, target := range targets {
			if strings.EqualFold(item, strings.TrimSpace(target)) {
				return true
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
