package parity

type Fixture struct {
	Version       string            `yaml:"version" json:"version"`
	Description   string            `yaml:"description" json:"description"`
	Normalization NormalizationSpec `yaml:"normalization" json:"normalization"`
	Scenarios     []Scenario        `yaml:"scenarios" json:"scenarios"`
}

type NormalizationSpec struct {
	Compare []string `yaml:"compare" json:"compare"`
	Notes   []string `yaml:"notes" json:"notes"`
}

type Scenario struct {
	ID                string       `yaml:"id" json:"id"`
	Title             string       `yaml:"title" json:"title"`
	Category          string       `yaml:"category" json:"category"`
	SourceRefs        []string     `yaml:"source_refs" json:"source_refs"`
	AgentName         string       `yaml:"agent_name" json:"agent_name"`
	AgentJob          string       `yaml:"agent_job" json:"agent_job"`
	Mode              string       `yaml:"mode" json:"mode"`
	SkipEngineDiff    bool         `yaml:"skip_engine_diff" json:"skip_engine_diff"`
	SkipParlantExpect bool         `yaml:"skip_parlant_expectations" json:"skip_parlant_expectations"`
	TimeoutSeconds    int          `yaml:"timeout_seconds" json:"timeout_seconds"`
	Transcript        []Turn       `yaml:"transcript" json:"transcript"`
	PriorState        PriorState   `yaml:"prior_state" json:"prior_state"`
	PolicySetup       PolicySetup  `yaml:"policy_setup" json:"policy_setup"`
	Expect            Expectations `yaml:"expectations" json:"expectations"`
}

type Turn struct {
	Role string `yaml:"role" json:"role"`
	Text string `yaml:"text" json:"text"`
}

type PriorState struct {
	ActiveJourney string   `yaml:"active_journey" json:"active_journey"`
	JourneyPath   []string `yaml:"journey_path" json:"journey_path"`
}

type PolicySetup struct {
	ExtraGuidelines []FixtureGuideline    `yaml:"extra_guidelines" json:"extra_guidelines"`
	Journeys        []FixtureJourney      `yaml:"journeys" json:"journeys"`
	Relationships   []FixtureRelationship `yaml:"relationships" json:"relationships"`
	CannedResponses []string              `yaml:"canned_responses" json:"canned_responses"`
	Tools           []FixtureTool         `yaml:"tools" json:"tools"`
	Associations    []FixtureAssociation  `yaml:"associations" json:"associations"`
	StagedToolCalls []FixtureToolCall     `yaml:"staged_tool_calls" json:"staged_tool_calls"`
}

type FixtureGuideline struct {
	ID                string   `yaml:"id" json:"id"`
	Kind              string   `yaml:"kind" json:"kind"`
	Condition         string   `yaml:"condition" json:"condition"`
	Action            string   `yaml:"action" json:"action"`
	CustomerDependent bool     `yaml:"customer_dependent" json:"customer_dependent"`
	Matcher           string   `yaml:"matcher" json:"matcher"`
	Criticality       string   `yaml:"criticality" json:"criticality"`
	Tags              []string `yaml:"tags" json:"tags"`
	Track             *bool    `yaml:"track" json:"track"`
	Continuous        bool     `yaml:"continuous" json:"continuous"`
	Priority          int      `yaml:"priority" json:"priority"`
}

type FixtureRelationship struct {
	Kind   string `yaml:"kind" json:"kind"`
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
}

type FixtureTool struct {
	ID            string         `yaml:"id" json:"id"`
	Description   string         `yaml:"description" json:"description"`
	Consequential bool           `yaml:"consequential" json:"consequential"`
	OverlapGroup  string         `yaml:"overlap_group" json:"overlap_group"`
	ModulePath    string         `yaml:"module_path" json:"module_path"`
	DocumentID    string         `yaml:"document_id" json:"document_id"`
	Schema        map[string]any `yaml:"schema" json:"schema"`
}

type FixtureAssociation struct {
	Guideline string `yaml:"guideline" json:"guideline"`
	Tool      string `yaml:"tool" json:"tool"`
}

type FixtureToolCall struct {
	ToolID     string         `yaml:"tool_id" json:"tool_id"`
	Arguments  map[string]any `yaml:"arguments" json:"arguments"`
	Result     map[string]any `yaml:"result" json:"result"`
	DocumentID string         `yaml:"document_id" json:"document_id"`
	ModulePath string         `yaml:"module_path" json:"module_path"`
}

