package semantics

import (
	"strconv"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

type DefaultConditionEvaluator struct{}
type DefaultJourneySatisfactionEvaluator struct{}
type DefaultJourneyBacktrackEvaluator struct{}
type DefaultActionCoverageEvaluator struct{}
type DefaultCustomerDependencyEvaluator struct{}
type DefaultToolGroundingEvaluator struct{}
type DefaultToolSelectionEvaluator struct{}
type DefaultArgumentExtractor struct{}

func EvaluateConditionAcrossTexts(condition string, texts ...string) ConditionEvidence {
	best := ConditionEvidence{Condition: strings.TrimSpace(condition)}
	for _, text := range texts {
		candidate := EvaluateCondition(condition, text)
		if candidate.Score > best.Score {
			best = candidate
		}
	}
	if best.Condition == "" {
		best.Condition = strings.TrimSpace(condition)
	}
	return best
}

func EvaluateCondition(condition string, text string) ConditionEvidence {
	return DefaultConditionEvaluator{}.Evaluate(ConditionContext{
		Condition: condition,
		Text:      text,
	})
}

func EvaluateJourneyState(latestText string, customerHistory []string, state policy.JourneyNode, edgeCondition string, latestOnly bool, customerSatisfiedAnswer func(string, policy.Guideline) bool) JourneyStateSatisfaction {
	text := strings.TrimSpace(latestText)
	if !latestOnly {
		text = strings.TrimSpace(strings.Join(append(append([]string(nil), customerHistory...), latestText), "\n"))
	}
	return DefaultJourneySatisfactionEvaluator{}.Evaluate(JourneyStateContext{
		Text:                    text,
		State:                   state,
		EdgeCondition:           edgeCondition,
		LatestTurn:              latestOnly,
		CustomerSatisfiedAnswer: customerSatisfiedAnswer,
	})
}

func EvaluateGuidelineCustomerDependency(item policy.Guideline, conversation string, customerSatisfied bool, supportingTermsPresent bool) CustomerDependencyEvidence {
	evidence := CustomerDependencyEvidence{}
	loweredScope := strings.ToLower(strings.TrimSpace(item.Scope))
	if strings.Contains(loweredScope, "customer") {
		evidence.CustomerDependent = true
		evidence.Source = "scope"
	}
	if !evidence.CustomerDependent && GuidelineRequestsCustomerFollowUp(item.Then) {
		evidence = DefaultCustomerDependencyEvaluator{}.Evaluate(CustomerDependencyContext{
			Action:       item.Then,
			Conversation: conversation,
		})
		evidence.CustomerDependent = true
		evidence.Source = firstNonEmpty(evidence.Source, "action_semantics")
	}
	if !evidence.CustomerDependent {
		evidence.Rationale = "guideline can proceed without extra customer data"
		return evidence
	}
	if customerSatisfied {
		evidence.MissingData = nil
		evidence.Rationale = "customer already provided the required follow-up data"
		return evidence
	}
	if len(evidence.MissingData) > 0 {
		evidence.Rationale = firstNonEmpty(evidence.Rationale, "guideline depends on customer clarification before execution")
		return evidence
	}
	if !supportingTermsPresent {
		evidence.MissingData = []string{"customer_confirmation"}
		evidence.Rationale = "guideline depends on customer clarification before execution"
		return evidence
	}
	evidence.Rationale = "customer message contains the needed follow-up detail"
	return evidence
}

func EvaluateActionCoverage(
	history string,
	instruction string,
	toolSatisfied func(string, string) bool,
	equivalentCheck func([]string, string) bool,
	splitSegments func(string) []string,
	segmentSatisfied func(string, string) ([]string, bool),
	dedupeParts func([]string) []string,
) ActionCoverageEvidence {
	evidence := ActionCoverageEvidence{AppliedDegree: string(CoverageKindNone)}
	if strings.TrimSpace(history) == "" || strings.TrimSpace(instruction) == "" {
		evidence.Rationale = "missing history or instruction"
		return evidence
	}
	if toolSatisfied != nil && toolSatisfied(history, instruction) {
		evidence.AppliedDegree = string(CoverageKindFull)
		evidence.Source = "tool_event"
		evidence.Rationale = "assistant history reflects a tool-backed completion of the instruction"
		return evidence
	}
	if equivalentCheck != nil && equivalentCheck([]string{history}, instruction) {
		evidence.AppliedDegree = string(CoverageKindFull)
		evidence.Source = "assistant_message"
		evidence.Rationale = "assistant history already contains the full instruction coverage"
		return evidence
	}
	if splitSegments == nil || segmentSatisfied == nil {
		return evidence
	}
	segments := splitSegments(instruction)
	if len(segments) == 0 {
		if matched, ok := segmentSatisfied(history, instruction); ok {
			evidence.AppliedDegree = string(CoverageKindFull)
			evidence.Source = "assistant_message"
			evidence.MatchedParts = matched
			evidence.Rationale = "assistant history covers the instruction"
		}
		return evidence
	}
	matched := 0
	var matchedParts []string
	for _, segment := range segments {
		if parts, ok := segmentSatisfied(history, segment); ok {
			matched++
			matchedParts = append(matchedParts, parts...)
		}
	}
	if dedupeParts != nil {
		matchedParts = dedupeParts(matchedParts)
	}
	switch {
	case matched == 0:
		evidence.Rationale = "assistant history does not cover the instruction yet"
	case matched == len(segments):
		evidence.AppliedDegree = string(CoverageKindFull)
		evidence.Source = "assistant_message"
		evidence.MatchedParts = matchedParts
		evidence.Rationale = "assistant history covers all instruction segments"
	default:
		evidence.AppliedDegree = string(CoverageKindPartial)
		evidence.Source = "assistant_message"
		evidence.MatchedParts = matchedParts
		evidence.Rationale = "assistant history covers only part of the instruction"
	}
	return evidence
}

func (DefaultConditionEvaluator) Evaluate(ctx ConditionContext) ConditionEvidence {
	condition := strings.TrimSpace(ctx.Condition)
	text := strings.TrimSpace(ctx.Text)
	evidence := ConditionEvidence{Condition: condition}
	if condition == "" || text == "" {
		evidence.Rationale = "missing condition or text"
		return evidence
	}
	loweredCondition := strings.ToLower(condition)
	loweredText := strings.ToLower(text)
	if score := reservationConditionScore(loweredCondition, loweredText); score != 0 {
		evidence.Applies = score > 0
		evidence.Score = score
		evidence.Signal = "reservation_fact"
		if evidence.Applies {
			evidence.Rationale = "reservation intent is explicitly present"
		} else {
			evidence.Rationale = "reservation-specific condition is contradicted by the latest text"
		}
		return evidence
	}
	if score := ageConditionScore(loweredCondition, loweredText); score != 0 {
		evidence.Applies = score > 0
		evidence.Score = score
		evidence.Signal = "age_fact"
		if evidence.Applies {
			evidence.Rationale = "age-specific condition is satisfied by an extracted age value"
		} else {
			evidence.Rationale = "age-specific condition is contradicted by an extracted age value"
		}
		return evidence
	}
	conditionWords := Signals(condition)
	textWords := SignalSet(Signals(text))
	score := 0
	var matched []string
	for _, token := range conditionWords {
		if _, ok := textWords[token]; ok {
			score++
			matched = append(matched, token)
		}
	}
	evidence.Score = score
	evidence.Applies = score > 0
	evidence.MatchedTerms = dedupeStrings(matched)
	evidence.Signal = "semantic_overlap"
	if evidence.Applies {
		evidence.Rationale = "condition and text share the required semantic signals"
	} else {
		evidence.Rationale = "condition has no supporting semantic signal overlap"
	}
	return evidence
}

func (DefaultJourneySatisfactionEvaluator) Evaluate(ctx JourneyStateContext) JourneyStateSatisfaction {
	result := JourneyStateSatisfaction{StateID: ctx.State.ID, LatestTurn: ctx.LatestTurn}
	if strings.EqualFold(ctx.State.Type, "tool") {
		result.Rationale = "tool nodes are satisfied only by executed tool events"
		result.Missing = []string{"tool_execution"}
		return result
	}
	text := strings.TrimSpace(ctx.Text)
	if text == "" {
		result.Rationale = "no customer text is available to satisfy the journey state"
		result.Missing = []string{"customer_input"}
		return result
	}
	condEval := DefaultConditionEvaluator{}
	for _, condition := range ctx.State.When {
		evidence := condEval.Evaluate(ConditionContext{Condition: condition, Text: text})
		result.Conditions = append(result.Conditions, evidence)
		if evidence.Applies {
			result.Satisfied = true
			result.Source = string(SatisfactionSourceCondition)
			result.Rationale = firstNonEmpty(evidence.Rationale, "state entry condition is satisfied")
			return result
		}
	}
	if strings.TrimSpace(ctx.EdgeCondition) != "" {
		evidence := condEval.Evaluate(ConditionContext{Condition: ctx.EdgeCondition, Text: text})
		result.Conditions = append(result.Conditions, evidence)
		if evidence.Applies {
			result.Satisfied = true
			result.Source = string(SatisfactionSourceEdge)
			result.Rationale = firstNonEmpty(evidence.Rationale, "incoming edge condition is satisfied")
			return result
		}
	}
	if JourneyStateSemanticSatisfied(text, ctx.State) {
		result.Satisfied = true
		result.Source = string(SatisfactionSourceState)
		result.Rationale = "state-specific semantic evidence is satisfied by the conversation text"
		return result
	}
	if ctx.CustomerSatisfiedAnswer != nil {
		pseudo := policy.Guideline{Then: ctx.State.Instruction, Scope: "customer"}
		if ctx.CustomerSatisfiedAnswer(text, pseudo) {
			result.Satisfied = true
			result.Source = string(SatisfactionSourceCustomer)
			result.Rationale = "the customer answer satisfies the state instruction"
			return result
		}
	}
	result.Missing = []string{"state_input"}
	result.Rationale = "state-specific evidence is still missing"
	return result
}

func (DefaultJourneyBacktrackEvaluator) Evaluate(ctx JourneyBacktrackContext) JourneyBacktrackIntent {
	text := strings.ToLower(strings.TrimSpace(ctx.LatestCustomerText))
	if text == "" {
		return JourneyBacktrackIntent{}
	}
	signals := SignalSet(Signals(text))
	sameProcess := strings.Contains(text, "go back") || hasAnySignal(signals, "actually", "change", "changed", "resume", "continue")
	restart := strings.Contains(text, "start over") || hasAnySignal(signals, "instead", "different", "another", "restart", "again", "new")
	if sameProcess {
		return JourneyBacktrackIntent{RequiresBacktrack: true, Source: "same_process", Rationale: "customer changed a prior decision within the same process"}
	}
	if restart {
		return JourneyBacktrackIntent{RequiresBacktrack: true, RestartFromRoot: true, Source: "restart", Rationale: "customer wants to restart or run the journey for a new purpose"}
	}
	return JourneyBacktrackIntent{}
}

func (DefaultActionCoverageEvaluator) Evaluate(ctx ActionCoverageContext) ActionCoverageEvidence {
	evidence := ActionCoverageEvidence{AppliedDegree: string(CoverageKindNone)}
	history := strings.TrimSpace(ctx.History)
	instruction := strings.TrimSpace(ctx.Instruction)
	if history == "" || instruction == "" {
		evidence.Rationale = "missing history or instruction"
		return evidence
	}
	if ctx.EquivalentCheck != nil && ctx.EquivalentCheck([]string{history}, instruction) {
		evidence.AppliedDegree = string(CoverageKindFull)
		evidence.Source = "assistant_message"
		evidence.Rationale = "instruction-equivalent response already appears in assistant history"
		return evidence
	}
	required := Signals(instruction)
	present := SignalSet(Signals(history))
	var matched []string
	for _, signal := range required {
		if _, ok := present[signal]; ok {
			matched = append(matched, signal)
		}
	}
	evidence.MatchedParts = dedupeStrings(matched)
	switch {
	case len(matched) == 0:
		evidence.Rationale = "assistant history does not yet cover the instruction signals"
	case len(matched) == len(required):
		evidence.AppliedDegree = string(CoverageKindFull)
		evidence.Source = "assistant_message"
		evidence.Rationale = "assistant history covers the full instruction signal set"
	default:
		evidence.AppliedDegree = string(CoverageKindPartial)
		evidence.Source = "assistant_message"
		evidence.Rationale = "assistant history covers only part of the instruction signal set"
	}
	return evidence
}

func (DefaultCustomerDependencyEvaluator) Evaluate(ctx CustomerDependencyContext) CustomerDependencyEvidence {
	evidence := CustomerDependencyEvidence{}
	action := strings.TrimSpace(strings.ToLower(ctx.Action))
	if action == "" {
		return evidence
	}
	signals := SignalSet(Signals(action))
	switch {
	case hasAnySignal(signals, "email"):
		evidence.CustomerDependent = true
		if !strings.Contains(strings.ToLower(ctx.Conversation), "@") {
			evidence.MissingData = []string{"email"}
		}
	case strings.Contains(action, "phone"):
		evidence.CustomerDependent = true
		if !hasPhoneLikeValue(ctx.Conversation) {
			evidence.MissingData = []string{"phone"}
		}
	}
	if evidence.CustomerDependent {
		evidence.Source = "action_semantics"
		if len(evidence.MissingData) > 0 {
			evidence.Rationale = "the guideline still requires customer-provided clarification"
		} else {
			evidence.Rationale = "the customer has already provided the required clarification"
		}
	}
	return evidence
}

func (DefaultToolGroundingEvaluator) Evaluate(ctx ToolGroundingContext) ToolGroundingEvidence {
	text := strings.ToLower(strings.TrimSpace(ctx.LatestCustomerText))
	if text == "" {
		return ToolGroundingEvidence{Grounded: false, Rationale: "latest customer text is empty so no grounding evidence is available"}
	}
	if strings.EqualFold(strings.TrimSpace(ctx.ActiveStateTool), ctx.ToolName) || strings.EqualFold(strings.TrimSpace(ctx.ActiveStateMCPTool), ctx.ToolName) {
		return ToolGroundingEvidence{Grounded: true, Source: string(GroundingSourceJourneyState), Rationale: "active journey state explicitly requires this tool"}
	}
	needleTerms := dedupeStrings(append(Signals(ctx.ToolName), Signals(ctx.ToolDescription)...))
	for _, guideline := range ctx.Guidelines {
		guidelineText := strings.TrimSpace(guideline.When + " " + guideline.Then)
		matched := matchedTerms(guidelineText, needleTerms)
		if len(matched) > 0 {
			return ToolGroundingEvidence{Grounded: true, Source: string(GroundingSourceGuideline), MatchedTerms: matched, Rationale: "matched guideline context explicitly points at this tool"}
		}
	}
	if ctx.ActiveJourneyID != "" && len(matchedTerms(ctx.ActiveJourneyID, needleTerms)) > 0 {
		return ToolGroundingEvidence{Grounded: true, Source: string(GroundingSourceJourneyContext), MatchedTerms: matchedTerms(ctx.ActiveJourneyID, needleTerms), Rationale: "the active journey context grounds this tool"}
	}
	matched := matchedTerms(text, needleTerms)
	if len(matched) > 0 {
		return ToolGroundingEvidence{Grounded: true, Source: string(GroundingSourceCustomerText), MatchedTerms: matched, Rationale: "the latest customer text explicitly references this tool's domain"}
	}
	return ToolGroundingEvidence{Grounded: false, Rationale: "no explicit grounding evidence was found for this tool"}
}

func (DefaultToolSelectionEvaluator) Evaluate(ctx ToolSelectionContext) ToolSelectionEvidence {
	if len(ctx.ReferenceToolIDs) == 0 {
		return ToolSelectionEvidence{}
	}
	candidateTerms := ctx.CandidateTerms
	for _, ref := range ctx.ReferenceToolIDs {
		refTerms := ctx.CandidateSets[ref]
		switch {
		case semanticSubset(candidateTerms, refTerms) || semanticSpecialization(candidateTerms, refTerms):
			return ToolSelectionEvidence{Specialized: true, ReferenceTo: ref, MatchedTerms: candidateTerms, Rationale: "candidate tool is more specialized for this use case than the reference tool"}
		case shouldRunInTandem(ctx.CandidateID, ctx.SelectedToolID, ctx.CandidateSets):
			return ToolSelectionEvidence{RunInTandem: true, ReferenceTo: firstNonEmpty(ctx.SelectedToolID, ref), Rationale: "candidate should still run in tandem with the better reference tool"}
		}
	}
	return ToolSelectionEvidence{}
}

func ToolSelectionContextFromIDs(candidateID string, referenceToolIDs []string, selectedToolID string, candidateIDs []string) ToolSelectionContext {
	candidateID = strings.TrimSpace(candidateID)
	ctx := ToolSelectionContext{
		CandidateID:      candidateID,
		ReferenceToolIDs: append([]string(nil), referenceToolIDs...),
		SelectedToolID:   strings.TrimSpace(selectedToolID),
		CandidateSets:    map[string][]string{},
	}
	if candidateID != "" {
		ctx.CandidateTerms = Signals(strings.ToLower(strings.ReplaceAll(candidateID, "_", " ")))
		ctx.CandidateSets[candidateID] = ctx.CandidateTerms
	}
	for _, id := range candidateIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := ctx.CandidateSets[id]; ok {
			continue
		}
		ctx.CandidateSets[id] = Signals(strings.ToLower(strings.ReplaceAll(id, "_", " ")))
	}
	return ctx
}

