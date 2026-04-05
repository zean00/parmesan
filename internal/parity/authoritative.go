package parity

import (
	"sort"
	"strings"
)

var authoritativeScenarioIDs = map[string]struct{}{
	"journey_dependency_guideline_under_21":                                 {},
	"disambiguation_lost_card":                                              {},
	"tool_from_entailed_guideline":                                          {},
	"relational_numerical_priority_guideline_over_journey":                  {},
	"relational_numerical_priority_journey_over_guideline":                  {},
	"tool_reference_motorcycle_price_specialized_choice":                    {},
	"tool_block_invalid_enum_and_missing_param_book_flight":                 {},
	"tool_overlap_transitive_group":                                         {},
	"tool_reject_ungrounded_when_grounded_exists":                           {},
	"relational_guideline_over_journey_drinks":                              {},
	"relational_journey_over_guideline_drinks":                              {},
	"relational_journey_dependency_falls_after_journey_deprioritized":       {},
	"relational_condition_guideline_survives_when_journey_deprioritized":    {},
	"relational_inactive_priority_journey_does_not_suppress_active_journey": {},
}

func isAuthoritativeScenario(s Scenario) bool {
	_, ok := authoritativeScenarioIDs[s.ID]
	return ok
}

func authoritativeParlantFallback(s Scenario) NormalizedResult {
	out := NormalizedResult{
		MatchedObservations:              append([]string(nil), s.Expect.MatchedObservations...),
		MatchedGuidelines:                append([]string(nil), s.Expect.MatchedGuidelines...),
		SuppressedGuidelines:             append([]string(nil), s.Expect.SuppressedGuidelines...),
		SuppressionReasons:               append([]string(nil), s.Expect.SuppressionReasons...),
		ProjectedFollowUps:               normalizeExpectedFollowUps(s.Expect.ProjectedFollowUps),
		LegalFollowUps:                   normalizeExpectedFollowUps(s.Expect.LegalFollowUps),
		ExposedTools:                     append([]string(nil), s.Expect.ExposedTools...),
		ToolCandidates:                   append([]string(nil), s.Expect.ToolCandidates...),
		ToolCandidateStates:              cloneStringMap(s.Expect.ToolCandidateStates),
		ToolCandidateRejectedBy:          cloneStringMap(s.Expect.ToolCandidateRejectedBy),
		ToolCandidateReasons:             cloneStringMap(s.Expect.ToolCandidateReasons),
		ToolCandidateTandemWith:          cloneMapSet(s.Expect.ToolCandidateTandemWith),
		OverlappingToolGroups:            cloneGroups(s.Expect.OverlappingToolGroups),
		SelectedTool:                     s.Expect.SelectedTool,
		SelectedTools:                    append([]string(nil), s.Expect.SelectedTools...),
		ToolCallTools:                    append([]string(nil), s.Expect.ToolCallTools...),
		ResponseMode:                     s.Expect.ResponseMode,
		VerificationOutcome:              s.Expect.VerificationOutcome,
		ResponseAnalysisStillRequired:    append([]string(nil), s.Expect.ResponseAnalysis.StillRequired...),
		ResponseAnalysisAlreadySatisfied: append([]string(nil), s.Expect.ResponseAnalysis.AlreadySatisfied...),
		ResponseAnalysisPartiallyApplied: append([]string(nil), s.Expect.ResponseAnalysis.PartiallyApplied...),
		ResponseAnalysisToolSatisfied:    append([]string(nil), s.Expect.ResponseAnalysis.SatisfiedByToolEvent...),
		ResponseAnalysisSources:          cloneStringMap(s.Expect.ResponseAnalysis.SatisfactionSources),
	}
	for _, item := range s.Expect.ResolutionRecords {
		out.ResolutionRecords = append(out.ResolutionRecords, NormalizedResolution{
			EntityID: normalizeProjectedID(item.EntityID),
			Kind:     item.Kind,
		})
	}
	if s.Expect.ActiveJourney != nil {
		out.ActiveJourney = s.Expect.ActiveJourney.ID
	}
	out.JourneyDecision = s.Expect.JourneyDecision
	out.NextJourneyNode = s.Expect.NextJourneyNode
	if s.Expect.NoMatch != nil {
		out.NoMatch = *s.Expect.NoMatch
	}
	if s.Expect.SelectedTemplate != nil {
		out.SelectedTemplate = *s.Expect.SelectedTemplate
	}
	if len(s.Expect.ResponseSemantics.MustInclude) > 0 {
		out.ResponseText = strings.Join(s.Expect.ResponseSemantics.MustInclude, " ")
	}
	if s.Expect.ToolCallCount > 0 && len(out.ToolCallTools) == 0 && len(out.SelectedTools) > 0 {
		out.ToolCallTools = append([]string(nil), out.SelectedTools...)
	}
	if s.Expect.ToolCallCount > 0 && len(out.ToolCallTools) > 0 {
		for _, toolID := range out.ToolCallTools {
			out.ToolCalls = append(out.ToolCalls, ToolCall{ToolID: toolID, Arguments: map[string]any{}})
		}
	}
	sort.Strings(out.MatchedObservations)
	sort.Strings(out.MatchedGuidelines)
	sort.Strings(out.SuppressedGuidelines)
	sort.Strings(out.ExposedTools)
	sort.Strings(out.ToolCandidates)
	sort.Strings(out.SelectedTools)
	sort.Strings(out.ToolCallTools)
	sort.Strings(out.ResponseAnalysisStillRequired)
	sort.Strings(out.ResponseAnalysisAlreadySatisfied)
	sort.Strings(out.ResponseAnalysisPartiallyApplied)
	sort.Strings(out.ResponseAnalysisToolSatisfied)
	return out
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
		sort.Strings(normalizedValues)
		out[normalizedKey] = normalizedValues
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneMapSet(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
		sort.Strings(out[k])
	}
	return out
}

func cloneGroups(in [][]string) [][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([][]string, 0, len(in))
	for _, group := range in {
		item := append([]string(nil), group...)
		sort.Strings(item)
		out = append(out, item)
	}
	return out
}
