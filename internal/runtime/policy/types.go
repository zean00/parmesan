package policyruntime

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
)

type MatchingContext struct {
	SessionID           string
	LatestCustomerText  string
	CustomerHistory     []string
	AssistantHistory    []string
	ConversationText    string
	AppliedGuidelines   []string
	AppliedInstructions []string
	OccurredAt          time.Time
}

type Match struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale,omitempty"`
}

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

type ProjectedJourneyNode struct {
	ID          string   `json:"id"`
	JourneyID   string   `json:"journey_id"`
	StateID     string   `json:"state_id"`
	Instruction string   `json:"instruction,omitempty"`
	FollowUps   []string `json:"follow_ups,omitempty"`
	ToolRefs    []string `json:"tool_refs,omitempty"`
	Priority    int      `json:"priority,omitempty"`
}

type JourneyDecision struct {
	Action       string   `json:"action,omitempty"`
	CurrentState string   `json:"current_state,omitempty"`
	NextState    string   `json:"next_state,omitempty"`
	BacktrackTo  string   `json:"backtrack_to,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
	Missing      []string `json:"missing,omitempty"`
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
	ID               string `json:"id"`
	AlreadySatisfied bool   `json:"already_satisfied"`
	RequiresResponse bool   `json:"requires_response"`
	RequiresTemplate bool   `json:"requires_template"`
	Rationale        string `json:"rationale,omitempty"`
}

type ResponseAnalysis struct {
	AnalyzedGuidelines  []AnalyzedGuideline `json:"analyzed_guidelines,omitempty"`
	NeedsRevision       bool                `json:"needs_revision,omitempty"`
	NeedsStrictMode     bool                `json:"needs_strict_mode,omitempty"`
	RecommendedTemplate string              `json:"recommended_template,omitempty"`
	Rationale           string              `json:"rationale,omitempty"`
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
	Output        map[string]any `json:"output,omitempty"`
}

type ResolvedView struct {
	Bundle               *policy.Bundle
	Context              MatchingContext
	Attention            PolicyAttention
	ObservationMatches   []Match
	GuidelineMatches     []Match
	ReapplyDecisions     []ReapplyDecision
	CustomerDecisions    []CustomerDependencyDecision
	MatchedObservations  []policy.Observation
	MatchedGuidelines    []policy.Guideline
	SuppressedGuidelines []SuppressedGuideline
	ActiveJourney        *policy.Journey
	ActiveJourneyState   *policy.JourneyNode
	JourneyInstance      *journey.Instance
	ProjectedNodes       []ProjectedJourneyNode
	JourneyDecision      JourneyDecision
	ExposedTools         []string
	ToolApprovals        map[string]string
	ToolDecision         ToolDecision
	ResponseAnalysis     ResponseAnalysis
	CandidateTemplates   []policy.Template
	CompositionMode      string
	NoMatch              string
	DisambiguationPrompt string
	BatchResults         []BatchResult
	PromptSetVersions    map[string]string
	ARQResults           []ARQResult
}