func (DefaultArgumentExtractor) Extract(ctx ArgumentExtractionContext) ArgumentExtractionResult {
	field := strings.ToLower(strings.TrimSpace(ctx.Field))
	text := strings.ToLower(strings.TrimSpace(ctx.Text))
	if field == "" || text == "" {
		return ArgumentExtractionResult{}
	}
	if len(ctx.Choices) > 0 {
		for _, choice := range ctx.Choices {
			if strings.Contains(text, strings.ToLower(choice)) {
				return ArgumentExtractionResult{Value: choice, Rationale: "customer text directly contains a valid choice"}
			}
		}
	}
	kind := SlotKindForField(field)
	switch field {
	case "vendor", "brand", "manufacturer":
		if value := inferredBrand(text); value != "" {
			return ArgumentExtractionResult{Value: value, SlotKind: string(kind), Rationale: "brand was inferred from semantic signals"}
		}
	case "keyword":
		if value := inferredKeyword(text); value != "" {
			return ArgumentExtractionResult{Value: value, SlotKind: string(kind), Rationale: "keyword was inferred from semantic signals"}
		}
	case "date":
		if marker := RelativeDateTerm(text); marker != "" && marker != "return in" {
			return ArgumentExtractionResult{Value: marker, SlotKind: string(kind), Rationale: "relative date marker was extracted from the customer text"}
		}
		if ctx.TextEvidence.HasDate {
			return ArgumentExtractionResult{Value: text, SlotKind: string(kind), Rationale: "date-like value is present in the customer text"}
		}
	}
	if value := extractArgumentEntity(text, kind); value != "" {
		return ArgumentExtractionResult{Value: value, SlotKind: string(kind), Rationale: "value was extracted from the typed slot extractor"}
	}
	return ArgumentExtractionResult{}
}

