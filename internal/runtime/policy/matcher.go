package policyruntime

import (
	"context"
	"sort"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
)

type guidelineMatcher struct {
	resolver guidelineMatchingStrategyResolver
}

func newGuidelineMatcher(resolver guidelineMatchingStrategyResolver) *guidelineMatcher {
	return &guidelineMatcher{resolver: resolver}
}

func (m *guidelineMatcher) Run(ctx context.Context, router *model.Router, bundle policy.Bundle, matchCtx MatchingContext, journeyInstances []journey.Instance, catalog []tool.CatalogEntry) (*matchingState, error) {
	state := &matchingState{
		router:            router,
		bundle:            bundle,
		context:           matchCtx,
		catalog:           catalog,
		journeyInstances:  journeyInstances,
		projectedNodes:    projectJourneyNodes(bundle),
		promptSetVersions: map[string]string{},
	}
	state.attention = runPolicyAttentionARQ(matchCtx, bundle, state.projectedNodes)

	guidelineCandidates := append([]policy.Guideline(nil), bundle.Guidelines...)
	for _, item := range bundle.Journeys {
		guidelineCandidates = append(guidelineCandidates, item.Guidelines...)
	}

	strategyGroups := map[string][]policy.Guideline{}
	strategies := map[string]guidelineMatchingStrategy{}
	for _, guideline := range guidelineCandidates {
		strategy := m.resolver.Resolve(guideline)
		if strategy == nil {
			continue
		}
		strategyGroups[strategy.Name()] = append(strategyGroups[strategy.Name()], guideline)
		strategies[strategy.Name()] = strategy
	}
	names := make([]string, 0, len(strategyGroups))
	for name := range strategyGroups {
		names = append(names, name)
	}
	if len(names) == 0 && m.resolver != nil {
		if strategy := m.resolver.Resolve(policy.Guideline{}); strategy != nil {
			name := strategy.Name()
			names = append(names, name)
			strategies[name] = strategy
			strategyGroups[name] = nil
		}
	}
	sort.Strings(names)

	for _, name := range names {
		strategy := strategies[name]
		for _, batch := range strategy.CreateMatchingBatches(state, strategyGroups[name]) {
			if err := batch.Process(ctx, state); err != nil {
				return nil, err
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), state)
		}
		for _, batch := range strategy.CreateResponseAnalysisBatches(state) {
			if err := batch.Process(ctx, state); err != nil {
				return nil, err
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), state)
		}
	}

	sortMatches(state.guidelineMatches)
	sortGuidelines(state.matchedGuidelines, state.guidelineMatches)
	state.matchedGuidelines = dedupeGuidelines(state.matchedGuidelines)
	return state, nil
}

func appendLowCriticality(ctx context.Context, state *matchingState, items []policy.Guideline) ([]Match, []policy.Guideline) {
	matches, guidelines := runLowCriticalityARQ(ctx, state.router, state.context, items)
	state.guidelineMatches = append(state.guidelineMatches, matches...)
	state.matchedGuidelines = append(state.matchedGuidelines, guidelines...)
	return matches, state.matchedGuidelines
}

func recordBatchResult(state *matchingState, name string, strategy string, promptVersion string, current *matchingState) {
	state.promptSetVersions[name] = promptVersion
	state.batchResults = append(state.batchResults, BatchResult{
		Name:          name,
		Strategy:      strategy,
		PromptVersion: promptVersion,
		Output:        batchOutputFor(name, current),
	})
}

func batchOutputFor(name string, state *matchingState) map[string]any {
	switch name {
	case "observation_match":
		return map[string]any{"matches": state.observationMatches}
	case "journey_backtrack":
		return map[string]any{
			"action":       state.backtrackDecision.Action,
			"backtrack_to": state.backtrackDecision.BacktrackTo,
			"rationale":    state.backtrackDecision.Rationale,
			"missing":      state.backtrackDecision.Missing,
		}
	case "journey_progress":
		return map[string]any{
			"action":        state.journeyDecision.Action,
			"current_state": state.journeyDecision.CurrentState,
			"next_state":    state.journeyDecision.NextState,
			"backtrack_to":  state.journeyDecision.BacktrackTo,
			"rationale":     state.journeyDecision.Rationale,
			"missing":       state.journeyDecision.Missing,
		}
	case "actionable_match":
		return map[string]any{"matches": state.guidelineMatches}
	case "custom_actionable_match":
		return map[string]any{"matches": state.guidelineMatches}
	case "low_criticality_match":
		return map[string]any{"matches": state.lowCriticality}
	case "customer_dependency":
		return map[string]any{"decisions": state.customerDecisions}
	case "previously_applied":
		return map[string]any{"reapply": state.reapplyDecisions}
	case "relationship_resolution":
		return map[string]any{"suppressed": state.suppressedGuidelines}
	case "disambiguation":
		return map[string]any{"prompt": state.disambiguationPrompt}
	case "response_analysis":
		return map[string]any{
			"analyzed_guidelines":  state.responseAnalysis.AnalyzedGuidelines,
			"needs_revision":       state.responseAnalysis.NeedsRevision,
			"needs_strict_mode":    state.responseAnalysis.NeedsStrictMode,
			"recommended_template": state.responseAnalysis.RecommendedTemplate,
			"rationale":            state.responseAnalysis.Rationale,
		}
	default:
		return map[string]any{}
	}
}
