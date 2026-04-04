package policyruntime

import (
	"context"
	"strings"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
)

type guidelineMatchingBatch interface {
	Name() string
	Strategy() string
	PromptVersion() string
	Process(context.Context, *matchingState) error
}

type responseAnalysisBatch interface {
	Name() string
	Strategy() string
	PromptVersion() string
	Process(context.Context, *matchingState) error
}

type guidelineMatchingStrategy interface {
	Name() string
	CreateMatchingBatches(*matchingState, []policy.Guideline) []guidelineMatchingBatch
	CreateResponseAnalysisBatches(*matchingState) []responseAnalysisBatch
	TransformMatches(*matchingState)
}

type guidelineMatchingStrategyResolver interface {
	Resolve(policy.Guideline) guidelineMatchingStrategy
}

type strategyResolver struct {
	defaultStrategy guidelineMatchingStrategy
	named           map[string]guidelineMatchingStrategy
	guidelineIDs    map[string]string
	tagOverrides    map[string]string
}

func newStrategyResolver(defaultStrategy guidelineMatchingStrategy) *strategyResolver {
	resolver := &strategyResolver{
		defaultStrategy: defaultStrategy,
		named:           map[string]guidelineMatchingStrategy{},
		guidelineIDs:    map[string]string{},
		tagOverrides:    map[string]string{},
	}
	if defaultStrategy != nil {
		resolver.named[defaultStrategy.Name()] = defaultStrategy
	}
	return resolver
}

func (r *strategyResolver) Register(strategy guidelineMatchingStrategy) {
	if strategy == nil {
		return
	}
	r.named[strategy.Name()] = strategy
}

func (r *strategyResolver) Resolve(item policy.Guideline) guidelineMatchingStrategy {
	for _, tag := range item.Tags {
		if strategy, ok := r.named[tag]; ok {
			return strategy
		}
	}
	if name := r.guidelineIDs[item.ID]; name != "" {
		if strategy, ok := r.named[name]; ok {
			return strategy
		}
	}
	for _, tag := range item.Tags {
		if name := r.tagOverrides[tag]; name != "" {
			if strategy, ok := r.named[name]; ok {
				return strategy
			}
		}
	}
	if name := item.Matcher; name != "" {
		if strategy, ok := r.named[name]; ok {
			return strategy
		}
	}
	return r.defaultStrategy
}

type matchingState struct {
	router           *model.Router
	bundle           policy.Bundle
	context          MatchingContext
	catalog          []tool.CatalogEntry
	journeyInstances []journey.Instance

	projectedNodes       []ProjectedJourneyNode
	attention            PolicyAttention
	observationMatches   []Match
	matchedObservations  []policy.Observation
	activeJourney        *policy.Journey
	activeJourneyState   *policy.JourneyNode
	journeyInstance      *journey.Instance
	backtrackDecision    JourneyDecision
	journeyDecision      JourneyDecision
	guidelineMatches     []Match
	matchedGuidelines    []policy.Guideline
	lowCriticality       []Match
	reapplyDecisions     []ReapplyDecision
	customerDecisions    []CustomerDependencyDecision
	suppressedGuidelines []SuppressedGuideline
	resolutionRecords    []ResolutionRecord
	disambiguationPrompt string
	candidateTemplates   []policy.Template
	responseAnalysis     ResponseAnalysis
	exposedTools         []string
	toolApprovals        map[string]string
	toolPlan             ToolCallPlan
	toolDecision         ToolDecision
	batchResults         []BatchResult
	promptSetVersions    map[string]string
}

type genericStrategy struct{}

type customStrategy struct{}

func (genericStrategy) Name() string {
	return "generic"
}

func (customStrategy) Name() string {
	return "custom"
}

func (genericStrategy) TransformMatches(state *matchingState) {
	if state == nil {
		return
	}
	sortMatches(state.guidelineMatches)
	state.guidelineMatches = dedupeMatches(state.guidelineMatches)
	sortGuidelines(state.matchedGuidelines, state.guidelineMatches)
	state.matchedGuidelines = dedupeGuidelines(state.matchedGuidelines)
	state.suppressedGuidelines = dedupeSuppressedGuidelines(state.suppressedGuidelines)
	state.resolutionRecords = dedupeResolutionRecords(state.resolutionRecords)
}

