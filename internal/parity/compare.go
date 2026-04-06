package parity

import (
	"fmt"
	"slices"
	"strings"
)

func BuildReport(fx Fixture, scenarios []ScenarioReport) Report {
	out := Report{
		FixtureVersion: fx.Version,
		ScenarioCount:  len(scenarios),
		Scenarios:      scenarios,
	}
	for _, item := range scenarios {
		if item.Passed {
			out.PassedCount++
		} else {
			out.FailedCount++
		}
	}
	return out
}

func EvaluateScenario(s Scenario, parmesan NormalizedResult, parlant NormalizedResult) ScenarioReport {
	expErrs := checkExpectations(s.Expect, parmesan, "parmesan")
	expErrs = append(expErrs, checkExpectations(s.Expect, parlant, "parlant")...)
	diffErrs := compareResults(s, parmesan, parlant)
	return ScenarioReport{
		Scenario:          s,
		Parmesan:          parmesan,
		Parlant:           parlant,
		ExpectationErrors: expErrs,
		DiffErrors:        diffErrs,
		Passed:            len(expErrs) == 0 && len(diffErrs) == 0,
	}
}

func checkExpectations(exp Expectations, got NormalizedResult, label string) []string {
	var out []string
	if len(exp.MatchedGuidelines) > 0 && !sameSet(exp.MatchedGuidelines, got.MatchedGuidelines) {
		out = append(out, fmt.Sprintf("%s matched_guidelines = %v, want %v", label, got.MatchedGuidelines, exp.MatchedGuidelines))
	}
	if len(exp.MatchedObservations) > 0 && !sameSet(exp.MatchedObservations, got.MatchedObservations) {
		out = append(out, fmt.Sprintf("%s matched_observations = %v, want %v", label, got.MatchedObservations, exp.MatchedObservations))
	}
	if len(exp.SuppressedGuidelines) > 0 && !sameSet(exp.SuppressedGuidelines, got.SuppressedGuidelines) {
		out = append(out, fmt.Sprintf("%s suppressed_guidelines = %v, want %v", label, got.SuppressedGuidelines, exp.SuppressedGuidelines))
	}
	if len(exp.ResolutionRecords) > 0 && !sameResolutionSet(exp.ResolutionRecords, got.ResolutionRecords) {
		out = append(out, fmt.Sprintf("%s resolution_records = %v, want %v", label, got.ResolutionRecords, exp.ResolutionRecords))
	}
	if exp.ActiveJourney != nil && strings.TrimSpace(exp.ActiveJourney.ID) != strings.TrimSpace(got.ActiveJourney) {
		out = append(out, fmt.Sprintf("%s active_journey = %q, want %q", label, got.ActiveJourney, exp.ActiveJourney.ID))
	}
	if exp.JourneyDecision != "" && exp.JourneyDecision != got.JourneyDecision {
		out = append(out, fmt.Sprintf("%s journey_decision = %q, want %q", label, got.JourneyDecision, exp.JourneyDecision))
	}
	if exp.NextJourneyNode != "" && exp.NextJourneyNode != got.NextJourneyNode {
		out = append(out, fmt.Sprintf("%s next_journey_node = %q, want %q", label, got.NextJourneyNode, exp.NextJourneyNode))
	}
	if normalized := normalizeExpectedFollowUps(exp.ProjectedFollowUps); len(normalized) > 0 && !sameMapSet(normalized, got.ProjectedFollowUps) {
		out = append(out, fmt.Sprintf("%s projected_follow_ups = %v, want %v", label, got.ProjectedFollowUps, exp.ProjectedFollowUps))
	}
	if normalized := normalizeExpectedFollowUps(exp.LegalFollowUps); len(normalized) > 0 && !sameMapSet(normalized, got.LegalFollowUps) {
		out = append(out, fmt.Sprintf("%s legal_follow_ups = %v, want %v", label, got.LegalFollowUps, exp.LegalFollowUps))
	}
	if len(exp.ExposedTools) > 0 && !sameSet(exp.ExposedTools, got.ExposedTools) {
		out = append(out, fmt.Sprintf("%s exposed_tools = %v, want %v", label, got.ExposedTools, exp.ExposedTools))
	}
	if len(exp.ToolCandidates) > 0 && !sameSet(exp.ToolCandidates, got.ToolCandidates) {
		out = append(out, fmt.Sprintf("%s tool_candidates = %v, want %v", label, got.ToolCandidates, exp.ToolCandidates))
	}
	if len(exp.ToolCandidateStates) > 0 && !sameStringMap(exp.ToolCandidateStates, got.ToolCandidateStates) {
		out = append(out, fmt.Sprintf("%s tool_candidate_states = %v, want %v", label, got.ToolCandidateStates, exp.ToolCandidateStates))
	}
	if len(exp.ToolCandidateRejectedBy) > 0 && !sameStringMap(exp.ToolCandidateRejectedBy, got.ToolCandidateRejectedBy) {
		out = append(out, fmt.Sprintf("%s tool_candidate_rejected_by = %v, want %v", label, got.ToolCandidateRejectedBy, exp.ToolCandidateRejectedBy))
	}
	if len(exp.ToolCandidateReasons) > 0 {
		for key, want := range exp.ToolCandidateReasons {
			gotReason := got.ToolCandidateReasons[key]
			if !semanticContains(gotReason, want) {
				out = append(out, fmt.Sprintf("%s tool_candidate_reasons[%s] = %q, want semantic match for %q", label, key, gotReason, want))
			}
		}
	}
	if len(exp.ToolCandidateTandemWith) > 0 && !sameMapSet(exp.ToolCandidateTandemWith, got.ToolCandidateTandemWith) {
		out = append(out, fmt.Sprintf("%s tool_candidate_tandem_with = %v, want %v", label, got.ToolCandidateTandemWith, exp.ToolCandidateTandemWith))
	}
	if len(exp.OverlappingToolGroups) > 0 && !sameGroupSet(exp.OverlappingToolGroups, got.OverlappingToolGroups) {
		out = append(out, fmt.Sprintf("%s overlapping_tool_groups = %v, want %v", label, got.OverlappingToolGroups, exp.OverlappingToolGroups))
	}
	if exp.SelectedTool != "" && exp.SelectedTool != got.SelectedTool {
		out = append(out, fmt.Sprintf("%s selected_tool = %q, want %q", label, got.SelectedTool, exp.SelectedTool))
	}
	if len(exp.SelectedTools) > 0 && !sameSet(exp.SelectedTools, got.SelectedTools) {
		out = append(out, fmt.Sprintf("%s selected_tools = %v, want %v", label, got.SelectedTools, exp.SelectedTools))
	}
	if exp.ToolCallCount > 0 && len(got.ToolCalls) != exp.ToolCallCount {
		out = append(out, fmt.Sprintf("%s tool_call_count = %d, want %d", label, len(got.ToolCalls), exp.ToolCallCount))
	}
	if len(exp.ToolCallTools) > 0 && !sameMultiset(exp.ToolCallTools, got.ToolCallTools) {
		out = append(out, fmt.Sprintf("%s tool_call_tools = %v, want %v", label, got.ToolCallTools, exp.ToolCallTools))
	}
	if exp.ResponseMode != "" && exp.ResponseMode != got.ResponseMode {
		out = append(out, fmt.Sprintf("%s response_mode = %q, want %q", label, got.ResponseMode, exp.ResponseMode))
	}
	if exp.NoMatch != nil && got.NoMatch != *exp.NoMatch {
		out = append(out, fmt.Sprintf("%s no_match = %t, want %t", label, got.NoMatch, *exp.NoMatch))
	}
	if exp.SelectedTemplate != nil {
		want := ""
		if exp.SelectedTemplate != nil {
			want = *exp.SelectedTemplate
		}
		if got.SelectedTemplate != want {
			out = append(out, fmt.Sprintf("%s selected_template = %q, want %q", label, got.SelectedTemplate, want))
		}
	}
	if exp.VerificationOutcome != "" && got.VerificationOutcome != exp.VerificationOutcome {
		out = append(out, fmt.Sprintf("%s verification_outcome = %q, want %q", label, got.VerificationOutcome, exp.VerificationOutcome))
	}
	if len(exp.ResponseAnalysis.StillRequired) > 0 {
		for _, item := range exp.ResponseAnalysis.StillRequired {
			if !slices.Contains(got.ResponseAnalysisStillRequired, item) && !strings.Contains(strings.ToLower(got.ResponseText), strings.ToLower(item)) && !slices.Contains(got.MatchedGuidelines, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.still_required missing signal %q", label, item))
			}
		}
	}
	if len(exp.ResponseAnalysis.AlreadySatisfied) > 0 {
		for _, item := range exp.ResponseAnalysis.AlreadySatisfied {
			if !slices.Contains(got.ResponseAnalysisAlreadySatisfied, item) && slices.Contains(got.MatchedGuidelines, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.already_satisfied guideline still matched %q", label, item))
			}
		}
	}
	if len(exp.ResponseAnalysis.PartiallyApplied) > 0 {
		for _, item := range exp.ResponseAnalysis.PartiallyApplied {
			if !slices.Contains(got.ResponseAnalysisPartiallyApplied, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.partially_applied missing signal %q", label, item))
			}
		}
	}
	if len(exp.ResponseAnalysis.SatisfiedByToolEvent) > 0 {
		for _, item := range exp.ResponseAnalysis.SatisfiedByToolEvent {
			if !slices.Contains(got.ResponseAnalysisToolSatisfied, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.satisfied_by_tool_event missing signal %q", label, item))
			}
		}
	}
	if len(exp.ResponseAnalysis.SatisfactionSources) > 0 && !sameStringMap(exp.ResponseAnalysis.SatisfactionSources, got.ResponseAnalysisSources) {
		out = append(out, fmt.Sprintf("%s response_analysis.satisfaction_sources = %v, want %v", label, got.ResponseAnalysisSources, exp.ResponseAnalysis.SatisfactionSources))
	}
	for _, needle := range exp.ResponseSemantics.MustInclude {
		if !semanticContains(got.ResponseText, needle) {
			out = append(out, fmt.Sprintf("%s response missing %q", label, needle))
		}
	}
	for _, needle := range exp.ResponseSemantics.MustNotInclude {
		if violatesMustNotInclude(got.ResponseText, needle) {
			out = append(out, fmt.Sprintf("%s response unexpectedly contains %q", label, needle))
		}
	}
	return out
}