func AnalyzeText(text string) TextSnapshot {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return TextSnapshot{Empty: true}
	}
	signals := SignalSet(Signals(lowered))
	snapshot := TextSnapshot{
		HasLocation:    hasLocationLikeValue(lowered),
		HasDate:        hasDateLikeValue(lowered),
		HasTravelClass: hasAnySignal(signals, SignalEconomy, SignalBusiness, SignalPremium),
		HasName:        containsNamePattern(lowered),
		HasEmail:       strings.Contains(lowered, "@"),
		HasPhone:       hasPhoneLikeValue(lowered),
	}
	switch {
	case hasAnySignal(signals, SignalInsideOutside):
		snapshot.ChoiceKind = string(SignalInsideOutside)
	case hasAnySignal(signals, SignalDelivery, SignalPickup):
		snapshot.ChoiceKind = string(SignalDeliveryPickup)
	case hasAnySignal(signals, SignalDrinkPreference):
		snapshot.ChoiceKind = string(SignalDrinkPreference)
	case hasAnySignal(signals, SignalPickup, "store"):
		snapshot.ChoiceKind = string(SignalStorePickup)
	}
	return snapshot
}

func InstructionResponseKind(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	if strings.Contains(action, "address") || strings.Contains(action, "time") {
		return ""
	}
	signals := SignalSet(Signals(action))
	switch {
	case hasAnySignal(signals, SignalInsideOutside):
		return string(SignalInsideOutside)
	case hasAnySignal(signals, SignalDelivery, SignalPickup):
		return string(SignalDeliveryPickup)
	case hasAnySignal(signals, SignalDrinkPreference):
		return string(SignalDrinkPreference)
	case hasAnySignal(signals, SignalPickup, "store"):
		return string(SignalStorePickup)
	default:
		return ""
	}
}

