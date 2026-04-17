package policy

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type ArtifactKind string

const (
	ObservationKind  ArtifactKind = "observation"
	GuidelineKind    ArtifactKind = "guideline"
	RelationshipKind ArtifactKind = "relationship"
	JourneyKind      ArtifactKind = "journey"
	TemplateKind     ArtifactKind = "template"
	ToolPolicyKind   ArtifactKind = "tool_policy"
)

type Bundle struct {
	ID                         string                      `json:"id" yaml:"id"`
	ArtifactMeta               artifactmeta.Meta           `json:"artifact_meta,omitempty" yaml:"-"`
	Version                    string                      `json:"version" yaml:"version"`
	CompositionMode            string                      `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	PerceivedPerformance       PerceivedPerformancePolicy  `json:"perceived_performance,omitempty" yaml:"perceived_performance,omitempty"`
	Semantics                  SemanticsPolicy             `json:"semantics,omitempty" yaml:"semantics,omitempty"`
	WatchCapabilities          []WatchCapability           `json:"watch_capabilities,omitempty" yaml:"watch_capabilities,omitempty"`
	DelegationContracts        []DelegationContract        `json:"delegation_contracts,omitempty" yaml:"delegation_contracts,omitempty"`
	DelegationWorkflows        []DelegationWorkflow        `json:"delegation_workflows,omitempty" yaml:"delegation_workflows,omitempty"`
	ResponseCapabilities       []ResponseCapability        `json:"response_capabilities,omitempty" yaml:"response_capabilities,omitempty"`
	QualityProfile             QualityProfile              `json:"quality_profile,omitempty" yaml:"quality_profile,omitempty"`
	LifecyclePolicy            LifecyclePolicy             `json:"lifecycle_policy,omitempty" yaml:"lifecycle_policy,omitempty"`
	CapabilityIsolation        CapabilityIsolation         `json:"capability_isolation,omitempty" yaml:"capability_isolation,omitempty"`
	NoMatch                    string                      `json:"no_match,omitempty" yaml:"no_match,omitempty"`
	DomainBoundary             DomainBoundary              `json:"domain_boundary,omitempty" yaml:"domain_boundary,omitempty"`
	Soul                       Soul                        `json:"soul,omitempty" yaml:"soul,omitempty"`
	SoulMarkdown               string                      `json:"soul_markdown,omitempty" yaml:"soul_markdown,omitempty"`
	Glossary                   []GlossaryTerm              `json:"glossary,omitempty" yaml:"glossary,omitempty"`
	ImportedAt                 time.Time                   `json:"imported_at" yaml:"-"`
	SourceYAML                 string                      `json:"source_yaml" yaml:"-"`
	Observations               []Observation               `json:"observations" yaml:"observations"`
	Guidelines                 []Guideline                 `json:"guidelines" yaml:"guidelines"`
	Relationships              []Relationship              `json:"relationships" yaml:"relationships"`
	Journeys                   []Journey                   `json:"journeys" yaml:"journeys"`
	Templates                  []Template                  `json:"templates" yaml:"templates"`
	ToolPolicies               []ToolPolicy                `json:"tool_policies" yaml:"tool_policies"`
	Retrievers                 []RetrieverBinding          `json:"retrievers,omitempty" yaml:"retrievers,omitempty"`
	GuidelineToolAssociations  []GuidelineToolAssociation  `json:"guideline_tool_associations,omitempty" yaml:"-"`
	GuidelineAgentAssociations []GuidelineAgentAssociation `json:"guideline_agent_associations,omitempty" yaml:"-"`
}

type GlossaryTerm struct {
	Term        string   `json:"term" yaml:"term"`
	Aliases     []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
}

type PerceivedPerformancePolicy struct {
	Mode                    string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	ProcessingIndicator     bool     `json:"processing_indicator_enabled,omitempty" yaml:"processing_indicator_enabled,omitempty"`
	PreambleEnabled         bool     `json:"preamble_enabled,omitempty" yaml:"preamble_enabled,omitempty"`
	PreambleDelayMS         int      `json:"preamble_delay_ms,omitempty" yaml:"preamble_delay_ms,omitempty"`
	ProcessingUpdateDelayMS int      `json:"processing_update_delay_ms,omitempty" yaml:"processing_update_delay_ms,omitempty"`
	Preambles               []string `json:"preambles,omitempty" yaml:"preambles,omitempty"`
	AllowedRiskTiers        []string `json:"allowed_risk_tiers,omitempty" yaml:"allowed_risk_tiers,omitempty"`
}

type SemanticsPolicy struct {
	Signals       []SemanticSignal   `json:"signals,omitempty" yaml:"signals,omitempty"`
	Categories    []SemanticCategory `json:"categories,omitempty" yaml:"categories,omitempty"`
	Slots         []SemanticSlot     `json:"slots,omitempty" yaml:"slots,omitempty"`
	RelativeDates []string           `json:"relative_dates,omitempty" yaml:"relative_dates,omitempty"`
}

type SemanticSignal struct {
	ID      string   `json:"id" yaml:"id"`
	Parent  string   `json:"parent,omitempty" yaml:"parent,omitempty"`
	Phrases []string `json:"phrases,omitempty" yaml:"phrases,omitempty"`
	Tokens  []string `json:"tokens,omitempty" yaml:"tokens,omitempty"`
	Aliases []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
}

type SemanticCategory struct {
	ID      string   `json:"id" yaml:"id"`
	Signals []string `json:"signals,omitempty" yaml:"signals,omitempty"`
}

type SemanticSlot struct {
	Field      string   `json:"field" yaml:"field"`
	Kind       string   `json:"kind" yaml:"kind"`
	Markers    []string `json:"markers,omitempty" yaml:"markers,omitempty"`
	StopTokens []string `json:"stop_tokens,omitempty" yaml:"stop_tokens,omitempty"`
}

type CapabilityIsolation struct {
	AllowedProviderIDs     []string            `json:"allowed_provider_ids,omitempty" yaml:"allowed_provider_ids,omitempty"`
	AllowedToolIDs         []string            `json:"allowed_tool_ids,omitempty" yaml:"allowed_tool_ids,omitempty"`
	AllowedAgentIDs        []string            `json:"allowed_agent_ids,omitempty" yaml:"allowed_agent_ids,omitempty"`
	AllowedRetrieverIDs    []string            `json:"allowed_retriever_ids,omitempty" yaml:"allowed_retriever_ids,omitempty"`
	AllowedKnowledgeScopes []KnowledgeScopeRef `json:"allowed_knowledge_scopes,omitempty" yaml:"allowed_knowledge_scopes,omitempty"`
}

type KnowledgeScopeRef struct {
	Kind string `json:"kind" yaml:"kind"`
	ID   string `json:"id" yaml:"id"`
}

type WatchCapability struct {
	ID                     string   `json:"id" yaml:"id"`
	Kind                   string   `json:"kind" yaml:"kind"`
	ScheduleStrategy       string   `json:"schedule_strategy,omitempty" yaml:"schedule_strategy,omitempty"`
	TriggerSignals         []string `json:"trigger_signals,omitempty" yaml:"trigger_signals,omitempty"`
	ToolMatchTerms         []string `json:"tool_match_terms,omitempty" yaml:"tool_match_terms,omitempty"`
	SubjectKeys            []string `json:"subject_keys,omitempty" yaml:"subject_keys,omitempty"`
	RequiredFields         []string `json:"required_fields,omitempty" yaml:"required_fields,omitempty"`
	RemindAtKeys           []string `json:"remind_at_keys,omitempty" yaml:"remind_at_keys,omitempty"`
	StatusKeys             []string `json:"status_keys,omitempty" yaml:"status_keys,omitempty"`
	PollIntervalSeconds    int      `json:"poll_interval_seconds,omitempty" yaml:"poll_interval_seconds,omitempty"`
	StopCondition          string   `json:"stop_condition,omitempty" yaml:"stop_condition,omitempty"`
	StopValues             []string `json:"stop_values,omitempty" yaml:"stop_values,omitempty"`
	ReminderLeadSeconds    int      `json:"reminder_lead_seconds,omitempty" yaml:"reminder_lead_seconds,omitempty"`
	AllowLifecycleFallback bool     `json:"allow_lifecycle_fallback,omitempty" yaml:"allow_lifecycle_fallback,omitempty"`
	DeliveryTemplate       string   `json:"delivery_template,omitempty" yaml:"delivery_template,omitempty"`
}

type DelegationContract struct {
	ID                 string                 `json:"id" yaml:"id"`
	AgentIDs           []string               `json:"agent_ids,omitempty" yaml:"agent_ids,omitempty"`
	ResourceType       string                 `json:"resource_type,omitempty" yaml:"resource_type,omitempty"`
	ResultTextField    string                 `json:"result_text_field,omitempty" yaml:"result_text_field,omitempty"`
	RequiredResultKeys []string               `json:"required_result_fields,omitempty" yaml:"required_result_fields,omitempty"`
	FieldAliases       []DelegationFieldAlias `json:"field_aliases,omitempty" yaml:"field_aliases,omitempty"`
	Verification       DelegationVerification `json:"verification,omitempty" yaml:"verification,omitempty"`
	WatchCapabilityID  string                 `json:"watch_capability_id,omitempty" yaml:"watch_capability_id,omitempty"`
	FailureUserMessage string                 `json:"failure_user_message,omitempty" yaml:"failure_user_message,omitempty"`
}

type DelegationWorkflow struct {
	ID              string                   `json:"id" yaml:"id"`
	Title           string                   `json:"title,omitempty" yaml:"title,omitempty"`
	Goal            string                   `json:"goal,omitempty" yaml:"goal,omitempty"`
	Steps           []DelegationWorkflowStep `json:"steps,omitempty" yaml:"steps,omitempty"`
	Constraints     []string                 `json:"constraints,omitempty" yaml:"constraints,omitempty"`
	SuccessCriteria []string                 `json:"success_criteria,omitempty" yaml:"success_criteria,omitempty"`
}

type DelegationWorkflowStep struct {
	ID          string   `json:"id" yaml:"id"`
	Instruction string   `json:"instruction" yaml:"instruction"`
	ToolIDs     []string `json:"tool_ids,omitempty" yaml:"tool_ids,omitempty"`
}

type ResponseCapability struct {
	ID                    string                        `json:"id" yaml:"id"`
	Mode                  string                        `json:"mode,omitempty" yaml:"mode,omitempty"`
	Facts                 []ResponseFact                `json:"facts,omitempty" yaml:"facts,omitempty"`
	Instructions          []string                      `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	Examples              []ResponseExample             `json:"examples,omitempty" yaml:"examples,omitempty"`
	DeterministicFallback ResponseDeterministicFallback `json:"deterministic_fallback,omitempty" yaml:"deterministic_fallback,omitempty"`
}

