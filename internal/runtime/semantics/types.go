package semantics

import "github.com/sahal/parmesan/internal/domain/policy"

type Signal string
type SignalFamily string
type Category string
type SlotKind string
type CoverageKind string
type GroundingSource string
type SatisfactionSource string

const (
	SignalUnknown          Signal = ""
	SignalReservation      Signal = "reservation"
	SignalReturnStatus     Signal = "return_status"
	SignalOrderStatus      Signal = "order_status"
	SignalPickup           Signal = "pickup"
	SignalDelivery         Signal = "delivery"
	SignalInsideOutside    Signal = "inside_outside"
	SignalDrinkPreference  Signal = "drink_preference"
	SignalTracking         Signal = "tracking"
	SignalScheduling       Signal = "scheduling"
	SignalConfirmation     Signal = "confirmation"
	SignalVehicle          Signal = "vehicle"
	SignalTemperature      Signal = "temperature"
	SignalSearch           Signal = "search"
	SignalStorePickup      Signal = "store_pickup"
	SignalDeliveryPickup   Signal = "delivery_pickup"
	SignalEconomy          Signal = "economy"
	SignalBusiness         Signal = "business"
	SignalPremium          Signal = "premium"
	SignalApology          Signal = "apology"
	SignalCardLocked       Signal = "card_locked"
)

const (
	CategoryUnknown      Category = ""
	CategoryVehicle      Category = "vehicle"
	CategoryTemperature  Category = "temperature"
	CategorySearch       Category = "search"
	CategoryScheduling   Category = "scheduling"
	CategoryConfirmation Category = "confirmation"
)

const (
	SlotUnknown     SlotKind = ""
	SlotDestination SlotKind = "destination"
	SlotProductLike SlotKind = "product_like"
)

const (
	GroundingSourceUnknown         GroundingSource = ""
	GroundingSourceJourneyState    GroundingSource = "journey_state"
	GroundingSourceGuideline       GroundingSource = "guideline_context"
	GroundingSourceJourneyContext  GroundingSource = "journey_context"
	GroundingSourceCustomerText    GroundingSource = "customer_text"
	SatisfactionSourceUnknown      SatisfactionSource = ""
	SatisfactionSourceCondition    SatisfactionSource = "condition"
	SatisfactionSourceEdge         SatisfactionSource = "edge_condition"
	SatisfactionSourceState        SatisfactionSource = "state_semantics"
	SatisfactionSourceCustomer     SatisfactionSource = "customer_answer"
	CoverageKindNone              CoverageKind = "none"
	CoverageKindFull              CoverageKind = "full"
	CoverageKindPartial           CoverageKind = "partial"
)

type ConditionEvidence struct {
	Condition    string   `json:"condition"`
	Applies      bool     `json:"applies"`
	Score        int      `json:"score,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
	Signal       string   `json:"signal,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
}

type CustomerDependencyEvidence struct {
	CustomerDependent bool     `json:"customer_dependent"`
	MissingData       []string `json:"missing_data,omitempty"`
	Source            string   `json:"source,omitempty"`
	Rationale         string   `json:"rationale,omitempty"`
}

type ActionCoverageEvidence struct {
	AppliedDegree string   `json:"applied_degree,omitempty"`
	Source        string   `json:"source,omitempty"`
	Rationale     string   `json:"rationale,omitempty"`
	MatchedParts  []string `json:"matched_parts,omitempty"`
}

type JourneyStateSatisfaction struct {
	StateID    string              `json:"state_id,omitempty"`
	Satisfied  bool                `json:"satisfied"`
	Source     string              `json:"source,omitempty"`
	Rationale  string              `json:"rationale,omitempty"`
	Conditions []ConditionEvidence `json:"conditions,omitempty"`
	Missing    []string            `json:"missing,omitempty"`
	LatestTurn bool                `json:"latest_turn,omitempty"`
}

type JourneyBacktrackIntent struct {
	RequiresBacktrack bool   `json:"requires_backtrack"`
	RestartFromRoot   bool   `json:"restart_from_root,omitempty"`
	Source            string `json:"source,omitempty"`
	Rationale         string `json:"rationale,omitempty"`
}

type JourneyNodeSelection struct {
	StateID    string              `json:"state_id,omitempty"`
	Score      int                 `json:"score,omitempty"`
	Rationale  string              `json:"rationale,omitempty"`
	Conditions []ConditionEvidence `json:"conditions,omitempty"`
}

type ToolGroundingEvidence struct {
	Grounded     bool     `json:"grounded"`
	Source       string   `json:"source,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
}

type ToolSelectionEvidence struct {
	Selected     bool     `json:"selected,omitempty"`
	Specialized  bool     `json:"specialized,omitempty"`
	RunInTandem  bool     `json:"run_in_tandem,omitempty"`
	ReferenceTo  string   `json:"reference_to,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
}

type TextSnapshot struct {
	Empty          bool
	HasLocation    bool
	HasDate        bool
	HasTravelClass bool
	HasName        bool
	HasEmail       bool
	HasPhone       bool
	ChoiceKind     string
}

type ArgumentExtractionResult struct {
	Value     string `json:"value,omitempty"`
	SlotKind  string `json:"slot_kind,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

type ConditionContext struct {
	Condition string
	Text      string
}

type JourneyStateContext struct {
	Text                    string
	State                   policy.JourneyNode
	EdgeCondition           string
	LatestTurn              bool
	CustomerSatisfiedAnswer func(string, policy.Guideline) bool
}

type JourneyBacktrackContext struct {
	LatestCustomerText string
}

type ActionCoverageContext struct {
	History             string
	Instruction         string
	EquivalentCheck     func([]string, string) bool
	ResponseKindSignals func(string) []string
}

type CustomerDependencyContext struct {
	Action        string
	LatestText    string
	Conversation  string
	Applied       []string
	AskedQuestion func([]string, string) bool
}

type ToolGroundingContext struct {
	LatestCustomerText string
	ActiveJourneyID    string
	ActiveStateTool    string
	ActiveStateMCPTool string
	Guidelines         []policy.Guideline
	ToolName           string
	ToolDescription    string
}

type ToolSelectionContext struct {
	CandidateID      string
	CandidateTerms   []string
	ReferenceToolIDs []string
	SelectedToolID   string
	CandidateSets    map[string][]string
}

type ArgumentExtractionContext struct {
	Field        string
	Choices      []string
	Text         string
	TextEvidence TextSnapshot
}

type ConditionEvaluator interface {
	Evaluate(ConditionContext) ConditionEvidence
}

type JourneySatisfactionEvaluator interface {
	Evaluate(JourneyStateContext) JourneyStateSatisfaction
}

type JourneyBacktrackEvaluator interface {
	Evaluate(JourneyBacktrackContext) JourneyBacktrackIntent
}

type ActionCoverageEvaluator interface {
	Evaluate(ActionCoverageContext) ActionCoverageEvidence
}

type CustomerDependencyEvaluator interface {
	Evaluate(CustomerDependencyContext) CustomerDependencyEvidence
}

type ToolGroundingEvaluator interface {
	Evaluate(ToolGroundingContext) ToolGroundingEvidence
}

type ToolSelectionEvaluator interface {
	Evaluate(ToolSelectionContext) ToolSelectionEvidence
}

type ArgumentExtractor interface {
	Extract(ArgumentExtractionContext) ArgumentExtractionResult
}