func GuidelineRequestsCustomerFollowUp(action string) bool {
	action = strings.TrimSpace(strings.ToLower(action))
	if action == "" {
		return false
	}
	if InstructionResponseKind(action) != "" && actionRequestsCustomerInput(action) {
		return true
	}
	actionTokens := SignalSet(Signals(action))
	for _, token := range []string{"ask", "clarify", "reason", "details", "status"} {
		if _, ok := actionTokens[token]; ok {
			return true
		}
	}
	return false
}

func actionRequestsCustomerInput(action string) bool {
	switch {
	case strings.Contains(action, "?"):
		return true
	case containsAnyPhrase(action, "ask", "confirm", "clarify", "which", "whether", "let me know", "tell me", "please provide", "provide your", "provide the", "share", "choose", "pick"):
		return true
	default:
		return false
	}
}

func InstructionCoverageSignals(instruction string) []string {
	lowered := strings.ToLower(strings.TrimSpace(instruction))
	if lowered == "" {
		return nil
	}
	base := SignalSet(Signals(lowered))
	var signals []string
	if strings.Contains(lowered, "apolog") || hasAnySignal(base, SignalApology) {
		signals = append(signals, "apology")
	}
	if _, ok := base["discount"]; ok {
		signals = append(signals, "discount")
	}
	if _, ok := base[string(SignalReturnStatus)]; ok {
		signals = append(signals, string(SignalReturnStatus))
	}
	if _, ok := base[string(SignalOrderStatus)]; ok {
		signals = append(signals, string(SignalOrderStatus))
	}
	if strings.Contains(lowered, "lock the card") || hasAnySignal(base, SignalCardLocked) {
		signals = append(signals, "card_locked")
	}
	return dedupeStrings(signals)
}