type ResponseFact struct {
	Key      string               `json:"key" yaml:"key"`
	Required bool                 `json:"required,omitempty" yaml:"required,omitempty"`
	Sources  []ResponseFactSource `json:"sources,omitempty" yaml:"sources,omitempty"`
}

type ResponseFactSource struct {
	ToolID string `json:"tool_id" yaml:"tool_id"`
	Path   string `json:"path" yaml:"path"`
}

type ResponseExample struct {
	Facts    map[string]any `json:"facts,omitempty" yaml:"facts,omitempty"`
	Messages []string       `json:"messages,omitempty" yaml:"messages,omitempty"`
}

type ResponseDeterministicFallback struct {
	Messages []ResponseDeterministicMessage `json:"messages,omitempty" yaml:"messages,omitempty"`
}

type ResponseDeterministicMessage struct {
	Text        string   `json:"text" yaml:"text"`
	WhenPresent []string `json:"when_present,omitempty" yaml:"when_present,omitempty"`
}

type DelegationFieldAlias struct {
	Target  string   `json:"target" yaml:"target"`
	Sources []string `json:"sources,omitempty" yaml:"sources,omitempty"`
}

type DelegationVerification struct {
	PrimaryToolID  string                       `json:"primary_tool_id,omitempty" yaml:"primary_tool_id,omitempty"`
	PrimaryArgs    map[string]string            `json:"primary_args,omitempty" yaml:"primary_args,omitempty"`
	FallbackTools  []DelegationVerificationTool `json:"fallback_tools,omitempty" yaml:"fallback_tools,omitempty"`
	ExtractPaths   []DelegationFieldAlias       `json:"extract_paths,omitempty" yaml:"extract_paths,omitempty"`
	RequireMatchOn []string                     `json:"require_match_on,omitempty" yaml:"require_match_on,omitempty"`
}

