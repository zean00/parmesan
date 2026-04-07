package policyruntime

import (
	"context"
	"time"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	retrieverdomain "github.com/sahal/parmesan/internal/knowledge/retriever"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/runtime/semantics"
)

type MatchingContext struct {
	SessionID           string
	LatestCustomerText  string
	CustomerHistory     []string
	AssistantHistory    []string
	StagedToolCalls     []StagedToolCall
	StagedToolText      []string
	ConversationText    string
	AppliedGuidelines   []string
	AppliedInstructions []string
	DerivedSignals      []string
	RetrievedKnowledge  []retrieverdomain.Result
	OccurredAt          time.Time
	cache               *matchingEvalCache
}

type StagedToolCall struct {
	ToolID     string         `json:"tool_id"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	DocumentID string         `json:"document_id,omitempty"`
	ModulePath string         `json:"module_path,omitempty"`
}

type Match struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale,omitempty"`
}

type argumentSlotKind = semantics.SlotKind

const (
	argumentSlotUnknown     = semantics.SlotUnknown
	argumentSlotDestination = semantics.SlotDestination
	argumentSlotProductLike = semantics.SlotProductLike
)

type ConditionEvidence = semantics.ConditionEvidence

type ReapplyDecision struct {
	ID            string  `json:"id"`
	ShouldReapply bool    `json:"should_reapply"`
	Score         float64 `json:"score"`
	Rationale     string  `json:"rationale,omitempty"`
}

type CustomerDependencyDecision struct {
	ID                  string   `json:"id"`
	CustomerDependent   bool     `json:"customer_dependent"`
	MissingCustomerData []string `json:"missing_customer_data,omitempty"`
	Rationale           string   `json:"rationale,omitempty"`
}

type CustomerDependencyEvidence = semantics.CustomerDependencyEvidence

type ActionCoverageEvidence = semantics.ActionCoverageEvidence

type PolicyAttention struct {
	CriticalInstructionIDs []string `json:"critical_instruction_ids,omitempty"`
	ContextSignals         []string `json:"context_signals,omitempty"`
	MissingInformation     []string `json:"missing_information,omitempty"`
}

type SuppressedGuideline struct {
	ID         string   `json:"id"`
	Reason     string   `json:"reason"`
	RelatedIDs []string `json:"related_ids,omitempty"`
}

type ResolutionKind string

const (
	ResolutionNone               ResolutionKind = "none"
	ResolutionUnmetDependency    ResolutionKind = "unmet_dependency"
	ResolutionUnmetDependencyAny ResolutionKind = "unmet_dependency_any"
	ResolutionDeprioritized      ResolutionKind = "deprioritized"
	ResolutionEntailed           ResolutionKind = "entailed"
)

type ResolutionDetails struct {
	Description string   `json:"description"`
	TargetIDs   []string `json:"target_ids,omitempty"`
}

type ResolutionRecord struct {
	EntityID string            `json:"entity_id"`
	Kind     ResolutionKind    `json:"kind"`
	Details  ResolutionDetails `json:"details"`
}

type ProjectedJourneyNode struct {
	ID              string         `json:"id"`
	JourneyID       string         `json:"journey_id"`
	StateID         string         `json:"state_id"`
	SourceEdgeID    string         `json:"source_edge_id,omitempty"`
	Index           int            `json:"index,omitempty"`
	Instruction     string         `json:"instruction,omitempty"`
	FollowUps       []string       `json:"follow_ups,omitempty"`
	LegalFollowUps  []string       `json:"legal_follow_ups,omitempty"`
	ToolRefs        []string       `json:"tool_refs,omitempty"`
	Labels          []string       `json:"labels,omitempty"`
	CompositionMode string         `json:"composition_mode,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Priority        int            `json:"priority,omitempty"`
}

type JourneyDecision struct {
	Action       string   `json:"action,omitempty"`
	CurrentState string   `json:"current_state,omitempty"`
	NextState    string   `json:"next_state,omitempty"`
	BacktrackTo  string   `json:"backtrack_to,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
	Missing      []string `json:"missing,omitempty"`
}

type JourneyStateSatisfaction = semantics.JourneyStateSatisfaction

type JourneyBacktrackIntent = semantics.JourneyBacktrackIntent

type JourneyNodeSelection = semantics.JourneyNodeSelection