func HistoryCoverageSignals(history string) []string {
	lowered := strings.ToLower(strings.TrimSpace(history))
	if lowered == "" {
		return nil
	}
	base := SignalSet(Signals(lowered))
	var signals []string
	if strings.Contains(lowered, "apolog") || DefaultSignalRegistry.HasPhraseFamily(lowered, SignalApology) || hasAnySignal(base, SignalApology) {
		signals = append(signals, "apology")
	}
	if _, ok := base["discount"]; ok {
		signals = append(signals, "discount")
	}
	switch statusSignal(lowered, base) {
	case SignalReturnStatus:
		signals = append(signals, string(SignalReturnStatus))
	case SignalOrderStatus:
		signals = append(signals, string(SignalOrderStatus))
	}
	if DefaultSignalRegistry.HasPhraseFamily(lowered, SignalCardLocked) {
		signals = append(signals, "card_locked")
	}
	return dedupeStrings(signals)
}

func ToolHistoryCoverageSignals(history string) []string {
	lowered := strings.ToLower(strings.TrimSpace(history))
	if lowered == "" {
		return nil
	}
	base := SignalSet(Signals(lowered))
	var signals []string
	switch statusSignal(lowered, base) {
	case SignalReturnStatus:
		signals = append(signals, string(SignalReturnStatus))
	case SignalOrderStatus:
		signals = append(signals, string(SignalOrderStatus))
	}
	if DefaultSignalRegistry.HasPhraseFamily(lowered, SignalCardLocked) {
		signals = append(signals, "card_locked")
	}
	return dedupeStrings(signals)
}

