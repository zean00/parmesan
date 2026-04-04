package policyruntime

import (
	"context"
	"sort"
	"sync"
	"time"

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
		if strategy.Name() == "generic" {
			if err := runGenericStrategyConcurrently(ctx, state, strategyGroups[name]); err != nil {
				return nil, err
			}
			continue
		}
		for _, batch := range strategy.CreateMatchingBatches(state, strategyGroups[name]) {
			start := time.Now()
			if err := batch.Process(ctx, state); err != nil {
				return nil, err
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, len(strategyGroups[name]), time.Since(start), state)
		}
		start := time.Now()
		strategy.TransformMatches(state)
		recordBatchResult(state, "match_finalize", strategy.Name(), promptVersion("match_finalize"), 0, len(state.matchedGuidelines), time.Since(start), state)
		for _, batch := range strategy.CreateResponseAnalysisBatches(state) {
			start := time.Now()
			if err := batch.Process(ctx, state); err != nil {
				return nil, err
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, len(state.matchedGuidelines), time.Since(start), state)
		}
	}

	sortMatches(state.guidelineMatches)
	sortGuidelines(state.matchedGuidelines, state.guidelineMatches)
	state.matchedGuidelines = dedupeGuidelines(state.matchedGuidelines)
	return state, nil
}

func runGenericStrategyConcurrently(ctx context.Context, state *matchingState, items []policy.Guideline) error {
	regular, low := splitLowCriticalityGuidelines(items)

	type stageResult struct {
		name          string
		promptVersion string
		batchSize     int
		duration      time.Duration
		err           error
		apply         func(*matchingState)
	}

	resultsCh := make(chan stageResult, 4)
	var wg sync.WaitGroup
	run := func(name string, version string, batchSize int, fn func() stageResult) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultsCh <- fn()
		}()
	}

	run("observation_match", promptVersion("observation_match"), len(state.bundle.Observations), func() stageResult {
		start := time.Now()
		matches, observations := runObservationARQ(ctx, state.router, state.context, state.bundle.Observations)
		return stageResult{
			name:          "observation_match",
			promptVersion: promptVersion("observation_match"),
			batchSize:     len(state.bundle.Observations),
			duration:      time.Since(start),
			apply: func(s *matchingState) {
				s.observationMatches = matches
				s.matchedObservations = observations
			},
		}
	})
	run("journey_phase", promptVersion("journey_progress"), len(state.projectedNodes), func() stageResult {
		start := time.Now()
		activeJourney, activeState, instance := resolveJourney(state.bundle, state.journeyInstances, state.context)
		backtrack := runJourneyBacktrackARQ(ctx, state.router, state.context, activeJourney, activeState, instance)
		progress := runJourneyProgressARQ(ctx, state.router, state.context, activeJourney, activeState, instance, backtrack)
		return stageResult{
			name:          "journey_progress",
			promptVersion: promptVersion("journey_progress"),
			batchSize:     len(state.projectedNodes),
			duration:      time.Since(start),
			apply: func(s *matchingState) {
				s.activeJourney = activeJourney
				s.activeJourneyState = activeState
				s.journeyInstance = instance
				s.backtrackDecision = backtrack
				s.journeyDecision = progress
			},
		}
	})
	run("actionable_match", promptVersion("actionable_match"), len(regular), func() stageResult {
		start := time.Now()
		matches, guidelines := runActionableARQ(ctx, state.router, state.context, regular)
		return stageResult{
			name:          "actionable_match",
			promptVersion: promptVersion("actionable_match"),
			batchSize:     len(regular),
			duration:      time.Since(start),
			apply: func(s *matchingState) {
				s.guidelineMatches = matches
				s.matchedGuidelines = guidelines
			},
		}
	})
	run("low_criticality_match", promptVersion("low_criticality_match"), len(low), func() stageResult {
		start := time.Now()
		matches, guidelines := runLowCriticalityARQ(ctx, state.router, state.context, low)
		return stageResult{
			name:          "low_criticality_match",
			promptVersion: promptVersion("low_criticality_match"),
			batchSize:     len(low),
			duration:      time.Since(start),
			apply: func(s *matchingState) {
				s.lowCriticality = matches
				s.guidelineMatches = append(s.guidelineMatches, matches...)
				s.matchedGuidelines = append(s.matchedGuidelines, guidelines...)
			},
		}
	})

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var initial []stageResult
	for result := range resultsCh {
		if result.err != nil {
			return result.err
		}
		initial = append(initial, result)
	}
	sort.SliceStable(initial, func(i, j int) bool { return initial[i].name < initial[j].name })
	for _, result := range initial {
		if result.apply != nil {
			result.apply(state)
		}
		if result.name == "journey_progress" {
			recordBatchResult(state, "journey_backtrack", "generic", promptVersion("journey_backtrack"), 0, result.batchSize, result.duration/2, state)
		}
		recordBatchResult(state, result.name, "generic", result.promptVersion, 0, result.batchSize, result.duration, state)
	}

	sequential := []struct {
		name string
		size int
		run  func()
	}{
		{
			name: "customer_dependency",
			size: len(state.matchedGuidelines),
			run: func() {
				state.customerDecisions, state.matchedGuidelines = runCustomerDependentARQ(state.context, state.matchedGuidelines)
			},
		},
		{
			name: "previously_applied",
			size: len(state.matchedGuidelines),
			run: func() {
				state.reapplyDecisions, state.matchedGuidelines = runPreviouslyAppliedARQ(state.context, state.matchedGuidelines, state.guidelineMatches)
			},
		},
		{
			name: "relationship_resolution",
			size: len(state.matchedGuidelines),
			run: func() {
				resolved := resolveRelationships(state.bundle, state.context, state.matchedObservations, state.guidelineMatches, state.matchedGuidelines, state.activeJourney, state.activeJourneyState)
				state.matchedGuidelines = resolved.guidelines
				state.suppressedGuidelines = resolved.suppressed
				state.disambiguationPrompt = resolved.disambiguation
				state.resolutionRecords = resolved.resolutions
				state.activeJourney = resolved.activeJourney
				if state.activeJourney == nil {
					state.activeJourneyState = nil
				}
			},
		},
		{
			name: "disambiguation",
			size: len(state.matchedGuidelines),
			run: func() {
				state.disambiguationPrompt = runDisambiguationARQ(ctx, state.router, state.context, state.matchedGuidelines, state.disambiguationPrompt)
			},
		},
	}
	for _, step := range sequential {
		start := time.Now()
		step.run()
		recordBatchResult(state, step.name, "generic", promptVersion(step.name), 0, step.size, time.Since(start), state)
	}
	start := time.Now()
	genericStrategy{}.TransformMatches(state)
	recordBatchResult(state, "match_finalize", "generic", promptVersion("match_finalize"), 0, len(state.matchedGuidelines), time.Since(start), state)
	responseStart := time.Now()
	state.candidateTemplates = collectTemplates(state.bundle, state.activeJourney, state.activeJourneyState, state.context)
	mode := modeOrDefault(state.bundle.CompositionMode, state.candidateTemplates)
	analysisGuidelines := responseAnalysisGuidelines(state.bundle, state.context, state.matchedGuidelines)
	state.responseAnalysis = analyzeResponsePlan(ctx, state.router, state.context, analysisGuidelines, state.candidateTemplates, mode, state.bundle.NoMatch)
	recordBatchResult(state, "response_analysis", "generic", promptVersion("response_analysis"), 0, len(analysisGuidelines), time.Since(responseStart), state)
	return nil
}