type JourneyNodeEvidence struct {
	RelevanceScore      int                      `json:"relevance_score,omitempty"`
	RelevanceConditions []ConditionEvidence      `json:"relevance_conditions,omitempty"`
	LatestSatisfaction  JourneyStateSatisfaction `json:"latest_satisfaction,omitempty"`
	HistorySatisfaction JourneyStateSatisfaction `json:"history_satisfaction,omitempty"`
	EdgeCondition       ConditionEvidence        `json:"edge_condition,omitempty"`
}

type BacktrackCandidateEvaluation struct {
	Selection           JourneyNodeSelection `json:"selection"`
	Evidence            JourneyNodeEvidence  `json:"evidence,omitempty"`
	RelevanceScore      int                  `json:"relevance_score,omitempty"`
	LatestSatisfied     bool                 `json:"latest_satisfied,omitempty"`
	HistorySatisfied    bool                 `json:"history_satisfied,omitempty"`
	BranchSwitchScore   int                  `json:"branch_switch_score,omitempty"`
	FastForwardPathSize int                  `json:"fast_forward_path_size,omitempty"`
}

type BacktrackSelectionEvaluation struct {
	Candidate  BacktrackCandidateEvaluation `json:"candidate"`
	FallbackID string                       `json:"fallback_id,omitempty"`
}

type JourneyBacktrackEvaluation struct {
	ActiveJourney        *policy.Journey                         `json:"active_journey,omitempty"`
	ActiveJourneyState   *policy.JourneyNode                     `json:"active_journey_state,omitempty"`
	JourneyInstance      *journey.Instance                       `json:"journey_instance,omitempty"`
	BacktrackIntent      JourneyBacktrackIntent                  `json:"backtrack_intent,omitempty"`
	BacktrackEvaluations map[string]BacktrackCandidateEvaluation `json:"backtrack_evaluations,omitempty"`
	SelectedBacktrack    BacktrackSelectionEvaluation            `json:"selected_backtrack,omitempty"`
}

type JourneyNextNodeEvaluation struct {
	Selection       JourneyNodeSelection `json:"selection"`
	Evidence        JourneyNodeEvidence  `json:"evidence,omitempty"`
	RelevanceScore  int                  `json:"relevance_score,omitempty"`
	EdgeScore       int                  `json:"edge_score,omitempty"`
	LatestSatisfied bool                 `json:"latest_satisfied,omitempty"`
}

type JourneyProgressEvaluation struct {
	JourneySatisfactions map[string]JourneyStateSatisfaction  `json:"journey_satisfactions,omitempty"`
	NextNodeEvaluations  map[string]JourneyNextNodeEvaluation `json:"next_node_evaluations,omitempty"`
	SelectedNextNode     JourneyNextNodeEvaluation            `json:"selected_next_node,omitempty"`
}