func (customStrategy) TransformMatches(state *matchingState) {
	genericStrategy{}.TransformMatches(state)
}

func (genericStrategy) CreateMatchingBatches(state *matchingState, items []policy.Guideline) []guidelineMatchingBatch {
	regular, low := splitLowCriticalityGuidelines(items)
	return []guidelineMatchingBatch{
		makeBatch("observation_match", "generic", promptVersion("observation_match"), func(ctx context.Context, state *matchingState) error {
			state.observationMatches, state.matchedObservations = runObservationARQ(ctx, state.router, state.context, state.bundle.Observations)
			return nil
		}),
		makeBatch("journey_backtrack", "generic", promptVersion("journey_backtrack"), func(ctx context.Context, state *matchingState) error {
			state.activeJourney, state.activeJourneyState, state.journeyInstance = resolveJourney(state.bundle, state.journeyInstances, state.context)
			state.backtrackDecision = runJourneyBacktrackARQ(ctx, state.router, state.context, state.activeJourney, state.activeJourneyState, state.journeyInstance)
			return nil
		}),
		makeBatch("journey_progress", "generic", promptVersion("journey_progress"), func(ctx context.Context, state *matchingState) error {
			state.journeyDecision = runJourneyProgressARQ(ctx, state.router, state.context, state.activeJourney, state.activeJourneyState, state.journeyInstance, state.backtrackDecision)
			return nil
		}),
		makeBatch("actionable_match", "generic", promptVersion("actionable_match"), func(ctx context.Context, state *matchingState) error {
			state.guidelineMatches, state.matchedGuidelines = runActionableARQ(ctx, state.router, state.context, regular)
			return nil
		}),
		makeBatch("low_criticality_match", "generic", promptVersion("low_criticality_match"), func(ctx context.Context, state *matchingState) error {
			state.lowCriticality, state.matchedGuidelines = appendLowCriticality(ctx, state, low)
			return nil
		}),
		makeBatch("customer_dependency", "generic", promptVersion("customer_dependency"), func(_ context.Context, state *matchingState) error {
			state.customerDecisions, state.matchedGuidelines = runCustomerDependentARQ(state.context, state.matchedGuidelines)
			return nil
		}),
		makeBatch("previously_applied", "generic", promptVersion("previously_applied"), func(_ context.Context, state *matchingState) error {
			state.reapplyDecisions, state.matchedGuidelines = runPreviouslyAppliedARQ(state.context, state.matchedGuidelines, state.guidelineMatches)
			return nil
		}),
		makeBatch("relationship_resolution", "generic", promptVersion("relationship_resolution"), func(_ context.Context, state *matchingState) error {
			resolved := resolveRelationships(state.bundle, state.context, state.matchedObservations, state.guidelineMatches, state.matchedGuidelines, state.activeJourney, state.activeJourneyState)
			state.matchedGuidelines = resolved.guidelines
			state.suppressedGuidelines = resolved.suppressed
			state.disambiguationPrompt = resolved.disambiguation
			state.resolutionRecords = resolved.resolutions
			state.activeJourney = resolved.activeJourney
			if state.activeJourney == nil {
				state.activeJourneyState = nil
			}
			return nil
		}),
		makeBatch("disambiguation", "generic", promptVersion("disambiguation"), func(ctx context.Context, state *matchingState) error {
			state.disambiguationPrompt = runDisambiguationARQ(ctx, state.router, state.context, state.matchedGuidelines, state.disambiguationPrompt)
			return nil
		}),
	}
}