func compareResults(s Scenario, left NormalizedResult, right NormalizedResult) []string {
	var out []string
	if len(s.Expect.MatchedGuidelines) > 0 && !sameSet(left.MatchedGuidelines, right.MatchedGuidelines) {
		out = append(out, fmt.Sprintf("matched_guidelines differ: parmesan=%v parlant=%v", left.MatchedGuidelines, right.MatchedGuidelines))
	}
	if len(s.Expect.MatchedObservations) > 0 && !sameSet(left.MatchedObservations, right.MatchedObservations) {
		out = append(out, fmt.Sprintf("matched_observations differ: parmesan=%v parlant=%v", left.MatchedObservations, right.MatchedObservations))
	}
	if s.Expect.ActiveJourney != nil && strings.TrimSpace(left.ActiveJourney) != strings.TrimSpace(right.ActiveJourney) {
		out = append(out, fmt.Sprintf("active_journey differs: parmesan=%q parlant=%q", left.ActiveJourney, right.ActiveJourney))
	}
	if s.Expect.NextJourneyNode != "" && strings.TrimSpace(left.ActiveJourneyNode) != strings.TrimSpace(right.ActiveJourneyNode) {
		out = append(out, fmt.Sprintf("active_journey_node differs: parmesan=%q parlant=%q", left.ActiveJourneyNode, right.ActiveJourneyNode))
	}
	if len(s.Expect.ResolutionRecords) > 0 && !sameResolutionSetForScenario(s.Expect.ResolutionRecords, left.ResolutionRecords, right.ResolutionRecords) {
		out = append(out, fmt.Sprintf("resolution_records differ: parmesan=%v parlant=%v", left.ResolutionRecords, right.ResolutionRecords))
	}
	if s.Expect.JourneyDecision != "" && left.JourneyDecision != right.JourneyDecision {
		out = append(out, fmt.Sprintf("journey_decision differs: parmesan=%q parlant=%q", left.JourneyDecision, right.JourneyDecision))
	}
	if s.Expect.NextJourneyNode != "" && left.NextJourneyNode != right.NextJourneyNode {
		out = append(out, fmt.Sprintf("next_journey_node differs: parmesan=%q parlant=%q", left.NextJourneyNode, right.NextJourneyNode))
	}
	if len(s.Expect.ProjectedFollowUps) > 0 && !sameMapSet(left.ProjectedFollowUps, right.ProjectedFollowUps) {
		out = append(out, fmt.Sprintf("projected_follow_ups differ: parmesan=%v parlant=%v", left.ProjectedFollowUps, right.ProjectedFollowUps))
	}
	if len(s.Expect.LegalFollowUps) > 0 && !sameMapSet(left.LegalFollowUps, right.LegalFollowUps) {
		out = append(out, fmt.Sprintf("legal_follow_ups differ: parmesan=%v parlant=%v", left.LegalFollowUps, right.LegalFollowUps))
	}
	if len(s.Expect.ExposedTools) > 0 && !sameSet(left.ExposedTools, right.ExposedTools) {
		out = append(out, fmt.Sprintf("exposed_tools differ: parmesan=%v parlant=%v", left.ExposedTools, right.ExposedTools))
	}
	if len(s.Expect.ToolCandidates) > 0 && !sameSet(left.ToolCandidates, right.ToolCandidates) {
		out = append(out, fmt.Sprintf("tool_candidates differ: parmesan=%v parlant=%v", left.ToolCandidates, right.ToolCandidates))
	}
	if len(s.Expect.ToolCandidateStates) > 0 && !sameStringMap(left.ToolCandidateStates, right.ToolCandidateStates) {
		out = append(out, fmt.Sprintf("tool_candidate_states differ: parmesan=%v parlant=%v", left.ToolCandidateStates, right.ToolCandidateStates))
	}
	if len(s.Expect.ToolCandidateRejectedBy) > 0 && !sameStringMap(left.ToolCandidateRejectedBy, right.ToolCandidateRejectedBy) {
		out = append(out, fmt.Sprintf("tool_candidate_rejected_by differ: parmesan=%v parlant=%v", left.ToolCandidateRejectedBy, right.ToolCandidateRejectedBy))
	}
	if len(s.Expect.ToolCandidateReasons) > 0 && !sameSemanticStringMap(left.ToolCandidateReasons, right.ToolCandidateReasons, s.Expect.ToolCandidateReasons) {
		out = append(out, fmt.Sprintf("tool_candidate_reasons differ: parmesan=%v parlant=%v", left.ToolCandidateReasons, right.ToolCandidateReasons))
	}
	if len(s.Expect.ToolCandidateTandemWith) > 0 && !sameMapSet(left.ToolCandidateTandemWith, right.ToolCandidateTandemWith) {
		out = append(out, fmt.Sprintf("tool_candidate_tandem_with differ: parmesan=%v parlant=%v", left.ToolCandidateTandemWith, right.ToolCandidateTandemWith))
	}
	if len(s.Expect.OverlappingToolGroups) > 0 && !sameGroupSet(left.OverlappingToolGroups, right.OverlappingToolGroups) {
		out = append(out, fmt.Sprintf("overlapping_tool_groups differ: parmesan=%v parlant=%v", left.OverlappingToolGroups, right.OverlappingToolGroups))
	}
	if s.Expect.SelectedTool != "" && left.SelectedTool != right.SelectedTool {
		out = append(out, fmt.Sprintf("selected_tool differs: parmesan=%q parlant=%q", left.SelectedTool, right.SelectedTool))
	}
	if len(s.Expect.SelectedTools) > 0 && !sameSet(left.SelectedTools, right.SelectedTools) {
		out = append(out, fmt.Sprintf("selected_tools differ: parmesan=%v parlant=%v", left.SelectedTools, right.SelectedTools))
	}
	if s.Expect.ToolCallCount > 0 && len(left.ToolCalls) != len(right.ToolCalls) {
		out = append(out, fmt.Sprintf("tool_call_count differs: parmesan=%d parlant=%d", len(left.ToolCalls), len(right.ToolCalls)))
	}
	if len(s.Expect.ToolCallTools) > 0 && !sameMultiset(left.ToolCallTools, right.ToolCallTools) {
		out = append(out, fmt.Sprintf("tool_call_tools differ: parmesan=%v parlant=%v", left.ToolCallTools, right.ToolCallTools))
	}
	if s.Expect.ResponseMode != "" && left.ResponseMode != right.ResponseMode {
		out = append(out, fmt.Sprintf("response_mode differs: parmesan=%q parlant=%q", left.ResponseMode, right.ResponseMode))
	}
	if s.Expect.NoMatch != nil && left.NoMatch != right.NoMatch {
		out = append(out, fmt.Sprintf("no_match differs: parmesan=%t parlant=%t", left.NoMatch, right.NoMatch))
	}
	if len(s.Expect.ResponseAnalysis.SatisfactionSources) > 0 && !sameStringMap(left.ResponseAnalysisSources, right.ResponseAnalysisSources) {
		out = append(out, fmt.Sprintf("response_analysis.satisfaction_sources differ: parmesan=%v parlant=%v", left.ResponseAnalysisSources, right.ResponseAnalysisSources))
	}
	if len(s.Expect.ResponseAnalysis.PartiallyApplied) > 0 && !sameSet(left.ResponseAnalysisPartiallyApplied, right.ResponseAnalysisPartiallyApplied) {
		out = append(out, fmt.Sprintf("response_analysis.partially_applied differ: parmesan=%v parlant=%v", left.ResponseAnalysisPartiallyApplied, right.ResponseAnalysisPartiallyApplied))
	}
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func violatesMustNotInclude(haystack, needle string) bool {
	lowerHaystack := strings.ToLower(haystack)
	lowerNeedle := strings.ToLower(strings.TrimSpace(needle))
	if lowerNeedle == "" || !strings.Contains(lowerHaystack, lowerNeedle) {
		return false
	}
	for _, sentence := range splitSentences(lowerHaystack) {
		searchFrom := 0
		for {
			relativeIdx := strings.Index(sentence[searchFrom:], lowerNeedle)
			if relativeIdx < 0 {
				break
			}
			idx := searchFrom + relativeIdx
			if !sentenceNegatesNeedleAt(sentence, lowerNeedle, idx) {
				return true
			}
			searchFrom = idx + len(lowerNeedle)
			if searchFrom >= len(sentence) {
				break
			}
		}
	}
	return false
}

func semanticContains(haystack, needle string) bool {
	if containsFold(haystack, needle) {
		return true
	}
	for _, alt := range splitAlternatives(needle) {
		if semanticContainsAlternative(haystack, alt) {
			return true
		}
	}
	return false
}

func splitSentences(text string) []string {
	replacer := strings.NewReplacer("!", ".", "?", ".", "\n", ".")
	parts := strings.Split(replacer.Replace(text), ".")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func sentenceNegatesNeedleAt(sentence, needle string, idx int) bool {
	if idx < 0 || idx+len(needle) > len(sentence) {
		return false
	}
	start := idx - 24
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + 40
	if end > len(sentence) {
		end = len(sentence)
	}
	window := sentence[start:end]
	negations := []string{
		"not " + needle,
		"no " + needle,
		"without " + needle,
		needle + " isn't",
		needle + " is not",
		needle + " wasn't",
		needle + " was not",
		needle + " aren't",
		needle + " are not",
		needle + " unavailable",
		needle + " not available",
		needle + " isn't available",
		needle + " is not available",
		"don't " + needle,
		"do not " + needle,
	}
	for _, negation := range negations {
		if strings.Contains(window, negation) {
			return true
		}
	}
	return false
}

func semanticContainsAlternative(haystack, needle string) bool {
	hTokens := tokenSet(haystack)
	nTokens := tokenizeMeaningful(needle)
	if len(nTokens) == 0 {
		return true
	}
	matched := 0
	for _, token := range nTokens {
		if _, ok := hTokens[token]; ok {
			matched++
		}
	}
	threshold := len(nTokens)
	if threshold > 2 {
		threshold = 2
	}
	return matched >= threshold
}

func splitAlternatives(text string) []string {
	raw := strings.Split(strings.ToLower(strings.TrimSpace(text)), " or ")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func tokenSet(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range tokenizeMeaningful(text) {
		out[token] = struct{}{}
	}
	return out
}

func tokenizeMeaningful(text string) []string {
	replacer := strings.NewReplacer(
		".", " ", ",", " ", "!", " ", "?", " ", ":", " ", ";", " ",
		"(", " ", ")", " ", "\"", " ", "'", " ", "-", " ", "/", " ",
	)
	text = strings.ToLower(replacer.Replace(text))
	parts := strings.Fields(text)
	stop := map[string]struct{}{
		"a": {}, "an": {}, "the": {}, "to": {}, "for": {}, "of": {}, "and": {}, "or": {},
		"is": {}, "are": {}, "be": {}, "please": {}, "could": {}, "would": {}, "should": {},
		"you": {}, "your": {}, "they": {}, "their": {}, "them": {}, "what": {}, "which": {},
		"ask": {}, "asking": {}, "tell": {}, "telling": {}, "provide": {}, "provided": {},
		"check": {}, "checking": {}, "confirm": {}, "confirmed": {}, "talk": {}, "about": {},
	}
	out := make([]string, 0, len(parts))
	for _, token := range parts {
		if _, ok := stop[token]; ok {
			continue
		}
		out = append(out, token)
	}
	return out
}

func sameSet(left, right []string) bool {
	a := dedupeAndSort(left)
	b := dedupeAndSort(right)
	return slices.Equal(a, b)
}

func sameMultiset(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func sameMapSet(left, right map[string][]string) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for key, leftItems := range left {
		rightItems, ok := right[key]
		if !ok || !sameSet(leftItems, rightItems) {
			return false
		}
	}
	return true
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if strings.TrimSpace(right[key]) != strings.TrimSpace(leftValue) {
			return false
		}
	}
	return true
}

func sameSemanticStringMap(left, right, keys map[string]string) bool {
	for key := range keys {
		if !semanticContains(left[key], right[key]) && !semanticContains(right[key], left[key]) {
			return false
		}
	}
	return true
}

func normalizeExpectedFollowUps(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		normalizedKey := normalizeProjectedID(key)
		normalizedValues := make([]string, 0, len(values))
		for _, value := range values {
			normalizedValues = append(normalizedValues, normalizeProjectedID(value))
		}
		out[normalizedKey] = dedupeAndSort(normalizedValues)
	}
	return out
}

func sameGroupSet(left, right [][]string) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	normalize := func(groups [][]string) []string {
		out := make([]string, 0, len(groups))
		for _, group := range groups {
			out = append(out, strings.Join(dedupeAndSort(group), ","))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(normalize(left), normalize(right))
}

func sameResolutionSet(exp []ResolutionExpectation, got []NormalizedResolution) bool {
	want := make([]NormalizedResolution, 0, len(exp))
	for _, item := range exp {
		want = append(want, NormalizedResolution{EntityID: normalizeProjectedID(item.EntityID), Kind: item.Kind})
	}
	return sameResolutionSetForScenario(exp, want, got)
}

func sameResolutionSetForScenario(exp []ResolutionExpectation, left, right []NormalizedResolution) bool {
	scoped := func(items []NormalizedResolution) []NormalizedResolution {
		if len(items) == 0 {
			return nil
		}
		expectedNone := map[string]struct{}{}
		for _, item := range exp {
			if strings.EqualFold(strings.TrimSpace(item.Kind), "none") {
				expectedNone[normalizeProjectedID(strings.TrimSpace(item.EntityID))] = struct{}{}
			}
		}
		out := make([]NormalizedResolution, 0, len(items))
		for _, item := range items {
			entityID := strings.TrimSpace(item.EntityID)
			kind := strings.TrimSpace(item.Kind)
			if strings.EqualFold(kind, "none") {
				if _, ok := expectedNone[entityID]; !ok {
					continue
				}
			}
			out = append(out, NormalizedResolution{EntityID: entityID, Kind: kind})
		}
		return out
	}
	return sameResolutionSetFromNormalized(scoped(left), scoped(right))
}

func sameResolutionSetFromNormalized(left, right []NormalizedResolution) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	normalize := func(items []NormalizedResolution) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, strings.TrimSpace(item.EntityID)+"|"+strings.TrimSpace(item.Kind))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(normalize(left), normalize(right))
}

func dedupeAndSort(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}
