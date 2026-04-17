package policyruntime

import (
	"github.com/sahal/parmesan/internal/domain/policy"
	semantics "github.com/sahal/parmesan/internal/engine/semantics"
	retrieverdomain "github.com/sahal/parmesan/internal/knowledge/retriever"
)

type StageResult interface {
	Apply(*matchingState)
	BatchOutput() map[string]any
}

type ConditionArtifactsStageResult struct {
	Artifacts map[string]semantics.ConditionEvidence
}

type ObservationMatchStageResult struct {
	Matches      []Match
	Observations []policy.Observation
}

func (r ObservationMatchStageResult) Apply(state *matchingState) {
	state.observationStage = cloneObservationMatchStageResult(r)
	for _, item := range r.Observations {
		recordConditionArtifact(state, "observation:"+item.ID, cachedEvaluateConditionAcrossTexts(state.context, item.When, matchingSource(state.context), state.context.ConversationText))
	}
}

func (r ObservationMatchStageResult) BatchOutput() map[string]any {
	return map[string]any{"matches": r.Matches}
}

type GuidelineMatchStageResult struct {
	Matches    []Match
	Guidelines []policy.Guideline
	Low        bool
	Append     bool
}

func (r GuidelineMatchStageResult) Apply(state *matchingState) {
	if r.Append {
		state.matchFinalizeStage.GuidelineMatches = append(state.matchFinalizeStage.GuidelineMatches, r.Matches...)
		state.matchFinalizeStage.MatchedGuidelines = append(state.matchFinalizeStage.MatchedGuidelines, r.Guidelines...)
		sortMatches(state.matchFinalizeStage.GuidelineMatches)
		sortGuidelines(state.matchFinalizeStage.MatchedGuidelines, state.matchFinalizeStage.GuidelineMatches)
		state.matchFinalizeStage.MatchedGuidelines = dedupeGuidelines(state.matchFinalizeStage.MatchedGuidelines)
	} else if r.Low {
		state.matchFinalizeStage.GuidelineMatches = append(state.matchFinalizeStage.GuidelineMatches, r.Matches...)
		state.matchFinalizeStage.MatchedGuidelines = append(state.matchFinalizeStage.MatchedGuidelines, r.Guidelines...)
	} else {
		state.matchFinalizeStage.GuidelineMatches = append([]Match(nil), r.Matches...)
		state.matchFinalizeStage.MatchedGuidelines = append([]policy.Guideline(nil), r.Guidelines...)
	}
	for _, item := range r.Guidelines {
		recordConditionArtifact(state, "guideline:"+item.ID, cachedEvaluateConditionAcrossTexts(state.context, item.When, matchingSource(state.context), state.context.ConversationText))
	}
	syncFinalizeStageToState(state)
}

func (r GuidelineMatchStageResult) BatchOutput() map[string]any {
	return map[string]any{"matches": r.Matches}
}

type PreviouslyAppliedStageResult struct {
	Decisions  []ReapplyDecision
	Guidelines []policy.Guideline
}

func (r PreviouslyAppliedStageResult) Apply(state *matchingState) {
	state.previouslyAppliedStage = clonePreviouslyAppliedStageResult(r)
	state.matchFinalizeStage.MatchedGuidelines = append([]policy.Guideline(nil), r.Guidelines...)
	syncFinalizeStageToState(state)
}

func (r PreviouslyAppliedStageResult) BatchOutput() map[string]any {
	return map[string]any{"reapply": r.Decisions}
}

type FinalizeStageResult struct {
	GuidelineMatches     []Match
	MatchedGuidelines    []policy.Guideline
	SuppressedGuidelines []SuppressedGuideline
	ResolutionRecords    []ResolutionRecord
}

func (r FinalizeStageResult) Apply(state *matchingState) {
	state.matchFinalizeStage = cloneFinalizeStageResult(r)
	syncFinalizeStageToState(state)
}

func (r FinalizeStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"matches":     r.GuidelineMatches,
		"guidelines":  r.MatchedGuidelines,
		"suppressed":  r.SuppressedGuidelines,
		"resolutions": r.ResolutionRecords,
	}
}

type JourneyBacktrackStageResult struct {
	Evaluation JourneyBacktrackEvaluation
	Decision   JourneyDecision
}

func (r JourneyBacktrackStageResult) Apply(state *matchingState) {
	state.journeyBacktrackStage = cloneJourneyBacktrackStageResult(r)
	state.activeJourney = r.Evaluation.ActiveJourney
	state.activeJourneyState = r.Evaluation.ActiveJourneyState
	state.journeyInstance = r.Evaluation.JourneyInstance
}

func (r JourneyBacktrackStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":   r.Evaluation,
		"intent":       r.Evaluation.BacktrackIntent,
		"selected":     r.Evaluation.SelectedBacktrack,
		"evaluations":  r.Evaluation.BacktrackEvaluations,
		"action":       r.Decision.Action,
		"backtrack_to": r.Decision.BacktrackTo,
		"rationale":    r.Decision.Rationale,
		"missing":      r.Decision.Missing,
	}
}

