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
	Relationships   []FixtureRelationship `yaml:"relationships" json:"relationships"`
	CannedResponses []string              `yaml:"canned_responses" json:"canned_responses"`
	Tools           []FixtureTool         `yaml:"tools" json:"tools"`
	Associations    []FixtureAssociation  `yaml:"associations" json:"associations"`
}

type FixtureGuideline struct {
	ID                string `yaml:"id" json:"id"`
	Kind              string `yaml:"kind" json:"kind"`
	Condition         string `yaml:"condition" json:"condition"`
	Action            string `yaml:"action" json:"action"`
	CustomerDependent bool   `yaml:"customer_dependent" json:"customer_dependent"`
}

type FixtureRelationship struct {
	Kind   string `yaml:"kind" json:"kind"`
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
}

type FixtureTool struct {
	ID string `yaml:"id" json:"id"`
}

type FixtureAssociation struct {
	Guideline string `yaml:"guideline" json:"guideline"`
	Tool      string `yaml:"tool" json:"tool"`
}

type Expectations struct {
	MatchedObservations    []string                    `yaml:"matched_observations" json:"matched_observations"`
	MatchedGuidelines      []string                    `yaml:"matched_guidelines" json:"matched_guidelines"`
	SuppressedGuidelines   []string                    `yaml:"suppressed_guidelines" json:"suppressed_guidelines"`
	SuppressionReasons     []string                    `yaml:"suppression_reasons" json:"suppression_reasons"`
	ActiveJourney          *IDExpectation              `yaml:"active_journey" json:"active_journey"`
	JourneyDecision        string                      `yaml:"journey_decision" json:"journey_decision"`
	NextJourneyNode        string                      `yaml:"next_journey_node" json:"next_journey_node"`
	ExposedTools           []string                    `yaml:"exposed_tools" json:"exposed_tools"`
	SelectedTool           string                      `yaml:"selected_tool" json:"selected_tool"`
	ResponseMode           string                      `yaml:"response_mode" json:"response_mode"`
	NoMatch                *bool                       `yaml:"no_match" json:"no_match"`
	SelectedTemplate       *string                     `yaml:"selected_template" json:"selected_template"`
	VerificationOutcome    string                      `yaml:"verification_outcome" json:"verification_outcome"`
	ResponseAnalysis       ResponseAnalysisExpectation `yaml:"response_analysis" json:"response_analysis"`
	DisambiguationRequired *bool                       `yaml:"disambiguation_required" json:"disambiguation_required"`
	ResponseSemantics      ResponseSemantics           `yaml:"response_semantics" json:"response_semantics"`
}

type IDExpectation struct {
	ID string `yaml:"id" json:"id"`
}

type ResponseAnalysisExpectation struct {
	StillRequired    []string `yaml:"still_required" json:"still_required"`
	AlreadySatisfied []string `yaml:"already_satisfied" json:"already_satisfied"`
}

type ResponseSemantics struct {
	MustInclude    []string `yaml:"must_include" json:"must_include"`
	MustNotInclude []string `yaml:"must_not_include" json:"must_not_include"`
}

type ToolCall struct {
	ToolID    string         `json:"tool_id"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type NormalizedResult struct {
	MatchedObservations  []string   `json:"matched_observations,omitempty"`
	MatchedGuidelines    []string   `json:"matched_guidelines,omitempty"`
	SuppressedGuidelines []string   `json:"suppressed_guidelines,omitempty"`
	SuppressionReasons   []string   `json:"suppression_reasons,omitempty"`
	ActiveJourney        string     `json:"active_journey,omitempty"`
	ActiveJourneyNode    string     `json:"active_journey_node,omitempty"`
	JourneyDecision      string     `json:"journey_decision,omitempty"`
	NextJourneyNode      string     `json:"next_journey_node,omitempty"`
	ExposedTools         []string   `json:"exposed_tools,omitempty"`
	SelectedTool         string     `json:"selected_tool,omitempty"`
	ToolCanRun           *bool      `json:"tool_can_run,omitempty"`
	ResponseMode         string     `json:"response_mode,omitempty"`
	NoMatch              bool       `json:"no_match,omitempty"`
	SelectedTemplate     string     `json:"selected_template,omitempty"`
	VerificationOutcome  string     `json:"verification_outcome,omitempty"`
	ResponseText         string     `json:"response_text,omitempty"`
	ToolCalls            []ToolCall `json:"tool_calls,omitempty"`
	UnsupportedFields    []string   `json:"unsupported_fields,omitempty"`
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