type DelegationVerificationTool struct {
	ToolID string            `json:"tool_id" yaml:"tool_id"`
	Args   map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
}

type QualityClaimProfile struct {
	ID                     string   `json:"id" yaml:"id"`
	MatchTerms             []string `json:"match_terms,omitempty" yaml:"match_terms,omitempty"`
	Risk                   string   `json:"risk,omitempty" yaml:"risk,omitempty"`
	RequiredEvidence       []string `json:"required_evidence,omitempty" yaml:"required_evidence,omitempty"`
	RequiredVerification   []string `json:"required_verification,omitempty" yaml:"required_verification,omitempty"`
	AllowedCommitments     []string `json:"allowed_commitments,omitempty" yaml:"allowed_commitments,omitempty"`
	VerificationQualifiers []string `json:"verification_qualifiers,omitempty" yaml:"verification_qualifiers,omitempty"`
	ContradictionMarkers   []string `json:"contradiction_markers,omitempty" yaml:"contradiction_markers,omitempty"`
}

type QualityProfile struct {
	ID                        string                `json:"id,omitempty" yaml:"id,omitempty"`
	RiskTier                  string                `json:"risk_tier,omitempty" yaml:"risk_tier,omitempty"`
	AllowedCommitments        []string              `json:"allowed_commitments,omitempty" yaml:"allowed_commitments,omitempty"`
	RequiredEvidence          []string              `json:"required_evidence,omitempty" yaml:"required_evidence,omitempty"`
	RequiredVerificationSteps []string              `json:"required_verification_steps,omitempty" yaml:"required_verification_steps,omitempty"`
	BlueprintRules            map[string][]string   `json:"blueprint_rules,omitempty" yaml:"blueprint_rules,omitempty"`
	ClaimProfiles             []QualityClaimProfile `json:"claim_profiles,omitempty" yaml:"claim_profiles,omitempty"`
	SemanticConcepts          map[string][]string   `json:"semantic_concepts,omitempty" yaml:"semantic_concepts,omitempty"`
	HighRiskIndicators        []string              `json:"high_risk_indicators,omitempty" yaml:"high_risk_indicators,omitempty"`
	RefusalSignals            []string              `json:"refusal_signals,omitempty" yaml:"refusal_signals,omitempty"`
	EscalationSignals         []string              `json:"escalation_signals,omitempty" yaml:"escalation_signals,omitempty"`
	MinimumOverall            float64               `json:"minimum_overall,omitempty" yaml:"minimum_overall,omitempty"`
}