type SiblingSuppressionDecision struct {
	LoserID    string `json:"loser_id,omitempty"`
	WinnerID   string `json:"winner_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Rationale  string `json:"rationale,omitempty"`
	ShouldDrop bool   `json:"should_drop,omitempty"`
}

type ToolDecision struct {
	SelectedTool     string              `json:"selected_tool,omitempty"`
	Arguments        map[string]any      `json:"arguments,omitempty"`
	ApprovalRequired bool                `json:"approval_required,omitempty"`
	CanRun           bool                `json:"can_run"`
	MissingArguments []string            `json:"missing_arguments,omitempty"`
	InvalidArguments []string            `json:"invalid_arguments,omitempty"`
	MissingIssues    []ToolArgumentIssue `json:"missing_issues,omitempty"`
	InvalidIssues    []ToolArgumentIssue `json:"invalid_issues,omitempty"`
	Rationale        string              `json:"rationale,omitempty"`
	Grounded         bool                `json:"grounded"`
}

type ToolDecisionEvaluation struct {
	PlannedSelectedTool string              `json:"planned_selected_tool,omitempty"`
	SelectedTools       []string            `json:"selected_tools,omitempty"`
	FinalSelectedTool   string              `json:"final_selected_tool,omitempty"`
	ApprovalRequired    bool                `json:"approval_required,omitempty"`
	CanRun              bool                `json:"can_run"`
	Grounded            bool                `json:"grounded"`
	MissingIssues       []ToolArgumentIssue `json:"missing_issues,omitempty"`
	InvalidIssues       []ToolArgumentIssue `json:"invalid_issues,omitempty"`
	Rationale           string              `json:"rationale,omitempty"`
}

type ToolPlanEvaluation struct {
	Candidates        []ToolCandidate                  `json:"candidates,omitempty"`
	Batches           []ToolCallBatchResult            `json:"batches,omitempty"`
	Grounding         map[string]ToolGroundingEvidence `json:"grounding,omitempty"`
	SelectionEvidence map[string]ToolSelectionEvidence `json:"selection_evidence,omitempty"`
	SelectedTool      string                           `json:"selected_tool,omitempty"`
	SelectedTools     []string                         `json:"selected_tools,omitempty"`
	OverlappingGroups [][]string                       `json:"overlapping_groups,omitempty"`
	Rationale         string                           `json:"rationale,omitempty"`
}

type ToolCandidate struct {
	ToolID               string                `json:"tool_id"`
	GroupKey             string                `json:"group_key,omitempty"`
	ReferenceTools       []string              `json:"reference_tools,omitempty"`
	RunInTandemWith      []string              `json:"run_in_tandem_with,omitempty"`
	Consequential        bool                  `json:"consequential,omitempty"`
	AutoApproved         bool                  `json:"auto_approved,omitempty"`
	Grounded             bool                  `json:"grounded"`
	GroundingEvidence    ToolGroundingEvidence `json:"grounding_evidence,omitempty"`
	ShouldRun            bool                  `json:"should_run,omitempty"`
	AlreadyStaged        bool                  `json:"already_staged,omitempty"`
	SameCallStaged       bool                  `json:"same_call_staged,omitempty"`
	AlreadySatisfied     bool                  `json:"already_satisfied,omitempty"`
	DecisionState        string                `json:"decision_state,omitempty"`
	ApprovalMode         string                `json:"approval_mode,omitempty"`
	Arguments            map[string]any        `json:"arguments,omitempty"`
	MissingIssues        []ToolArgumentIssue   `json:"missing_issues,omitempty"`
	InvalidIssues        []ToolArgumentIssue   `json:"invalid_issues,omitempty"`
	RejectedBy           string                `json:"rejected_by,omitempty"`
	PreparationRationale string                `json:"preparation_rationale,omitempty"`
	SelectionRationale   string                `json:"selection_rationale,omitempty"`
	Rationale            string                `json:"rationale,omitempty"`
}

type ToolGroundingEvidence = semantics.ToolGroundingEvidence

type ToolSelectionEvidence = semantics.ToolSelectionEvidence

type ToolCallBatchResult struct {
	Kind            string   `json:"kind"`
	CandidateTools  []string `json:"candidate_tools,omitempty"`
	ReferenceTools  []string `json:"reference_tools,omitempty"`
	RunInTandemWith []string `json:"run_in_tandem_with,omitempty"`
	SelectedTool    string   `json:"selected_tool,omitempty"`
	Consequential   bool     `json:"consequential,omitempty"`
	Simplified      bool     `json:"simplified,omitempty"`
	Rationale       string   `json:"rationale,omitempty"`
}

type ToolCallPlan struct {
	Candidates        []ToolCandidate       `json:"candidates,omitempty"`
	Batches           []ToolCallBatchResult `json:"batches,omitempty"`
	SelectedTool      string                `json:"selected_tool,omitempty"`
	SelectedTools     []string              `json:"selected_tools,omitempty"`
	Calls             []ToolPlannedCall     `json:"calls,omitempty"`
	OverlappingGroups [][]string            `json:"overlapping_groups,omitempty"`
	Rationale         string                `json:"rationale,omitempty"`
}

type ToolPlannedCall struct {
	ToolID    string         `json:"tool_id"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Rationale string         `json:"rationale,omitempty"`
}