func ResponseTextSatisfiesInstruction(text string, sample string) bool {
	required := Signals(sample)
	if len(required) == 0 {
		return true
	}
	present := SignalSet(Signals(text))
	for _, token := range required {
		if _, ok := present[token]; !ok {
			return false
		}
	}
	return true
}

func JourneyStateSemanticSatisfied(text string, state policy.JourneyNode) bool {
	textEvidence := AnalyzeText(text)
	if textEvidence.Empty {
		return false
	}
	label := strings.ToLower(strings.TrimSpace(state.ID + " " + state.Instruction))
	switch {
	case strings.Contains(label, "destination"), strings.Contains(label, "origin"), strings.Contains(label, "departure"):
		return textEvidence.HasLocation
	case strings.Contains(label, "date"), strings.Contains(label, "travel"):
		return textEvidence.HasDate
	case strings.Contains(label, "class"):
		return textEvidence.HasTravelClass
	case strings.Contains(label, "name"):
		return textEvidence.HasName
	default:
		return false
	}
}

func matchedTerms(text string, terms []string) []string {
	if strings.TrimSpace(text) == "" || len(terms) == 0 {
		return nil
	}
	textTerms := SignalSet(Signals(text))
	var matched []string
	for _, term := range terms {
		if _, ok := textTerms[term]; ok {
			matched = append(matched, term)
		}
	}
	return dedupeStrings(matched)
}

