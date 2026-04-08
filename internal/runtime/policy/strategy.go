package policyruntime

import (
	"context"
	"strings"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	retrieverdomain "github.com/sahal/parmesan/internal/knowledge/retriever"
	"github.com/sahal/parmesan/internal/model"
	semantics "github.com/sahal/parmesan/internal/runtime/semantics"
)

type guidelineMatchingBatch interface {
	Name() string
	Strategy() string
	PromptVersion() string
	Process(context.Context, matchingSnapshot) (StageResult, error)
}

type responseAnalysisBatch interface {
	Name() string
	Strategy() string
	PromptVersion() string
	Process(context.Context, matchingSnapshot) (StageResult, error)
}

type guidelineMatchingStrategy interface {
	Name() string
	CreateMatchingBatches(matchingSnapshot, []policy.Guideline) []guidelineMatchingBatch
	CreateResponseAnalysisBatches(matchingSnapshot) []responseAnalysisBatch
	TransformMatches(matchingSnapshot) StageResult
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

	projectedNodes              []ProjectedJourneyNode
	attention                   PolicyAttention
	observationStage            ObservationMatchStageResult
	activeJourney               *policy.Journey
	activeJourneyState          *policy.JourneyNode
	journeyInstance             *journey.Instance
	matchFinalizeStage          FinalizeStageResult
	previouslyAppliedStage      PreviouslyAppliedStageResult
	conditionArtifactsStage     ConditionArtifactsStageResult
	journeyBacktrackStage       JourneyBacktrackStageResult
	journeyProgressStage        JourneyProgressStageResult
	customerDependencyStage     CustomerDependencyStageResult
	relationshipResolutionStage RelationshipResolutionStageResult
	disambiguationStage         DisambiguationStageResult
	scopeBoundaryStage          ScopeBoundaryStageResult
	retrieverStage              RetrieverStageResult
	responseAnalysisStage       ResponseAnalysisStageResult
	toolExposureStage           ToolExposureStageResult
	toolPlanStage               ToolPlanStageResult
	toolDecisionStage           ToolDecisionStageResult
	batchResults                []BatchResult
	promptSetVersions           map[string]string
}

type matchingSnapshot struct {
	router                      *model.Router
	bundle                      policy.Bundle
	context                     MatchingContext
	catalog                     []tool.CatalogEntry
	journeyInstances            []journey.Instance
	projectedNodes              []ProjectedJourneyNode
	attention                   PolicyAttention
	observationStage            ObservationMatchStageResult
	activeJourney               *policy.Journey
	activeJourneyState          *policy.JourneyNode
	journeyInstance             *journey.Instance
	matchFinalizeStage          FinalizeStageResult
	previouslyAppliedStage      PreviouslyAppliedStageResult
	conditionArtifactsStage     ConditionArtifactsStageResult
	journeyBacktrackStage       JourneyBacktrackStageResult
	journeyProgressStage        JourneyProgressStageResult
	customerDependencyStage     CustomerDependencyStageResult
	relationshipResolutionStage RelationshipResolutionStageResult
	disambiguationStage         DisambiguationStageResult
	scopeBoundaryStage          ScopeBoundaryStageResult
	retrieverStage              RetrieverStageResult
	responseAnalysisStage       ResponseAnalysisStageResult
	toolExposureStage           ToolExposureStageResult
	toolPlanStage               ToolPlanStageResult
	toolDecisionStage           ToolDecisionStageResult
}