type ToolArgumentIssue struct {
	Parameter    string   `json:"parameter"`
	Required     bool     `json:"required,omitempty"`
	Hidden       bool     `json:"hidden,omitempty"`
	HasDefault   bool     `json:"has_default,omitempty"`
	Choices      []string `json:"choices,omitempty"`
	Significance string   `json:"significance,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

type AnalyzedGuideline struct {
	ID                   string `json:"id"`
	AlreadySatisfied     bool   `json:"already_satisfied"`
	SatisfiedByToolEvent bool   `json:"satisfied_by_tool_event,omitempty"`
	SatisfactionSource   string `json:"satisfaction_source,omitempty"`
	AppliedDegree        string `json:"applied_degree,omitempty"`
	RequiresResponse     bool   `json:"requires_response"`
	RequiresTemplate     bool   `json:"requires_template"`
	Rationale            string `json:"rationale,omitempty"`
}

type ResponseAnalysis struct {
	AnalyzedGuidelines  []AnalyzedGuideline `json:"analyzed_guidelines,omitempty"`
	NeedsRevision       bool                `json:"needs_revision,omitempty"`
	NeedsStrictMode     bool                `json:"needs_strict_mode,omitempty"`
	RecommendedTemplate string              `json:"recommended_template,omitempty"`
	Rationale           string              `json:"rationale,omitempty"`
}

type ResponseAnalysisEvaluation struct {
	Coverage            map[string]ActionCoverageEvidence `json:"coverage,omitempty"`
	AnalyzedGuidelines  []AnalyzedGuideline               `json:"analyzed_guidelines,omitempty"`
	NeedsRevision       bool                              `json:"needs_revision,omitempty"`
	NeedsStrictMode     bool                              `json:"needs_strict_mode,omitempty"`
	RecommendedTemplate string                            `json:"recommended_template,omitempty"`
	Rationale           string                            `json:"rationale,omitempty"`
}

type VerificationResult struct {
	Status      string   `json:"status"`
	Reasons     []string `json:"reasons,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
}

type ARQResult struct {
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Output  map[string]any `json:"output,omitempty"`
}

type BatchResult struct {
	Name          string         `json:"name"`
	Strategy      string         `json:"strategy"`
	PromptVersion string         `json:"prompt_version,omitempty"`
	BatchSize     int            `json:"batch_size,omitempty"`
	RetryCount    int            `json:"retry_count,omitempty"`
	DurationMS    int64          `json:"duration_ms,omitempty"`
	Output        map[string]any `json:"output,omitempty"`
}

type EngineResult struct {
	Bundle                      *policy.Bundle
	Context                     MatchingContext
	Attention                   PolicyAttention
	ObservationStage            ObservationMatchStageResult
	MatchFinalizeStage          FinalizeStageResult
	PreviouslyAppliedStage      PreviouslyAppliedStageResult
	SuppressedGuidelines        []SuppressedGuideline
	ActiveJourney               *policy.Journey
	ActiveJourneyState          *policy.JourneyNode
	JourneyInstance             *journey.Instance
	ProjectedNodes              []ProjectedJourneyNode
	ResolutionRecords           []ResolutionRecord
	ConditionArtifactsStage     ConditionArtifactsStageResult
	JourneyBacktrackStage       JourneyBacktrackStageResult
	JourneyProgressStage        JourneyProgressStageResult
	CustomerDependencyStage     CustomerDependencyStageResult
	RelationshipResolutionStage RelationshipResolutionStageResult
	DisambiguationStage         DisambiguationStageResult
	RetrieverStage              RetrieverStageResult
	ResponseAnalysisStage       ResponseAnalysisStageResult
	ToolExposureStage           ToolExposureStageResult
	ToolPlanStage               ToolPlanStageResult
	ToolDecisionStage           ToolDecisionStageResult
	CompositionMode             string
	NoMatch                     string
	DisambiguationPrompt        string
	BatchResults                []BatchResult
	PromptSetVersions           map[string]string
	ARQResults                  []ARQResult
}

type RetrieverRegistry interface {
	GetRetriever(id string) retrieverdomain.Interface
}

type KnowledgeSearcher interface {
	SearchKnowledgeChunks(ctx context.Context, query knowledge.ChunkSearchQuery) ([]knowledge.Chunk, error)
}

type RetrieverMap map[string]retrieverdomain.Interface

func (m RetrieverMap) GetRetriever(id string) retrieverdomain.Interface {
	return m[id]
}

type ResolveOptions struct {
	Router            *model.Router
	RetrieverRegistry RetrieverRegistry
	KnowledgeSearcher KnowledgeSearcher
	KnowledgeSnapshot *knowledge.Snapshot
	KnowledgeChunks   []knowledge.Chunk
	DerivedSignals    []string
}