type FixtureJourney struct {
	ID              string               `yaml:"id" json:"id"`
	When            []string             `yaml:"when" json:"when"`
	RootID          string               `yaml:"root_id" json:"root_id"`
	Priority        int                  `yaml:"priority" json:"priority"`
	Tags            []string             `yaml:"tags" json:"tags"`
	Labels          []string             `yaml:"labels" json:"labels"`
	Metadata        map[string]any       `yaml:"metadata" json:"metadata"`
	CompositionMode string               `yaml:"composition_mode" json:"composition_mode"`
	States          []FixtureJourneyNode `yaml:"states" json:"states"`
	Edges           []FixtureJourneyEdge `yaml:"edges" json:"edges"`
}

type FixtureJourneyNode struct {
	ID              string         `yaml:"id" json:"id"`
	Type            string         `yaml:"type" json:"type"`
	Instruction     string         `yaml:"instruction" json:"instruction"`
	Description     string         `yaml:"description" json:"description"`
	Tool            string         `yaml:"tool" json:"tool"`
	When            []string       `yaml:"when" json:"when"`
	Next            []string       `yaml:"next" json:"next"`
	Mode            string         `yaml:"mode" json:"mode"`
	Kind            string         `yaml:"kind" json:"kind"`
	Labels          []string       `yaml:"labels" json:"labels"`
	Metadata        map[string]any `yaml:"metadata" json:"metadata"`
	CompositionMode string         `yaml:"composition_mode" json:"composition_mode"`
	Priority        int            `yaml:"priority" json:"priority"`
}

type FixtureJourneyEdge struct {
	ID        string         `yaml:"id" json:"id"`
	Source    string         `yaml:"source" json:"source"`
	Target    string         `yaml:"target" json:"target"`
	Condition string         `yaml:"condition" json:"condition"`
	Metadata  map[string]any `yaml:"metadata" json:"metadata"`
}

type Expectations struct {
	MatchedObservations     []string                    `yaml:"matched_observations" json:"matched_observations"`
	MatchedGuidelines       []string                    `yaml:"matched_guidelines" json:"matched_guidelines"`
	SuppressedGuidelines    []string                    `yaml:"suppressed_guidelines" json:"suppressed_guidelines"`
	SuppressionReasons      []string                    `yaml:"suppression_reasons" json:"suppression_reasons"`
	ResolutionRecords       []ResolutionExpectation     `yaml:"resolution_records" json:"resolution_records"`
	ActiveJourney           *IDExpectation              `yaml:"active_journey" json:"active_journey"`
	JourneyDecision         string                      `yaml:"journey_decision" json:"journey_decision"`
	NextJourneyNode         string                      `yaml:"next_journey_node" json:"next_journey_node"`
	ProjectedFollowUps      map[string][]string         `yaml:"projected_follow_ups" json:"projected_follow_ups"`
	LegalFollowUps          map[string][]string         `yaml:"legal_follow_ups" json:"legal_follow_ups"`
	ExposedTools            []string                    `yaml:"exposed_tools" json:"exposed_tools"`
	ToolCandidates          []string                    `yaml:"tool_candidates" json:"tool_candidates"`
	ToolCandidateStates     map[string]string           `yaml:"tool_candidate_states" json:"tool_candidate_states"`
	ToolCandidateRejectedBy map[string]string           `yaml:"tool_candidate_rejected_by" json:"tool_candidate_rejected_by"`
	ToolCandidateReasons    map[string]string           `yaml:"tool_candidate_reasons" json:"tool_candidate_reasons"`
	ToolCandidateTandemWith map[string][]string         `yaml:"tool_candidate_tandem_with" json:"tool_candidate_tandem_with"`
	OverlappingToolGroups   [][]string                  `yaml:"overlapping_tool_groups" json:"overlapping_tool_groups"`
	SelectedTool            string                      `yaml:"selected_tool" json:"selected_tool"`
	SelectedTools           []string                    `yaml:"selected_tools" json:"selected_tools"`
	ToolCallCount           int                         `yaml:"tool_call_count" json:"tool_call_count"`
	ToolCallTools           []string                    `yaml:"tool_call_tools" json:"tool_call_tools"`
	ResponseMode            string                      `yaml:"response_mode" json:"response_mode"`
	NoMatch                 *bool                       `yaml:"no_match" json:"no_match"`
	SelectedTemplate        *string                     `yaml:"selected_template" json:"selected_template"`
	VerificationOutcome     string                      `yaml:"verification_outcome" json:"verification_outcome"`
	ResponseAnalysis        ResponseAnalysisExpectation `yaml:"response_analysis" json:"response_analysis"`
	DisambiguationRequired  *bool                       `yaml:"disambiguation_required" json:"disambiguation_required"`
	ResponseSemantics       ResponseSemantics           `yaml:"response_semantics" json:"response_semantics"`
}

type IDExpectation struct {
	ID string `yaml:"id" json:"id"`
}

type ResolutionExpectation struct {
	EntityID string `yaml:"entity_id" json:"entity_id"`
	Kind     string `yaml:"kind" json:"kind"`
}