type LifecyclePolicy struct {
	ID                         string   `json:"id,omitempty" yaml:"id,omitempty"`
	IdleCandidateAfterMS       int      `json:"idle_candidate_after_ms,omitempty" yaml:"idle_candidate_after_ms,omitempty"`
	AwaitingCloseAfterMS       int      `json:"awaiting_close_after_ms,omitempty" yaml:"awaiting_close_after_ms,omitempty"`
	KeepRecheckAfterMS         int      `json:"keep_recheck_after_ms,omitempty" yaml:"keep_recheck_after_ms,omitempty"`
	FollowupMessage            string   `json:"followup_message,omitempty" yaml:"followup_message,omitempty"`
	ResolutionSignals          []string `json:"resolution_signals,omitempty" yaml:"resolution_signals,omitempty"`
	DeliveryUpdateSignals      []string `json:"delivery_update_signals,omitempty" yaml:"delivery_update_signals,omitempty"`
	AppointmentReminderSignals []string `json:"appointment_reminder_signals,omitempty" yaml:"appointment_reminder_signals,omitempty"`
}

type DomainBoundary struct {
	Mode              string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	AllowedTopics     []string `json:"allowed_topics,omitempty" yaml:"allowed_topics,omitempty"`
	AdjacentTopics    []string `json:"adjacent_topics,omitempty" yaml:"adjacent_topics,omitempty"`
	BlockedTopics     []string `json:"blocked_topics,omitempty" yaml:"blocked_topics,omitempty"`
	AdjacentAction    string   `json:"adjacent_action,omitempty" yaml:"adjacent_action,omitempty"`
	UncertaintyAction string   `json:"uncertainty_action,omitempty" yaml:"uncertainty_action,omitempty"`
	OutOfScopeReply   string   `json:"out_of_scope_reply,omitempty" yaml:"out_of_scope_reply,omitempty"`
}

