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
	Process(context.Context, matchingSnapshot) (stateMutation, error)
}

type responseAnalysisBatch interface {
	Name() string
	Strategy() string
	PromptVersion() string
	Process(context.Context, matchingSnapshot) (stateMutation, error)
}

type stateMutation func(*matchingState)

type guidelineMatchingStrategy interface {
	Name() string
	CreateMatchingBatches(matchingSnapshot, []policy.Guideline) []guidelineMatchingBatch
	CreateResponseAnalysisBatches(matchingSnapshot) []responseAnalysisBatch
	TransformMatches(matchingSnapshot) stateMutation
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

type matchingSnapshot struct {
	router              *model.Router
	bundle              policy.Bundle
	context             MatchingContext
	catalog             []tool.CatalogEntry
	journeyInstances    []journey.Instance
	projectedNodes      []ProjectedJourneyNode
	attention           PolicyAttention
	observationMatches  []Match
	matchedObservations []policy.Observation
	activeJourney       *policy.Journey
	activeJourneyState  *policy.JourneyNode
	journeyInstance     *journey.Instance
	backtrackDecision   JourneyDecision
	journeyDecision     JourneyDecision
	guidelineMatches    []Match
	matchedGuidelines   []policy.Guideline
	lowCriticality      []Match
	reapplyDecisions    []ReapplyDecision
	customerDecisions   []CustomerDependencyDecision
	suppressedGuidelines []SuppressedGuideline
	resolutionRecords   []ResolutionRecord
	disambiguationPrompt string
	candidateTemplates  []policy.Template
	responseAnalysis    ResponseAnalysis
	exposedTools        []string
	toolApprovals       map[string]string
	toolPlan            ToolCallPlan
	toolDecision        ToolDecision
}

func snapshotFromState(state *matchingState) matchingSnapshot {
	if state == nil {
		return matchingSnapshot{}
	}
	return matchingSnapshot{
		router:               state.router,
		bundle:               state.bundle,
		context:              state.context,
		catalog:              append([]tool.CatalogEntry(nil), state.catalog...),
		journeyInstances:     append([]journey.Instance(nil), state.journeyInstances...),
		projectedNodes:       append([]ProjectedJourneyNode(nil), state.projectedNodes...),
		attention:            state.attention,
		observationMatches:   append([]Match(nil), state.observationMatches...),
		matchedObservations:  append([]policy.Observation(nil), state.matchedObservations...),
		activeJourney:        state.activeJourney,
		activeJourneyState:   state.activeJourneyState,
		journeyInstance:      state.journeyInstance,
		backtrackDecision:    state.backtrackDecision,
		journeyDecision:      state.journeyDecision,
		guidelineMatches:     append([]Match(nil), state.guidelineMatches...),
		matchedGuidelines:    append([]policy.Guideline(nil), state.matchedGuidelines...),
		lowCriticality:       append([]Match(nil), state.lowCriticality...),
		reapplyDecisions:     append([]ReapplyDecision(nil), state.reapplyDecisions...),
		customerDecisions:    append([]CustomerDependencyDecision(nil), state.customerDecisions...),
		suppressedGuidelines: append([]SuppressedGuideline(nil), state.suppressedGuidelines...),
		resolutionRecords:    append([]ResolutionRecord(nil), state.resolutionRecords...),
		disambiguationPrompt: state.disambiguationPrompt,
		candidateTemplates:   append([]policy.Template(nil), state.candidateTemplates...),
		responseAnalysis:     state.responseAnalysis,
		exposedTools:         append([]string(nil), state.exposedTools...),
		toolApprovals:        cloneStringMap(state.toolApprovals),
		toolPlan:             state.toolPlan,
		toolDecision:         state.toolDecision,
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

type genericStrategy struct{}

type customStrategy struct{}

func (genericStrategy) Name() string {
	return "generic"
}

func (customStrategy) Name() string {
	return "custom"
}

func (genericStrategy) TransformMatches(snapshot matchingSnapshot) stateMutation {
	if snapshot.bundle.ID == "" && snapshot.context.SessionID == "" && len(snapshot.guidelineMatches) == 0 && len(snapshot.matchedGuidelines) == 0 {
		return nil
	}
	guidelineMatches := append([]Match(nil), snapshot.guidelineMatches...)
	matchedGuidelines := append([]policy.Guideline(nil), snapshot.matchedGuidelines...)
	suppressed := append([]SuppressedGuideline(nil), snapshot.suppressedGuidelines...)
	resolutions := append([]ResolutionRecord(nil), snapshot.resolutionRecords...)
	return func(s *matchingState) {
		sortMatches(guidelineMatches)
		guidelineMatches = dedupeMatches(guidelineMatches)
		sortGuidelines(matchedGuidelines, guidelineMatches)
		matchedGuidelines = dedupeGuidelines(matchedGuidelines)
		suppressed = dedupeSuppressedGuidelines(suppressed)
		resolutions = dedupeResolutionRecords(resolutions)
		s.guidelineMatches = guidelineMatches
		s.matchedGuidelines = matchedGuidelines
		s.suppressedGuidelines = suppressed
		s.resolutionRecords = resolutions
	}
}

func (customStrategy) TransformMatches(snapshot matchingSnapshot) stateMutation {
	return genericStrategy{}.TransformMatches(snapshot)
}

func (genericStrategy) CreateMatchingBatches(_ matchingSnapshot, items []policy.Guideline) []guidelineMatchingBatch {
	regular, low := splitLowCriticalityGuidelines(items)
	return []guidelineMatchingBatch{
		makeBatch("observation_match", "generic", promptVersion("observation_match"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			matches, observations := runObservationARQ(ctx, snapshot.router, snapshot.context, snapshot.bundle.Observations)
			return func(s *matchingState) {
				s.observationMatches, s.matchedObservations = matches, observations
			}, nil
		}),
		makeBatch("journey_backtrack", "generic", promptVersion("journey_backtrack"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			activeJourney, activeJourneyState, instance := resolveJourney(snapshot.bundle, snapshot.journeyInstances, snapshot.context)
			backtrack := runJourneyBacktrackARQ(ctx, snapshot.router, snapshot.context, activeJourney, activeJourneyState, instance)
			return func(s *matchingState) {
				s.activeJourney, s.activeJourneyState, s.journeyInstance = activeJourney, activeJourneyState, instance
				s.backtrackDecision = backtrack
			}, nil
		}),
		makeBatch("journey_progress", "generic", promptVersion("journey_progress"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			decision := runJourneyProgressARQ(ctx, snapshot.router, snapshot.context, snapshot.activeJourney, snapshot.activeJourneyState, snapshot.journeyInstance, snapshot.backtrackDecision)
			return func(s *matchingState) {
				s.journeyDecision = decision
			}, nil
		}),
		makeBatch("actionable_match", "generic", promptVersion("actionable_match"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			matches, guidelines := runActionableARQ(ctx, snapshot.router, snapshot.context, regular)
			return func(s *matchingState) {
				s.guidelineMatches, s.matchedGuidelines = matches, guidelines
			}, nil
		}),
		makeBatch("low_criticality_match", "generic", promptVersion("low_criticality_match"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			matches, guidelines := runLowCriticalityARQ(ctx, snapshot.router, snapshot.context, low)
			return func(s *matchingState) {
				s.lowCriticality = matches
				s.guidelineMatches = append(s.guidelineMatches, matches...)
				s.matchedGuidelines = append(s.matchedGuidelines, guidelines...)
			}, nil
		}),
		makeBatch("customer_dependency", "generic", promptVersion("customer_dependency"), func(_ context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			decisions, guidelines := runCustomerDependentARQ(snapshot.context, snapshot.matchedGuidelines)
			return func(s *matchingState) {
				s.customerDecisions, s.matchedGuidelines = decisions, guidelines
			}, nil
		}),
		makeBatch("previously_applied", "generic", promptVersion("previously_applied"), func(_ context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			decisions, guidelines := runPreviouslyAppliedARQ(snapshot.context, snapshot.matchedGuidelines, snapshot.guidelineMatches)
			return func(s *matchingState) {
				s.reapplyDecisions, s.matchedGuidelines = decisions, guidelines
			}, nil
		}),
		makeBatch("relationship_resolution", "generic", promptVersion("relationship_resolution"), func(_ context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			resolved := resolveRelationships(snapshot.bundle, snapshot.context, snapshot.matchedObservations, snapshot.guidelineMatches, snapshot.matchedGuidelines, snapshot.activeJourney, snapshot.activeJourneyState)
			return func(s *matchingState) {
				s.matchedGuidelines = resolved.guidelines
				s.suppressedGuidelines = resolved.suppressed
				s.disambiguationPrompt = resolved.disambiguation
				s.resolutionRecords = resolved.resolutions
				s.activeJourney = resolved.activeJourney
				if s.activeJourney == nil {
					s.activeJourneyState = nil
				}
			}, nil
		}),
		makeBatch("disambiguation", "generic", promptVersion("disambiguation"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			guidelines, suppressed, resolutions := applySiblingDisambiguation(snapshot.bundle, snapshot.context, snapshot.guidelineMatches, snapshot.matchedGuidelines, snapshot.suppressedGuidelines, snapshot.resolutionRecords)
			prompt := runDisambiguationARQ(ctx, snapshot.router, snapshot.context, guidelines, snapshot.disambiguationPrompt)
			return func(s *matchingState) {
				s.matchedGuidelines, s.suppressedGuidelines, s.resolutionRecords = guidelines, suppressed, resolutions
				s.disambiguationPrompt = prompt
			}, nil
		}),
	}
}

func (genericStrategy) CreateResponseAnalysisBatches(_ matchingSnapshot) []responseAnalysisBatch {
	return []responseAnalysisBatch{
		makeResponseBatch("response_analysis", "generic", promptVersion("response_analysis"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			templates := collectTemplates(snapshot.bundle, snapshot.activeJourney, snapshot.activeJourneyState, snapshot.context)
			mode := modeOrDefault(snapshot.bundle.CompositionMode, templates)
			analysis := analyzeResponsePlan(ctx, snapshot.router, snapshot.context, responseAnalysisGuidelines(snapshot.bundle, snapshot.context, snapshot.matchedGuidelines), templates, mode, snapshot.bundle.NoMatch)
			return func(s *matchingState) {
				s.candidateTemplates = templates
				s.responseAnalysis = analysis
			}, nil
		}),
	}
}

func (customStrategy) CreateMatchingBatches(_ matchingSnapshot, items []policy.Guideline) []guidelineMatchingBatch {
	return []guidelineMatchingBatch{
		makeBatch("custom_actionable_match", "custom", promptVersion("custom_actionable_match"), func(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
			matches, guidelines := runActionableARQ(ctx, snapshot.router, snapshot.context, items)
			return func(s *matchingState) {
				s.guidelineMatches = append(s.guidelineMatches, matches...)
				s.matchedGuidelines = append(s.matchedGuidelines, guidelines...)
				sortMatches(s.guidelineMatches)
				sortGuidelines(s.matchedGuidelines, s.guidelineMatches)
				s.matchedGuidelines = dedupeGuidelines(s.matchedGuidelines)
			}, nil
		}),
	}
}

func (customStrategy) CreateResponseAnalysisBatches(_ matchingSnapshot) []responseAnalysisBatch {
	return nil
}

type batchFunc struct {
	name          string
	strategy      string
	promptVersion string
	run           func(context.Context, matchingSnapshot) (stateMutation, error)
}

func makeBatch(name string, strategy string, promptVersion string, run func(context.Context, matchingSnapshot) (stateMutation, error)) batchFunc {
	return batchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b batchFunc) Name() string          { return b.name }
func (b batchFunc) Strategy() string      { return b.strategy }
func (b batchFunc) PromptVersion() string { return b.promptVersion }
func (b batchFunc) Process(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
	return b.run(ctx, snapshot)
}

type responseBatchFunc struct {
	name          string
	strategy      string
	promptVersion string
	run           func(context.Context, matchingSnapshot) (stateMutation, error)
}

func makeResponseBatch(name string, strategy string, promptVersion string, run func(context.Context, matchingSnapshot) (stateMutation, error)) responseBatchFunc {
	return responseBatchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b responseBatchFunc) Name() string          { return b.name }
func (b responseBatchFunc) Strategy() string      { return b.strategy }
func (b responseBatchFunc) PromptVersion() string { return b.promptVersion }
func (b responseBatchFunc) Process(ctx context.Context, snapshot matchingSnapshot) (stateMutation, error) {
	return b.run(ctx, snapshot)
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