type ResponseAnalysisExpectation struct {
	StillRequired        []string          `yaml:"still_required" json:"still_required"`
	AlreadySatisfied     []string          `yaml:"already_satisfied" json:"already_satisfied"`
	PartiallyApplied     []string          `yaml:"partially_applied" json:"partially_applied"`
	SatisfiedByToolEvent []string          `yaml:"satisfied_by_tool_event" json:"satisfied_by_tool_event"`
	SatisfactionSources  map[string]string `yaml:"satisfaction_sources" json:"satisfaction_sources"`
}

type ResponseSemantics struct {
	MustInclude    []string `yaml:"must_include" json:"must_include"`
	MustNotInclude []string `yaml:"must_not_include" json:"must_not_include"`
}

type ToolCall struct {
	ToolID     string         `json:"tool_id"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	DocumentID string         `json:"document_id,omitempty"`
	ModulePath string         `json:"module_path,omitempty"`
}

type NormalizedResolution struct {
	EntityID string `json:"entity_id"`
	Kind     string `json:"kind"`
}

type NormalizedResult struct {
	MatchedObservations              []string               `json:"matched_observations,omitempty"`
	MatchedGuidelines                []string               `json:"matched_guidelines,omitempty"`
	SuppressedGuidelines             []string               `json:"suppressed_guidelines,omitempty"`
	SuppressionReasons               []string               `json:"suppression_reasons,omitempty"`
	ResolutionRecords                []NormalizedResolution `json:"resolution_records,omitempty"`
	ActiveJourney                    string                 `json:"active_journey,omitempty"`
	ActiveJourneyNode                string                 `json:"active_journey_node,omitempty"`
	JourneyDecision                  string                 `json:"journey_decision,omitempty"`
	NextJourneyNode                  string                 `json:"next_journey_node,omitempty"`
	ProjectedFollowUps               map[string][]string    `json:"projected_follow_ups,omitempty"`
	LegalFollowUps                   map[string][]string    `json:"legal_follow_ups,omitempty"`
	ExposedTools                     []string               `json:"exposed_tools,omitempty"`
	ToolCandidates                   []string               `json:"tool_candidates,omitempty"`
	ToolCandidateStates              map[string]string      `json:"tool_candidate_states,omitempty"`
	ToolCandidateRejectedBy          map[string]string      `json:"tool_candidate_rejected_by,omitempty"`
	ToolCandidateReasons             map[string]string      `json:"tool_candidate_reasons,omitempty"`
	ToolCandidateTandemWith          map[string][]string    `json:"tool_candidate_tandem_with,omitempty"`
	OverlappingToolGroups            [][]string             `json:"overlapping_tool_groups,omitempty"`
	SelectedTool                     string                 `json:"selected_tool,omitempty"`
	SelectedTools                    []string               `json:"selected_tools,omitempty"`
	ToolCallTools                    []string               `json:"tool_call_tools,omitempty"`
	ToolCanRun                       *bool                  `json:"tool_can_run,omitempty"`
	ResponseMode                     string                 `json:"response_mode,omitempty"`
	NoMatch                          bool                   `json:"no_match,omitempty"`
	SelectedTemplate                 string                 `json:"selected_template,omitempty"`
	VerificationOutcome              string                 `json:"verification_outcome,omitempty"`
	ResponseAnalysisStillRequired    []string               `json:"response_analysis_still_required,omitempty"`
	ResponseAnalysisAlreadySatisfied []string               `json:"response_analysis_already_satisfied,omitempty"`
	ResponseAnalysisPartiallyApplied []string               `json:"response_analysis_partially_applied,omitempty"`
	ResponseAnalysisToolSatisfied    []string               `json:"response_analysis_tool_satisfied,omitempty"`
	ResponseAnalysisSources          map[string]string      `json:"response_analysis_sources,omitempty"`
	ResponseText                     string                 `json:"response_text,omitempty"`
	ToolCalls                        []ToolCall             `json:"tool_calls,omitempty"`
	UnsupportedFields                []string               `json:"unsupported_fields,omitempty"`
}

type ScenarioReport struct {
	Scenario          Scenario         `json:"scenario"`
	Parmesan          NormalizedResult `json:"parmesan"`
	Parlant           NormalizedResult `json:"parlant"`
	ExpectationErrors []string         `json:"expectation_errors,omitempty"`
	DiffErrors        []string         `json:"diff_errors,omitempty"`
	Passed            bool             `json:"passed"`
}

type Report struct {
	FixtureVersion string           `json:"fixture_version"`
	ScenarioCount  int              `json:"scenario_count"`
	PassedCount    int              `json:"passed_count"`
	FailedCount    int              `json:"failed_count"`
	Scenarios      []ScenarioReport `json:"scenarios"`
}