type Soul struct {
	Identity           string   `json:"identity,omitempty" yaml:"identity,omitempty"`
	Role               string   `json:"role,omitempty" yaml:"role,omitempty"`
	Brand              string   `json:"brand,omitempty" yaml:"brand,omitempty"`
	DefaultLanguage    string   `json:"default_language,omitempty" yaml:"default_language,omitempty"`
	SupportedLanguages []string `json:"supported_languages,omitempty" yaml:"supported_languages,omitempty"`
	LanguageMatching   string   `json:"language_matching,omitempty" yaml:"language_matching,omitempty"`
	Tone               string   `json:"tone,omitempty" yaml:"tone,omitempty"`
	Formality          string   `json:"formality,omitempty" yaml:"formality,omitempty"`
	Verbosity          string   `json:"verbosity,omitempty" yaml:"verbosity,omitempty"`
	StyleRules         []string `json:"style_rules,omitempty" yaml:"style_rules,omitempty"`
	AvoidRules         []string `json:"avoid_rules,omitempty" yaml:"avoid_rules,omitempty"`
	EscalationStyle    string   `json:"escalation_style,omitempty" yaml:"escalation_style,omitempty"`
	FormattingRules    []string `json:"formatting_rules,omitempty" yaml:"formatting_rules,omitempty"`
}

type MCPRef struct {
	Server string   `json:"server,omitempty" yaml:"server,omitempty"`
	Tool   string   `json:"tool,omitempty" yaml:"tool,omitempty"`
	Tools  []string `json:"tools,omitempty" yaml:"tools,omitempty"`
}

type Observation struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	When         string            `json:"when" yaml:"when"`
	Tools        []string          `json:"tools,omitempty" yaml:"tools,omitempty"`
	MCP          *MCPRef           `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Matcher      string            `json:"matcher,omitempty" yaml:"matcher,omitempty"`
	Criticality  string            `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Tags         []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Priority     int               `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type Guideline struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	When         string            `json:"when" yaml:"when"`
	Then         string            `json:"then" yaml:"then"`
	Tools        []string          `json:"tools,omitempty" yaml:"tools,omitempty"`
	Agents       []string          `json:"agents,omitempty" yaml:"agents,omitempty"`
	AgentBindings []GuidelineAgentBinding `json:"agent_bindings,omitempty" yaml:"agent_bindings,omitempty"`
	ResponseCapabilityID string           `json:"response_capability_id,omitempty" yaml:"response_capability_id,omitempty"`
	MCP          *MCPRef           `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Scope        string            `json:"scope,omitempty" yaml:"scope,omitempty"`
	Matcher      string            `json:"matcher,omitempty" yaml:"matcher,omitempty"`
	Criticality  string            `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Tags         []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Track        bool              `json:"track,omitempty" yaml:"track,omitempty"`
	Continuous   bool              `json:"continuous,omitempty" yaml:"continuous,omitempty"`
	Priority     int               `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type Relationship struct {
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	Source       string            `json:"source" yaml:"source"`
	Kind         string            `json:"kind" yaml:"kind"`
	Target       string            `json:"target" yaml:"target"`
}