func semanticSubset(left []string, right []string) bool {
	if len(left) == 0 || len(right) == 0 || len(left) >= len(right) {
		return false
	}
	rightSet := SignalSet(right)
	matched := 0
	for _, item := range left {
		if _, ok := rightSet[item]; ok {
			matched++
		}
	}
	return matched == len(left)
}

func semanticSpecialization(left []string, right []string) bool {
	leftCats := Categories(left)
	rightCats := Categories(right)
	if len(leftCats) == 0 || len(rightCats) == 0 {
		return false
	}
	for category := range leftCats {
		if _, ok := rightCats[category]; ok && !termSetsEqual(left, right) {
			return true
		}
	}
	return false
}

func SemanticSpecialization(left []string, right []string) bool {
	return semanticSpecialization(left, right)
}

func shouldRunInTandem(candidate string, selected string, candidateSets map[string][]string) bool {
	selectedTerms := candidateSets[selected]
	candidateTerms := candidateSets[candidate]
	candidateCats := Categories(candidateTerms)
	selectedCats := Categories(selectedTerms)
	_, candidateIsConfirmation := candidateCats[string(CategoryConfirmation)]
	_, selectedIsScheduling := selectedCats[string(CategoryScheduling)]
	return candidateIsConfirmation && selectedIsScheduling
}