func snapshotFromState(state *matchingState) matchingSnapshot {
	if state == nil {
		return matchingSnapshot{}
	}
	return matchingSnapshot{
		router:  state.router,
		bundle:  state.bundle,
		context: state.context,
		// Snapshots are read-only views for batch creation/execution, so keep
		// them shallow to avoid re-cloning large stage artifacts on every step.
		catalog:                     state.catalog,
		journeyInstances:            state.journeyInstances,
		projectedNodes:              state.projectedNodes,
		attention:                   state.attention,
		observationStage:            state.observationStage,
		activeJourney:               state.activeJourney,
		activeJourneyState:          state.activeJourneyState,
		journeyInstance:             state.journeyInstance,
		matchFinalizeStage:          state.matchFinalizeStage,
		previouslyAppliedStage:      state.previouslyAppliedStage,
		conditionArtifactsStage:     state.conditionArtifactsStage,
		journeyBacktrackStage:       state.journeyBacktrackStage,
		journeyProgressStage:        state.journeyProgressStage,
		customerDependencyStage:     state.customerDependencyStage,
		relationshipResolutionStage: state.relationshipResolutionStage,
		disambiguationStage:         state.disambiguationStage,
		scopeBoundaryStage:          state.scopeBoundaryStage,
		retrieverStage:              state.retrieverStage,
		responseAnalysisStage:       state.responseAnalysisStage,
		toolExposureStage:           state.toolExposureStage,
		toolPlanStage:               state.toolPlanStage,
		toolDecisionStage:           state.toolDecisionStage,
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

func cloneConditionArtifacts(src map[string]semantics.ConditionEvidence) map[string]semantics.ConditionEvidence {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.ConditionEvidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneJourneySatisfactions(src map[string]semantics.JourneyStateSatisfaction) map[string]semantics.JourneyStateSatisfaction {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.JourneyStateSatisfaction, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneBacktrackEvaluations(src map[string]BacktrackCandidateEvaluation) map[string]BacktrackCandidateEvaluation {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]BacktrackCandidateEvaluation, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneNextNodeEvaluations(src map[string]JourneyNextNodeEvaluation) map[string]JourneyNextNodeEvaluation {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]JourneyNextNodeEvaluation, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneCustomerDependencyEvidence(src map[string]semantics.CustomerDependencyEvidence) map[string]semantics.CustomerDependencyEvidence {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.CustomerDependencyEvidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneActionCoverage(src map[string]semantics.ActionCoverageEvidence) map[string]semantics.ActionCoverageEvidence {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.ActionCoverageEvidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneToolGrounding(src map[string]semantics.ToolGroundingEvidence) map[string]semantics.ToolGroundingEvidence {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.ToolGroundingEvidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneToolSelection(src map[string]semantics.ToolSelectionEvidence) map[string]semantics.ToolSelectionEvidence {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]semantics.ToolSelectionEvidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneJourneyBacktrackStageResult(src JourneyBacktrackStageResult) JourneyBacktrackStageResult {
	src.Evaluation = cloneJourneyBacktrackEvaluation(src.Evaluation)
	return src
}

func cloneJourneyProgressStageResult(src JourneyProgressStageResult) JourneyProgressStageResult {
	src.Evaluation = cloneJourneyProgressEvaluation(src.Evaluation)
	return src
}

func cloneJourneyBacktrackEvaluation(src JourneyBacktrackEvaluation) JourneyBacktrackEvaluation {
	src.BacktrackEvaluations = cloneBacktrackEvaluations(src.BacktrackEvaluations)
	return src
}

func cloneJourneyProgressEvaluation(src JourneyProgressEvaluation) JourneyProgressEvaluation {
	src.JourneySatisfactions = cloneJourneySatisfactions(src.JourneySatisfactions)
	src.NextNodeEvaluations = cloneNextNodeEvaluations(src.NextNodeEvaluations)
	return src
}

func cloneCustomerDependencyStageResult(src CustomerDependencyStageResult) CustomerDependencyStageResult {
	src.Decisions = append([]CustomerDependencyDecision(nil), src.Decisions...)
	src.Guidelines = append([]policy.Guideline(nil), src.Guidelines...)
	src.Evidence = cloneCustomerDependencyEvidence(src.Evidence)
	return src
}

func cloneRelationshipResolutionStageResult(src RelationshipResolutionStageResult) RelationshipResolutionStageResult {
	src.Guidelines = append([]policy.Guideline(nil), src.Guidelines...)
	src.SuppressedGuidelines = append([]SuppressedGuideline(nil), src.SuppressedGuidelines...)
	src.ResolutionRecords = append([]ResolutionRecord(nil), src.ResolutionRecords...)
	return src
}

func cloneObservationMatchStageResult(src ObservationMatchStageResult) ObservationMatchStageResult {
	src.Matches = append([]Match(nil), src.Matches...)
	src.Observations = append([]policy.Observation(nil), src.Observations...)
	return src
}

func cloneFinalizeStageResult(src FinalizeStageResult) FinalizeStageResult {
	src.GuidelineMatches = append([]Match(nil), src.GuidelineMatches...)
	src.MatchedGuidelines = append([]policy.Guideline(nil), src.MatchedGuidelines...)
	src.SuppressedGuidelines = append([]SuppressedGuideline(nil), src.SuppressedGuidelines...)
	src.ResolutionRecords = append([]ResolutionRecord(nil), src.ResolutionRecords...)
	return src
}

func cloneDisambiguationStageResult(src DisambiguationStageResult) DisambiguationStageResult {
	src.Guidelines = append([]policy.Guideline(nil), src.Guidelines...)
	src.SuppressedGuidelines = append([]SuppressedGuideline(nil), src.SuppressedGuidelines...)
	src.ResolutionRecords = append([]ResolutionRecord(nil), src.ResolutionRecords...)
	return src
}

func effectiveSuppressedGuidelines(relationshipStage RelationshipResolutionStageResult, disambiguationStage DisambiguationStageResult) []SuppressedGuideline {
	if len(disambiguationStage.SuppressedGuidelines) > 0 {
		return append([]SuppressedGuideline(nil), disambiguationStage.SuppressedGuidelines...)
	}
	return append([]SuppressedGuideline(nil), relationshipStage.SuppressedGuidelines...)
}

func effectiveResolutionRecords(relationshipStage RelationshipResolutionStageResult, disambiguationStage DisambiguationStageResult) []ResolutionRecord {
	items := append([]ResolutionRecord(nil), relationshipStage.ResolutionRecords...)
	items = append(items, disambiguationStage.ResolutionRecords...)
	return dedupeResolutionRecords(items)
}

func effectiveDisambiguationPrompt(relationshipStage RelationshipResolutionStageResult, disambiguationStage DisambiguationStageResult) string {
	if strings.TrimSpace(disambiguationStage.Prompt) != "" {
		return disambiguationStage.Prompt
	}
	return relationshipStage.DisambiguationPrompt
}

func clonePreviouslyAppliedStageResult(src PreviouslyAppliedStageResult) PreviouslyAppliedStageResult {
	src.Decisions = append([]ReapplyDecision(nil), src.Decisions...)
	src.Guidelines = append([]policy.Guideline(nil), src.Guidelines...)
	return src
}

func cloneResponseAnalysisEvaluation(src ResponseAnalysisEvaluation) ResponseAnalysisEvaluation {
	src.Coverage = cloneActionCoverage(src.Coverage)
	src.AnalyzedGuidelines = append([]AnalyzedGuideline(nil), src.AnalyzedGuidelines...)
	return src
}

func cloneResponseAnalysisStageResult(src ResponseAnalysisStageResult) ResponseAnalysisStageResult {
	src.CandidateTemplates = append([]policy.Template(nil), src.CandidateTemplates...)
	src.Evaluation = cloneResponseAnalysisEvaluation(src.Evaluation)
	src.Analysis.AnalyzedGuidelines = append([]AnalyzedGuideline(nil), src.Analysis.AnalyzedGuidelines...)
	return src
}

func cloneToolPlanEvaluation(src ToolPlanEvaluation) ToolPlanEvaluation {
	src.Candidates = append([]ToolCandidate(nil), src.Candidates...)
	src.Batches = append([]ToolCallBatchResult(nil), src.Batches...)
	src.Grounding = cloneToolGrounding(src.Grounding)
	src.SelectionEvidence = cloneToolSelection(src.SelectionEvidence)
	if len(src.OverlappingGroups) > 0 {
		out := make([][]string, 0, len(src.OverlappingGroups))
		for _, group := range src.OverlappingGroups {
			out = append(out, append([]string(nil), group...))
		}
		src.OverlappingGroups = out
	}
	src.SelectedTools = append([]string(nil), src.SelectedTools...)
	return src
}

func cloneToolExposureStageResult(src ToolExposureStageResult) ToolExposureStageResult {
	src.ExposedTools = append([]string(nil), src.ExposedTools...)
	src.ToolApprovals = cloneStringMap(src.ToolApprovals)
	return src
}

func cloneRetrieverStageResult(src RetrieverStageResult) RetrieverStageResult {
	src.Results = append([]retrieverdomain.Result(nil), src.Results...)
	src.TransientGuidelines = append([]policy.Guideline(nil), src.TransientGuidelines...)
	return src
}

func cloneToolPlanStageResult(src ToolPlanStageResult) ToolPlanStageResult {
	src.Plan.Candidates = append([]ToolCandidate(nil), src.Plan.Candidates...)
	src.Plan.Batches = append([]ToolCallBatchResult(nil), src.Plan.Batches...)
	src.Plan.SelectedTools = append([]string(nil), src.Plan.SelectedTools...)
	src.Plan.OverlappingGroups = cloneOverlappingGroups(src.Plan.OverlappingGroups)
	src.Plan.Calls = append([]ToolPlannedCall(nil), src.Plan.Calls...)
	src.Evaluation = cloneToolPlanEvaluation(src.Evaluation)
	return src
}

func cloneToolDecisionStageResult(src ToolDecisionStageResult) ToolDecisionStageResult {
	src.Decision.Arguments = cloneAnyMap(src.Decision.Arguments)
	src.Decision.MissingArguments = append([]string(nil), src.Decision.MissingArguments...)
	src.Decision.InvalidArguments = append([]string(nil), src.Decision.InvalidArguments...)
	src.Decision.MissingIssues = append([]ToolArgumentIssue(nil), src.Decision.MissingIssues...)
	src.Decision.InvalidIssues = append([]ToolArgumentIssue(nil), src.Decision.InvalidIssues...)
	src.Evaluation.SelectedTools = append([]string(nil), src.Evaluation.SelectedTools...)
	src.Evaluation.MissingIssues = append([]ToolArgumentIssue(nil), src.Evaluation.MissingIssues...)
	src.Evaluation.InvalidIssues = append([]ToolArgumentIssue(nil), src.Evaluation.InvalidIssues...)
	return src
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
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

func (genericStrategy) TransformMatches(snapshot matchingSnapshot) StageResult {
	if snapshot.bundle.ID == "" && snapshot.context.SessionID == "" && len(snapshot.matchFinalizeStage.GuidelineMatches) == 0 && len(snapshot.matchFinalizeStage.MatchedGuidelines) == 0 {
		return nil
	}
	guidelineMatches := append([]Match(nil), snapshot.matchFinalizeStage.GuidelineMatches...)
	matchedGuidelines := append([]policy.Guideline(nil), snapshot.matchFinalizeStage.MatchedGuidelines...)
	suppressed := effectiveSuppressedGuidelines(snapshot.relationshipResolutionStage, snapshot.disambiguationStage)
	resolutions := effectiveResolutionRecords(snapshot.relationshipResolutionStage, snapshot.disambiguationStage)
	sortMatches(guidelineMatches)
	guidelineMatches = dedupeMatches(guidelineMatches)
	sortGuidelines(matchedGuidelines, guidelineMatches)
	matchedGuidelines = dedupeGuidelines(matchedGuidelines)
	suppressed = dedupeSuppressedGuidelines(suppressed)
	resolutions = dedupeResolutionRecords(resolutions)
	return FinalizeStageResult{
		GuidelineMatches:     guidelineMatches,
		MatchedGuidelines:    matchedGuidelines,
		SuppressedGuidelines: suppressed,
		ResolutionRecords:    resolutions,
	}
}

func (customStrategy) TransformMatches(snapshot matchingSnapshot) StageResult {
	return genericStrategy{}.TransformMatches(snapshot)
}

func (genericStrategy) CreateMatchingBatches(_ matchingSnapshot, items []policy.Guideline) []guidelineMatchingBatch {
	regular, low := splitLowCriticalityGuidelines(items)
	return []guidelineMatchingBatch{
		makeBatch("observation_match", "generic", promptVersion("observation_match"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			matches, observations := runObservationARQ(ctx, snapshot.router, snapshot.context, snapshot.bundle.Observations)
			return ObservationMatchStageResult{Matches: matches, Observations: observations}, nil
		}),
		makeBatch("journey_backtrack", "generic", promptVersion("journey_backtrack"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			return buildJourneyBacktrackStageResult(ctx, snapshot.router, snapshot.bundle, snapshot.context, snapshot.journeyInstances), nil
		}),
		makeBatch("journey_progress", "generic", promptVersion("journey_progress"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			return buildJourneyProgressStageResult(ctx, snapshot.router, snapshot.context, snapshot.activeJourney, snapshot.activeJourneyState, snapshot.journeyInstance, snapshot.journeyBacktrackStage.Decision), nil
		}),
		makeBatch("actionable_match", "generic", promptVersion("actionable_match"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			matches, guidelines := runActionableARQ(ctx, snapshot.router, snapshot.context, regular)
			return GuidelineMatchStageResult{Matches: matches, Guidelines: guidelines}, nil
		}),
		makeBatch("low_criticality_match", "generic", promptVersion("low_criticality_match"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			matches, guidelines := runLowCriticalityARQ(ctx, snapshot.router, snapshot.context, low)
			return GuidelineMatchStageResult{Matches: matches, Guidelines: guidelines, Low: true}, nil
		}),
		makeBatch("customer_dependency", "generic", promptVersion("customer_dependency"), func(_ context.Context, snapshot matchingSnapshot) (StageResult, error) {
			return buildCustomerDependencyStageResult(snapshot.context, snapshot.matchFinalizeStage.MatchedGuidelines), nil
		}),
		makeBatch("previously_applied", "generic", promptVersion("previously_applied"), func(_ context.Context, snapshot matchingSnapshot) (StageResult, error) {
			return buildPreviouslyAppliedStageResult(snapshot.context, snapshot.matchFinalizeStage.MatchedGuidelines, snapshot.matchFinalizeStage.GuidelineMatches), nil
		}),
		makeBatch("relationship_resolution", "generic", promptVersion("relationship_resolution"), func(_ context.Context, snapshot matchingSnapshot) (StageResult, error) {
			return buildRelationshipResolutionStageResult(snapshot.bundle, snapshot.context, snapshot.observationStage.Observations, snapshot.matchFinalizeStage.GuidelineMatches, snapshot.matchFinalizeStage.MatchedGuidelines, snapshot.activeJourney, snapshot.activeJourneyState), nil
		}),
		makeBatch("disambiguation", "generic", promptVersion("disambiguation"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			suppressed := effectiveSuppressedGuidelines(snapshot.relationshipResolutionStage, snapshot.disambiguationStage)
			resolutions := effectiveResolutionRecords(snapshot.relationshipResolutionStage, snapshot.disambiguationStage)
			prompt := effectiveDisambiguationPrompt(snapshot.relationshipResolutionStage, snapshot.disambiguationStage)
			return buildDisambiguationStageResult(ctx, snapshot.router, snapshot.bundle, snapshot.context, snapshot.matchFinalizeStage.GuidelineMatches, snapshot.matchFinalizeStage.MatchedGuidelines, suppressed, resolutions, prompt), nil
		}),
	}
}

func (genericStrategy) CreateResponseAnalysisBatches(_ matchingSnapshot) []responseAnalysisBatch {
	return []responseAnalysisBatch{
		makeResponseBatch("response_analysis", "generic", promptVersion("response_analysis"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			templates := collectTemplates(snapshot.bundle, snapshot.activeJourney, snapshot.activeJourneyState, snapshot.context)
			return buildResponseAnalysisStageResult(ctx, snapshot.router, snapshot.context, snapshot.bundle, snapshot.matchFinalizeStage.MatchedGuidelines, templates, snapshot.responseAnalysisStage.Evaluation.Coverage), nil
		}),
	}
}

func (customStrategy) CreateMatchingBatches(_ matchingSnapshot, items []policy.Guideline) []guidelineMatchingBatch {
	return []guidelineMatchingBatch{
		makeBatch("custom_actionable_match", "custom", promptVersion("custom_actionable_match"), func(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
			matches, guidelines := runActionableARQ(ctx, snapshot.router, snapshot.context, items)
			return GuidelineMatchStageResult{
				Matches:    matches,
				Guidelines: guidelines,
				Append:     true,
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
	run           func(context.Context, matchingSnapshot) (StageResult, error)
}

func makeBatch(name string, strategy string, promptVersion string, run func(context.Context, matchingSnapshot) (StageResult, error)) batchFunc {
	return batchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b batchFunc) Name() string          { return b.name }
func (b batchFunc) Strategy() string      { return b.strategy }
func (b batchFunc) PromptVersion() string { return b.promptVersion }
func (b batchFunc) Process(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
	return b.run(ctx, snapshot)
}

type responseBatchFunc struct {
	name          string
	strategy      string
	promptVersion string
	run           func(context.Context, matchingSnapshot) (StageResult, error)
}

func makeResponseBatch(name string, strategy string, promptVersion string, run func(context.Context, matchingSnapshot) (StageResult, error)) responseBatchFunc {
	return responseBatchFunc{name: name, strategy: strategy, promptVersion: promptVersion, run: run}
}

func (b responseBatchFunc) Name() string          { return b.name }
func (b responseBatchFunc) Strategy() string      { return b.strategy }
func (b responseBatchFunc) PromptVersion() string { return b.promptVersion }
func (b responseBatchFunc) Process(ctx context.Context, snapshot matchingSnapshot) (StageResult, error) {
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