type Journey struct {
	ID              string            `json:"id" yaml:"id"`
	ArtifactMeta    artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	When            []string          `json:"when" yaml:"when"`
	RootID          string            `json:"root_id,omitempty" yaml:"root_id,omitempty"`
	States          []JourneyNode     `json:"states" yaml:"states"`
	Edges           []JourneyEdge     `json:"edges,omitempty" yaml:"edges,omitempty"`
	Guidelines      []Guideline       `json:"guidelines,omitempty" yaml:"guidelines,omitempty"`
	Templates       []Template        `json:"templates,omitempty" yaml:"templates,omitempty"`
	Tags            []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Labels          []string          `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CompositionMode string            `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	Priority        int               `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type JourneyNode struct {
	ID              string            `json:"id" yaml:"id"`
	ArtifactMeta    artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	Type            string            `json:"type" yaml:"type"`
	Instruction     string            `json:"instruction,omitempty" yaml:"instruction,omitempty"`
	Description     string            `json:"description,omitempty" yaml:"description,omitempty"`
	Tool            string            `json:"tool,omitempty" yaml:"tool,omitempty"`
	Agent           string            `json:"agent,omitempty" yaml:"agent,omitempty"`
	ResponseCapabilityID string       `json:"response_capability_id,omitempty" yaml:"response_capability_id,omitempty"`
	MCP             *MCPRef           `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	When            []string          `json:"when,omitempty" yaml:"when,omitempty"`
	Next            []string          `json:"next,omitempty" yaml:"next,omitempty"`
	Mode            string            `json:"mode,omitempty" yaml:"mode,omitempty"`
	Kind            string            `json:"kind,omitempty" yaml:"kind,omitempty"`
	Labels          []string          `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CompositionMode string            `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	Priority        int               `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type JourneyEdge struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	Source       string            `json:"source" yaml:"source"`
	Target       string            `json:"target" yaml:"target"`
	Condition    string            `json:"condition,omitempty" yaml:"condition,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type Template struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	Mode         string            `json:"mode" yaml:"mode"`
	Text         string            `json:"text,omitempty" yaml:"text,omitempty"`
	Messages     []string          `json:"messages,omitempty" yaml:"messages,omitempty"`
	When         string            `json:"when,omitempty" yaml:"when,omitempty"`
}

type ToolPolicy struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	ToolIDs      []string          `json:"tool_ids" yaml:"tool_ids"`
	Exposure     string            `json:"exposure" yaml:"exposure"`
	Approval     string            `json:"approval,omitempty" yaml:"approval,omitempty"`
}

type RetrieverBinding struct {
	ID           string            `json:"id" yaml:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty" yaml:"-"`
	Kind         string            `json:"kind" yaml:"kind"`
	Scope        string            `json:"scope" yaml:"scope"`
	TargetID     string            `json:"target_id,omitempty" yaml:"target_id,omitempty"`
	Mode         string            `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxResults   int               `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Config       map[string]any    `json:"config,omitempty" yaml:"config,omitempty"`
}

type GuidelineToolAssociation struct {
	GuidelineID string `json:"guideline_id"`
	ToolID      string `json:"tool_id"`
}

type GuidelineAgentAssociation struct {
	GuidelineID string `json:"guideline_id"`
	AgentID     string `json:"agent_id"`
	WorkflowID  string `json:"workflow_id,omitempty"`
}

type GuidelineAgentBinding struct {
	AgentID    string `json:"agent_id" yaml:"agent_id"`
	WorkflowID string `json:"workflow_id,omitempty" yaml:"workflow_id,omitempty"`
}