func appendLowCriticality(ctx context.Context, state *matchingState, items []policy.Guideline) ([]Match, []policy.Guideline) {
	matches, guidelines := runLowCriticalityARQ(ctx, state.router, state.context, items)
	state.guidelineMatches = append(state.guidelineMatches, matches...)
	state.matchedGuidelines = append(state.matchedGuidelines, guidelines...)
	return matches, state.matchedGuidelines
}

func recordBatchResult(state *matchingState, name string, strategy string, promptVersion string, retryCount int, batchSize int, duration time.Duration, current *matchingState) {
	state.promptSetVersions[name] = promptVersion
	state.batchResults = append(state.batchResults, BatchResult{
		Name:          name,
		Strategy:      strategy,
		PromptVersion: promptVersion,
		BatchSize:     batchSize,
		RetryCount:    retryCount,
		DurationMS:    duration.Milliseconds(),
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
		return map[string]any{
			"suppressed":  state.suppressedGuidelines,
			"resolutions": state.resolutionRecords,
		}
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
	case "tool_plan":
		return map[string]any{
			"candidates":         state.toolPlan.Candidates,
			"selected_tool":      state.toolPlan.SelectedTool,
			"overlapping_groups": state.toolPlan.OverlappingGroups,
			"rationale":          state.toolPlan.Rationale,
		}
	default:
		return map[string]any{}
	}
}