type JourneyProgressStageResult struct {
	Evaluation JourneyProgressEvaluation
	Decision   JourneyDecision
}

func (r JourneyProgressStageResult) Apply(state *matchingState) {
	state.journeyProgressStage = cloneJourneyProgressStageResult(r)
}

func (r JourneyProgressStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":    r.Evaluation,
		"satisfactions": r.Evaluation.JourneySatisfactions,
		"next_nodes":    r.Evaluation.NextNodeEvaluations,
		"selected":      r.Evaluation.SelectedNextNode,
		"action":        r.Decision.Action,
		"current_state": r.Decision.CurrentState,
		"next_state":    r.Decision.NextState,
		"backtrack_to":  r.Decision.BacktrackTo,
		"rationale":     r.Decision.Rationale,
		"missing":       r.Decision.Missing,
	}
}

type CustomerDependencyStageResult struct {
	Decisions  []CustomerDependencyDecision
	Evidence   map[string]semantics.CustomerDependencyEvidence
	Guidelines []policy.Guideline
}

func (r CustomerDependencyStageResult) Apply(state *matchingState) {
	state.customerDependencyStage = cloneCustomerDependencyStageResult(r)
	state.matchFinalizeStage.MatchedGuidelines = append([]policy.Guideline(nil), r.Guidelines...)
	syncFinalizeStageToState(state)
}

func (r CustomerDependencyStageResult) BatchOutput() map[string]any {
	return map[string]any{"decisions": r.Decisions, "evidence": r.Evidence}
}

type RelationshipResolutionStageResult struct {
	Guidelines           []policy.Guideline
	SuppressedGuidelines []SuppressedGuideline
	ResolutionRecords    []ResolutionRecord
	DisambiguationPrompt string
	ActiveJourney        *policy.Journey
}

func (r RelationshipResolutionStageResult) Apply(state *matchingState) {
	state.relationshipResolutionStage = cloneRelationshipResolutionStageResult(r)
	state.matchFinalizeStage.MatchedGuidelines = append([]policy.Guideline(nil), r.Guidelines...)
	state.activeJourney = r.ActiveJourney
	if state.activeJourney == nil {
		state.activeJourneyState = nil
	}
	syncFinalizeStageToState(state)
}

func (r RelationshipResolutionStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"suppressed":  r.SuppressedGuidelines,
		"resolutions": r.ResolutionRecords,
	}
}

type DisambiguationStageResult struct {
	Guidelines           []policy.Guideline
	SuppressedGuidelines []SuppressedGuideline
	ResolutionRecords    []ResolutionRecord
	Prompt               string
}

func (r DisambiguationStageResult) Apply(state *matchingState) {
	state.disambiguationStage = cloneDisambiguationStageResult(r)
	state.matchFinalizeStage.MatchedGuidelines = append([]policy.Guideline(nil), r.Guidelines...)
	syncFinalizeStageToState(state)
}

func (r DisambiguationStageResult) BatchOutput() map[string]any {
	return map[string]any{"prompt": r.Prompt}
}

type RetrieverStageResult struct {
	Results             []retrieverdomain.Result `json:"results,omitempty"`
	KnowledgeSnapshotID string                   `json:"knowledge_snapshot_id,omitempty"`
	TransientGuidelines []policy.Guideline       `json:"transient_guidelines,omitempty"`
	Outcome             RetrievalOutcome         `json:"outcome,omitempty"`
}

func (r RetrieverStageResult) Apply(state *matchingState) {
	state.retrieverStage = cloneRetrieverStageResult(r)
	state.context.RetrievedKnowledge = append([]retrieverdomain.Result(nil), r.Results...)
	if len(r.TransientGuidelines) > 0 {
		state.matchFinalizeStage.MatchedGuidelines = append(state.matchFinalizeStage.MatchedGuidelines, r.TransientGuidelines...)
		state.matchFinalizeStage.MatchedGuidelines = dedupeGuidelines(state.matchFinalizeStage.MatchedGuidelines)
		syncFinalizeStageToState(state)
	}
}

func (r RetrieverStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"results":               r.Results,
		"knowledge_snapshot_id": r.KnowledgeSnapshotID,
		"transient_guidelines":  r.TransientGuidelines,
		"outcome":               r.Outcome,
	}
}

type RetrievalOutcome struct {
	Attempted         bool   `json:"attempted,omitempty"`
	State             string `json:"state,omitempty"`
	HasUsableEvidence bool   `json:"has_usable_evidence,omitempty"`
	GroundingRequired bool   `json:"grounding_required,omitempty"`
}

type ResponseAnalysisStageResult struct {
	CandidateTemplates []policy.Template
	Analysis           ResponseAnalysis
	Evaluation         ResponseAnalysisEvaluation
}