func extractArgumentEntity(text string, kind SlotKind) string {
	extractor, ok := SlotExtractorForKind(kind)
	if !ok {
		return ""
	}
	return extractEntityAfterMarkers(text, extractor.Markers, extractor.StopTokens)
}

func extractEntityAfterMarkers(text string, markers []string, stopTokens []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(markers) == 0 {
		return ""
	}
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		idx := strings.Index(text, marker+" ")
		if idx < 0 {
			continue
		}
		remainder := strings.TrimLeft(strings.TrimSpace(text[idx+len(marker):]), " ")
		if value := trimArgumentEntity(remainder, stopTokens); value != "" {
			return value
		}
	}
	return ""
}

func trimArgumentEntity(text string, stopTokens []string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	var kept []string
stopLoop:
	for i, part := range parts {
		token := strings.Trim(strings.ToLower(part), ".,!?;:\"'()[]{}")
		for _, stop := range stopTokens {
			if token == stop {
				break stopLoop
			}
		}
		if i == 0 && (token == "a" || token == "an" || token == "the") {
			continue
		}
		kept = append(kept, strings.Trim(part, ".,!?;:\"'()[]{}"))
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

func reservationConditionScore(condition, text string) int {
	if !DefaultSignalRegistry.HasPhraseFamily(condition, SignalReservation) {
		return 0
	}
	if containsAnyPhrase(text, "book a table", "book a table for", "reserve a table", "make a reservation", "book a reservation") {
		return 3
	}
	return 0
}

func ageConditionScore(condition, text string) int {
	age, ok := extractMentionedAge(text)
	if !ok {
		return 0
	}
	switch {
	case containsAnyPhrase(condition, "under 21", "younger than 21", "below 21"):
		if age < 21 {
			return 3
		}
		return -3
	case containsAnyPhrase(condition, "21 or older", "over 21", "21 and older", "at least 21"):
		if age >= 21 {
			return 3
		}
		return -3
	default:
		return 0
	}
}

func extractMentionedAge(text string) (int, bool) {
	for _, token := range strings.Fields(text) {
		value, err := strconv.Atoi(strings.Trim(token, ".,!?;:\"'()[]{}"))
		if err == nil && value > 0 && value < 130 {
			return value, true
		}
	}
	return 0, false
}

func containsAnyPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func containsNamePattern(text string) bool {
	return containsAnyPhrase(text, "my name is", "i am ", "i'm ")
}

func hasLocationLikeValue(text string) bool {
	if containsAnyPhrase(text, "airport", "station", "terminal") {
		return true
	}
	return len(strings.Fields(text)) >= 2
}

func hasPhoneLikeValue(text string) bool {
	for _, token := range strings.Fields(text) {
		digits := 0
		for _, r := range token {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= 5 {
			return true
		}
	}
	return false
}

func hasDateLikeValue(text string) bool {
	for _, token := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' || r == ';' }) {
		token = strings.Trim(token, ".!?()[]{}")
		parts := strings.FieldsFunc(token, func(r rune) bool { return r == '.' || r == '/' || r == '-' })
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		allNumeric := true
		for _, part := range parts {
			if part == "" {
				allNumeric = false
				break
			}
			for _, r := range part {
				if r < '0' || r > '9' {
					allNumeric = false
					break
				}
			}
			if !allNumeric {
				break
			}
		}
		if allNumeric {
			return true
		}
	}
	return RelativeDateTerm(text) != ""
}

func inferredBrand(text string) string {
	for _, signal := range Signals(text) {
		switch signal {
		case "dell", "samsung", "apple", "lenovo", "hp", "asus":
			return strings.Title(signal)
		}
	}
	return ""
}

func inferredKeyword(text string) string {
	for _, signal := range Signals(text) {
		switch signal {
		case "laptop", "ssd", "phone", "tablet":
			return strings.ToUpper(signal[:1]) + signal[1:]
		}
	}
	return ""
}

func termSetsEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := SignalSet(left)
	for _, item := range right {
		if _, ok := leftSet[item]; !ok {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
