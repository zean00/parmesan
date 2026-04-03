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
	if !s.SkipParlantExpect {
		expErrs = append(expErrs, checkExpectations(s.Expect, parlant, "parlant")...)
	}
	diffErrs := []string(nil)
	if !s.SkipEngineDiff {
		diffErrs = compareResults(parmesan, parlant)
	}
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
	if exp.ActiveJourney != nil && strings.TrimSpace(exp.ActiveJourney.ID) != strings.TrimSpace(got.ActiveJourney) {
		out = append(out, fmt.Sprintf("%s active_journey = %q, want %q", label, got.ActiveJourney, exp.ActiveJourney.ID))
	}
	if exp.JourneyDecision != "" && exp.JourneyDecision != got.JourneyDecision {
		out = append(out, fmt.Sprintf("%s journey_decision = %q, want %q", label, got.JourneyDecision, exp.JourneyDecision))
	}
	if exp.NextJourneyNode != "" && exp.NextJourneyNode != got.NextJourneyNode {
		out = append(out, fmt.Sprintf("%s next_journey_node = %q, want %q", label, got.NextJourneyNode, exp.NextJourneyNode))
	}
	if len(exp.ExposedTools) > 0 && !sameSet(exp.ExposedTools, got.ExposedTools) {
		out = append(out, fmt.Sprintf("%s exposed_tools = %v, want %v", label, got.ExposedTools, exp.ExposedTools))
	}
	if exp.SelectedTool != "" && exp.SelectedTool != got.SelectedTool {
		out = append(out, fmt.Sprintf("%s selected_tool = %q, want %q", label, got.SelectedTool, exp.SelectedTool))
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
			if !strings.Contains(strings.ToLower(got.ResponseText), strings.ToLower(item)) && !slices.Contains(got.MatchedGuidelines, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.still_required missing signal %q", label, item))
			}
		}
	}
	if len(exp.ResponseAnalysis.AlreadySatisfied) > 0 {
		for _, item := range exp.ResponseAnalysis.AlreadySatisfied {
			if slices.Contains(got.MatchedGuidelines, item) {
				out = append(out, fmt.Sprintf("%s response_analysis.already_satisfied guideline still matched %q", label, item))
			}
		}
	}
	for _, needle := range exp.ResponseSemantics.MustInclude {
		if !semanticContains(got.ResponseText, needle) {
			out = append(out, fmt.Sprintf("%s response missing %q", label, needle))
		}
	}
	for _, needle := range exp.ResponseSemantics.MustNotInclude {
		if containsFold(got.ResponseText, needle) {
			out = append(out, fmt.Sprintf("%s response unexpectedly contains %q", label, needle))
		}
	}
	return out
}

func compareResults(left NormalizedResult, right NormalizedResult) []string {
	var out []string
	if !sameSet(left.MatchedGuidelines, right.MatchedGuidelines) {
		out = append(out, fmt.Sprintf("matched_guidelines differ: parmesan=%v parlant=%v", left.MatchedGuidelines, right.MatchedGuidelines))
	}
	if !sameSet(left.MatchedObservations, right.MatchedObservations) {
		out = append(out, fmt.Sprintf("matched_observations differ: parmesan=%v parlant=%v", left.MatchedObservations, right.MatchedObservations))
	}
	if strings.TrimSpace(left.ActiveJourney) != strings.TrimSpace(right.ActiveJourney) {
		out = append(out, fmt.Sprintf("active_journey differs: parmesan=%q parlant=%q", left.ActiveJourney, right.ActiveJourney))
	}
	if strings.TrimSpace(left.ActiveJourneyNode) != strings.TrimSpace(right.ActiveJourneyNode) {
		out = append(out, fmt.Sprintf("active_journey_node differs: parmesan=%q parlant=%q", left.ActiveJourneyNode, right.ActiveJourneyNode))
	}
	if left.JourneyDecision != right.JourneyDecision {
		out = append(out, fmt.Sprintf("journey_decision differs: parmesan=%q parlant=%q", left.JourneyDecision, right.JourneyDecision))
	}
	if left.NextJourneyNode != right.NextJourneyNode {
		out = append(out, fmt.Sprintf("next_journey_node differs: parmesan=%q parlant=%q", left.NextJourneyNode, right.NextJourneyNode))
	}
	if !sameSet(left.ExposedTools, right.ExposedTools) {
		out = append(out, fmt.Sprintf("exposed_tools differ: parmesan=%v parlant=%v", left.ExposedTools, right.ExposedTools))
	}
	if left.SelectedTool != right.SelectedTool {
		out = append(out, fmt.Sprintf("selected_tool differs: parmesan=%q parlant=%q", left.SelectedTool, right.SelectedTool))
	}
	if left.ResponseMode != right.ResponseMode {
		out = append(out, fmt.Sprintf("response_mode differs: parmesan=%q parlant=%q", left.ResponseMode, right.ResponseMode))
	}
	if left.NoMatch != right.NoMatch {
		out = append(out, fmt.Sprintf("no_match differs: parmesan=%t parlant=%t", left.NoMatch, right.NoMatch))
	}
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
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
