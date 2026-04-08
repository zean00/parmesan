package policy

import "time"

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
	ID                        string                     `json:"id" yaml:"id"`
	Version                   string                     `json:"version" yaml:"version"`
	CompositionMode           string                     `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	NoMatch                   string                     `json:"no_match,omitempty" yaml:"no_match,omitempty"`
	DomainBoundary            DomainBoundary             `json:"domain_boundary,omitempty" yaml:"domain_boundary,omitempty"`
	Soul                      Soul                       `json:"soul,omitempty" yaml:"soul,omitempty"`
	SoulMarkdown              string                     `json:"soul_markdown,omitempty" yaml:"soul_markdown,omitempty"`
	ImportedAt                time.Time                  `json:"imported_at" yaml:"-"`
	SourceYAML                string                     `json:"source_yaml" yaml:"-"`
	Observations              []Observation              `json:"observations" yaml:"observations"`
	Guidelines                []Guideline                `json:"guidelines" yaml:"guidelines"`
	Relationships             []Relationship             `json:"relationships" yaml:"relationships"`
	Journeys                  []Journey                  `json:"journeys" yaml:"journeys"`
	Templates                 []Template                 `json:"templates" yaml:"templates"`
	ToolPolicies              []ToolPolicy               `json:"tool_policies" yaml:"tool_policies"`
	Retrievers                []RetrieverBinding         `json:"retrievers,omitempty" yaml:"retrievers,omitempty"`
	GuidelineToolAssociations []GuidelineToolAssociation `json:"guideline_tool_associations,omitempty" yaml:"-"`
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
	ID          string   `json:"id" yaml:"id"`
	When        string   `json:"when" yaml:"when"`
	Tools       []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	MCP         *MCPRef  `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Matcher     string   `json:"matcher,omitempty" yaml:"matcher,omitempty"`
	Criticality string   `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Priority    int      `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type Guideline struct {
	ID          string   `json:"id" yaml:"id"`
	When        string   `json:"when" yaml:"when"`
	Then        string   `json:"then" yaml:"then"`
	Tools       []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	MCP         *MCPRef  `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Scope       string   `json:"scope,omitempty" yaml:"scope,omitempty"`
	Matcher     string   `json:"matcher,omitempty" yaml:"matcher,omitempty"`
	Criticality string   `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Track       bool     `json:"track,omitempty" yaml:"track,omitempty"`
	Continuous  bool     `json:"continuous,omitempty" yaml:"continuous,omitempty"`
	Priority    int      `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type Relationship struct {
	Source string `json:"source" yaml:"source"`
	Kind   string `json:"kind" yaml:"kind"`
	Target string `json:"target" yaml:"target"`
}

type Journey struct {
	ID              string         `json:"id" yaml:"id"`
	When            []string       `json:"when" yaml:"when"`
	RootID          string         `json:"root_id,omitempty" yaml:"root_id,omitempty"`
	States          []JourneyNode  `json:"states" yaml:"states"`
	Edges           []JourneyEdge  `json:"edges,omitempty" yaml:"edges,omitempty"`
	Guidelines      []Guideline    `json:"guidelines,omitempty" yaml:"guidelines,omitempty"`
	Templates       []Template     `json:"templates,omitempty" yaml:"templates,omitempty"`
	Tags            []string       `json:"tags,omitempty" yaml:"tags,omitempty"`
	Labels          []string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CompositionMode string         `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	Priority        int            `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type JourneyNode struct {
	ID              string         `json:"id" yaml:"id"`
	Type            string         `json:"type" yaml:"type"`
	Instruction     string         `json:"instruction,omitempty" yaml:"instruction,omitempty"`
	Description     string         `json:"description,omitempty" yaml:"description,omitempty"`
	Tool            string         `json:"tool,omitempty" yaml:"tool,omitempty"`
	MCP             *MCPRef        `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	When            []string       `json:"when,omitempty" yaml:"when,omitempty"`
	Next            []string       `json:"next,omitempty" yaml:"next,omitempty"`
	Mode            string         `json:"mode,omitempty" yaml:"mode,omitempty"`
	Kind            string         `json:"kind,omitempty" yaml:"kind,omitempty"`
	Labels          []string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CompositionMode string         `json:"composition_mode,omitempty" yaml:"composition_mode,omitempty"`
	Priority        int            `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type JourneyEdge struct {
	ID        string         `json:"id" yaml:"id"`
	Source    string         `json:"source" yaml:"source"`
	Target    string         `json:"target" yaml:"target"`
	Condition string         `json:"condition,omitempty" yaml:"condition,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type Template struct {
	ID   string `json:"id" yaml:"id"`
	Mode string `json:"mode" yaml:"mode"`
	Text string `json:"text" yaml:"text"`
	When string `json:"when,omitempty" yaml:"when,omitempty"`
}

type ToolPolicy struct {
	ID       string   `json:"id" yaml:"id"`
	ToolIDs  []string `json:"tool_ids" yaml:"tool_ids"`
	Exposure string   `json:"exposure" yaml:"exposure"`
	Approval string   `json:"approval,omitempty" yaml:"approval,omitempty"`
}

type RetrieverBinding struct {
	ID         string         `json:"id" yaml:"id"`
	Kind       string         `json:"kind" yaml:"kind"`
	Scope      string         `json:"scope" yaml:"scope"`
	TargetID   string         `json:"target_id,omitempty" yaml:"target_id,omitempty"`
	Mode       string         `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxResults int            `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Config     map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

type GuidelineToolAssociation struct {
	GuidelineID string `json:"guideline_id"`
	ToolID      string `json:"tool_id"`
}