func (genericStrategy) CreateResponseAnalysisBatches(state *matchingState) []responseAnalysisBatch {
	return []responseAnalysisBatch{
		makeResponseBatch("response_analysis", "generic", promptVersion("response_analysis"), func(ctx context.Context, state *matchingState) error {
			state.candidateTemplates = collectTemplates(state.bundle, state.activeJourney, state.activeJourneyState, state.context)
			mode := modeOrDefault(state.bundle.CompositionMode, state.candidateTemplates)
			state.responseAnalysis = analyzeResponsePlan(ctx, state.router, state.context, responseAnalysisGuidelines(state.bundle, state.context, state.matchedGuidelines), state.candidateTemplates, mode, state.bundle.NoMatch)
			return nil
		}),
	}
}

func (customStrategy) CreateMatchingBatches(state *matchingState, items []policy.Guideline) []guidelineMatchingBatch {
	return []guidelineMatchingBatch{
		makeBatch("custom_actionable_match", "custom", promptVersion("custom_actionable_match"), func(ctx context.Context, state *matchingState) error {
			matches, guidelines := runActionableARQ(ctx, state.router, state.context, items)
			state.guidelineMatches = append(state.guidelineMatches, matches...)
			state.matchedGuidelines = append(state.matchedGuidelines, guidelines...)
			sortMatches(state.guidelineMatches)
			sortGuidelines(state.matchedGuidelines, state.guidelineMatches)
			state.matchedGuidelines = dedupeGuidelines(state.matchedGuidelines)
			return nil
		}),
	}
}

func (customStrategy) CreateResponseAnalysisBatches(state *matchingState) []responseAnalysisBatch {
	return nil
}

type batchFunc struct {
	name          string
	strategy      string
	promptVersion string
	run           func(context.Context, *matchingState) error
}

func makeBatch(name string, strategy string, promptVersion string, run func(context.Context, *matchingState) error) batchFunc {
	return batchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b batchFunc) Name() string          { return b.name }
func (b batchFunc) Strategy() string      { return b.strategy }
func (b batchFunc) PromptVersion() string { return b.promptVersion }
func (b batchFunc) Process(ctx context.Context, state *matchingState) error {
	return b.run(ctx, state)
}

type responseBatchFunc struct {
	name          string
	strategy      string
	promptVersion string
	run           func(context.Context, *matchingState) error
}

func makeResponseBatch(name string, strategy string, promptVersion string, run func(context.Context, *matchingState) error) responseBatchFunc {
	return responseBatchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b responseBatchFunc) Name() string          { return b.name }
func (b responseBatchFunc) Strategy() string      { return b.strategy }
func (b responseBatchFunc) PromptVersion() string { return b.promptVersion }
func (b responseBatchFunc) Process(ctx context.Context, state *matchingState) error {
	return b.run(ctx, state)
}

func promptVersion(stage string) string {
	return stage + ".v1"
}

func dedupeSuppressedGuidelines(items []SuppressedGuideline) []SuppressedGuideline {
	seen := map[string]SuppressedGuideline{}
	order := make([]string, 0, len(items))
	for _, item := range items {
		if existing, ok := seen[item.ID]; ok {
			existing.RelatedIDs = dedupe(append(existing.RelatedIDs, item.RelatedIDs...))
			if existing.Reason == "" {
				existing.Reason = item.Reason
			}
			seen[item.ID] = existing
			continue
		}
		copied := item
		copied.RelatedIDs = dedupe(append([]string(nil), item.RelatedIDs...))
		seen[item.ID] = copied
		order = append(order, item.ID)
	}
	out := make([]SuppressedGuideline, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func dedupeResolutionRecords(items []ResolutionRecord) []ResolutionRecord {
	seen := map[string]ResolutionRecord{}
	order := make([]string, 0, len(items))
	keyFor := func(item ResolutionRecord) string {
		return item.EntityID + "|" + string(item.Kind) + "|" + strings.Join(item.Details.TargetIDs, ",")
	}
	for _, item := range items {
		key := keyFor(item)
		if _, ok := seen[key]; ok {
			continue
		}
		copied := item
		copied.Details.TargetIDs = dedupe(append([]string(nil), item.Details.TargetIDs...))
		seen[key] = copied
		order = append(order, key)
	}
	out := make([]ResolutionRecord, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	return out
}
