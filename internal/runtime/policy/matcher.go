package policyruntime

import (
	"context"
	"sort"
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
		snapshot := snapshotFromState(state)
		for _, batch := range strategy.CreateMatchingBatches(snapshot, strategyGroups[name]) {
			start := time.Now()
			result, err := batch.Process(ctx, snapshot)
			if err != nil {
				return nil, err
			}
			if result != nil {
				result.Apply(state)
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, len(strategyGroups[name]), time.Since(start), result)
			snapshot = snapshotFromState(state)
		}
		start := time.Now()
		snapshot = snapshotFromState(state)
		if result := strategy.TransformMatches(snapshot); result != nil {
			result.Apply(state)
			recordBatchResult(state, "match_finalize", strategy.Name(), promptVersion("match_finalize"), 0, len(state.matchFinalizeStage.MatchedGuidelines), time.Since(start), result)
		} else {
			recordBatchResult(state, "match_finalize", strategy.Name(), promptVersion("match_finalize"), 0, len(state.matchFinalizeStage.MatchedGuidelines), time.Since(start), nil)
		}
		snapshot = snapshotFromState(state)
		for _, batch := range strategy.CreateResponseAnalysisBatches(snapshot) {
			start := time.Now()
			result, err := batch.Process(ctx, snapshot)
			if err != nil {
				return nil, err
			}
			if result != nil {
				result.Apply(state)
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, len(state.matchFinalizeStage.MatchedGuidelines), time.Since(start), result)
			snapshot = snapshotFromState(state)
		}
	}

	sortMatches(state.matchFinalizeStage.GuidelineMatches)
	sortGuidelines(state.matchFinalizeStage.MatchedGuidelines, state.matchFinalizeStage.GuidelineMatches)
	state.matchFinalizeStage.MatchedGuidelines = dedupeGuidelines(state.matchFinalizeStage.MatchedGuidelines)
	return state, nil
}

func recordBatchResult(state *matchingState, name string, strategy string, promptVersion string, retryCount int, batchSize int, duration time.Duration, result StageResult) {
	state.promptSetVersions[name] = promptVersion
	output := map[string]any{}
	if result != nil {
		output = result.BatchOutput()
	}
	state.batchResults = append(state.batchResults, BatchResult{
		Name:          name,
		Strategy:      strategy,
		PromptVersion: promptVersion,
		BatchSize:     batchSize,
		RetryCount:    retryCount,
		DurationMS:    duration.Milliseconds(),
		Output:        output,
	})
}