func (r ResponseAnalysisStageResult) Apply(state *matchingState) {
	state.responseAnalysisStage = cloneResponseAnalysisStageResult(r)
}

func (r ResponseAnalysisStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":                     r.Evaluation,
		"coverage":                       r.Evaluation.Coverage,
		"analyzed_guidelines":            r.Analysis.AnalyzedGuidelines,
		"needs_revision":                 r.Analysis.NeedsRevision,
		"needs_strict_mode":              r.Analysis.NeedsStrictMode,
		"recommended_template":           r.Analysis.RecommendedTemplate,
		"rationale":                      r.Analysis.Rationale,
		"response_capability_id":         r.Evaluation.ResponseCapabilityID,
		"response_capability_source":     r.Evaluation.ResponseCapabilitySource,
		"response_capability_candidates": r.Evaluation.ResponseCapabilityCandidates,
		"style_profile_id":               r.Evaluation.StyleProfileID,
		"style_profile_source":           r.Evaluation.StyleProfileSource,
		"style_profile_candidates":       r.Evaluation.StyleProfileCandidates,
	}
}

type ToolExposureStageResult struct {
	ExposedTools  []string
	ToolApprovals map[string]string
}

func (r ToolExposureStageResult) Apply(state *matchingState) {
	state.toolExposureStage = cloneToolExposureStageResult(r)
}

func (r ToolExposureStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"exposed_tools":  append([]string(nil), r.ExposedTools...),
		"tool_approvals": cloneStringMap(r.ToolApprovals),
	}
}

type ToolPlanStageResult struct {
	Plan       ToolCallPlan
	Evaluation ToolPlanEvaluation
}

func (r ToolPlanStageResult) Apply(state *matchingState) {
	state.toolPlanStage = cloneToolPlanStageResult(r)
}

func (r ToolPlanStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":         r.Evaluation,
		"candidates":         r.Plan.Candidates,
		"batches":            r.Evaluation.Batches,
		"grounding":          r.Evaluation.Grounding,
		"selection_evidence": r.Evaluation.SelectionEvidence,
		"selected_tool":      r.Plan.SelectedTool,
		"overlapping_groups": r.Plan.OverlappingGroups,
		"rationale":          r.Plan.Rationale,
	}
}

type ToolDecisionStageResult struct {
	Decision   ToolDecision
	Evaluation ToolDecisionEvaluation
}

func (r ToolDecisionStageResult) Apply(state *matchingState) {
	state.toolDecisionStage = cloneToolDecisionStageResult(r)
}

func (r ToolDecisionStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":        r.Evaluation,
		"selected_tool":     r.Decision.SelectedTool,
		"arguments":         r.Decision.Arguments,
		"approval_required": r.Decision.ApprovalRequired,
		"can_run":           r.Decision.CanRun,
		"missing_arguments": r.Decision.MissingArguments,
		"invalid_arguments": r.Decision.InvalidArguments,
		"missing_issues":    r.Decision.MissingIssues,
		"invalid_issues":    r.Decision.InvalidIssues,
		"grounded":          r.Decision.Grounded,
		"rationale":         r.Decision.Rationale,
	}
}

type AgentExposureStageResult struct {
	ExposedAgents   []string
	ExposedBindings []ExposedAgentBinding
}

func (r AgentExposureStageResult) Apply(state *matchingState) {
	state.agentExposureStage = cloneAgentExposureStageResult(r)
}

func (r AgentExposureStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"exposed_agents":   append([]string(nil), r.ExposedAgents...),
		"exposed_bindings": append([]ExposedAgentBinding(nil), r.ExposedBindings...),
	}
}

type AgentDecisionStageResult struct {
	Decision   AgentDecision
	Evaluation AgentDecisionEvaluation
}

func (r AgentDecisionStageResult) Apply(state *matchingState) {
	state.agentDecisionStage = cloneAgentDecisionStageResult(r)
}

func (r AgentDecisionStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation":           r.Evaluation,
		"selected_agent":       r.Decision.SelectedAgent,
		"selected_workflow_id": r.Decision.SelectedWorkflowID,
		"can_run":              r.Decision.CanRun,
		"grounded":             r.Decision.Grounded,
		"rationale":            r.Decision.Rationale,
	}
}

type CapabilityDecisionStageResult struct {
	Decision   CapabilityDecision
	Evaluation CapabilityDecisionEvaluation
}

func (r CapabilityDecisionStageResult) Apply(state *matchingState) {
	state.capabilityDecisionStage = cloneCapabilityDecisionStageResult(r)
}

func (r CapabilityDecisionStageResult) BatchOutput() map[string]any {
	return map[string]any{
		"evaluation": r.Evaluation,
		"kind":       r.Decision.Kind,
		"target_id":  r.Decision.TargetID,
		"rationale":  r.Decision.Rationale,
	}
}
