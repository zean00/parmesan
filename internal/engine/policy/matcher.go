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
		snapshot := snapshotFromState(state)
		batches := strategy.CreateMatchingBatches(snapshot, strategyGroups[name])
		if strategy.Name() == "generic" {
			var err error
			snapshot, err = m.runGenericMatchingBatches(ctx, state, snapshot, strategyGroups[name], batches)
			if err != nil {
				return nil, err
			}
		} else {
			for _, batch := range batches {
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

type matchingBatchOutcome struct {
	batch    guidelineMatchingBatch
	result   StageResult
	duration time.Duration
	err      error
}

func (m *guidelineMatcher) runGenericMatchingBatches(ctx context.Context, state *matchingState, snapshot matchingSnapshot, items []policy.Guideline, batches []guidelineMatchingBatch) (matchingSnapshot, error) {
	if len(batches) == 0 {
		return snapshot, nil
	}
	parallelNames := map[string]struct{}{
		"observation_match":     {},
		"journey_backtrack":     {},
		"actionable_match":      {},
		"low_criticality_match": {},
	}
	outcomes := make(map[string]matchingBatchOutcome, len(parallelNames))
	var parallel []guidelineMatchingBatch
	var progress guidelineMatchingBatch
	startIndex := 0
	for startIndex < len(batches) {
		name := batches[startIndex].Name()
		if name == "journey_progress" {
			progress = batches[startIndex]
			startIndex++
			continue
		}
		if _, ok := parallelNames[name]; !ok {
			break
		}
		parallel = append(parallel, batches[startIndex])
		startIndex++
	}
	if len(parallel) == 0 {
		for _, batch := range batches {
			next, err := runMatchingBatch(ctx, state, snapshot, items, batch)
			if err != nil {
				return snapshot, err
			}
			snapshot = next
		}
		return snapshot, nil
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(len(parallel))
	for _, batch := range parallel {
		batch := batch
		go func() {
			defer wg.Done()
			start := time.Now()
			result, err := batch.Process(ctx, snapshot)
			mu.Lock()
			outcomes[batch.Name()] = matchingBatchOutcome{batch: batch, result: result, duration: time.Since(start), err: err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, name := range []string{"observation_match", "journey_backtrack", "actionable_match", "low_criticality_match"} {
		outcome, ok := outcomes[name]
		if !ok {
			continue
		}
		if outcome.err != nil {
			return snapshot, outcome.err
		}
		if outcome.result != nil {
			outcome.result.Apply(state)
		}
		recordBatchResult(state, outcome.batch.Name(), outcome.batch.Strategy(), outcome.batch.PromptVersion(), 0, len(items), outcome.duration, outcome.result)
		snapshot = snapshotFromState(state)
	}
	if progress != nil {
		next, err := runMatchingBatch(ctx, state, snapshot, items, progress)
		if err != nil {
			return snapshot, err
		}
		snapshot = next
	}
	for _, batch := range batches[startIndex:] {
		next, err := runMatchingBatch(ctx, state, snapshot, items, batch)
		if err != nil {
			return snapshot, err
		}
		snapshot = next
	}
	return snapshot, nil
}

func runMatchingBatch(ctx context.Context, state *matchingState, snapshot matchingSnapshot, items []policy.Guideline, batch guidelineMatchingBatch) (matchingSnapshot, error) {
	start := time.Now()
	result, err := batch.Process(ctx, snapshot)
	if err != nil {
		return snapshot, err
	}
	if result != nil {
		result.Apply(state)
	}
	recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, len(items), time.Since(start), result)
	return snapshotFromState(state), nil
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
