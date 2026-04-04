package policyruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
)

func Resolve(events []session.Event, bundles []policy.Bundle, journeyInstances []journey.Instance, catalog []tool.CatalogEntry) (ResolvedView, error) {
	return ResolveWithRouter(context.Background(), nil, events, bundles, journeyInstances, catalog)
}

func ResolveWithRouter(ctx context.Context, router *model.Router, events []session.Event, bundles []policy.Bundle, journeyInstances []journey.Instance, catalog []tool.CatalogEntry) (ResolvedView, error) {
	if len(bundles) == 0 {
		return ResolvedView{}, nil
	}
	bundle := bundles[0]
	matchCtx := buildMatchingContext(events)
	resolver := newStrategyResolver(genericStrategy{})
	resolver.Register(customStrategy{})
	matcher := newGuidelineMatcher(resolver)
	state, err := matcher.Run(ctx, router, bundle, matchCtx, journeyInstances, catalog)
	if err != nil {
		return ResolvedView{}, err
	}
	// Journey-node projected guidelines only become active once the active state is known.
	if state.activeJourney != nil && state.activeJourneyState != nil {
		projected := projectedNodeGuideline(*state.activeJourney, *state.activeJourneyState)
		if strategy := resolver.Resolve(projected); strategy != nil {
			appendProjectedGuideline(ctx, state, strategy, projected)
			state.matchedGuidelines = matchedGuidelinesFromMatches(state.bundle, state.guidelineMatches, state.activeJourney)
			state.customerDecisions, state.matchedGuidelines = runCustomerDependentARQ(state.context, state.matchedGuidelines)
			state.reapplyDecisions, state.matchedGuidelines = runPreviouslyAppliedARQ(state.context, state.matchedGuidelines, state.guidelineMatches)
			resolved := resolveRelationships(state.bundle, state.context, state.matchedObservations, state.guidelineMatches, state.matchedGuidelines, state.activeJourney, state.activeJourneyState)
			state.matchedGuidelines = resolved.guidelines
			state.suppressedGuidelines = resolved.suppressed
			state.disambiguationPrompt = resolved.disambiguation
			state.resolutionRecords = resolved.resolutions
			state.activeJourney = resolved.activeJourney
			if state.activeJourney == nil {
				state.activeJourneyState = nil
			}
			state.disambiguationPrompt = runDisambiguationARQ(ctx, state.router, state.context, state.matchedGuidelines, state.disambiguationPrompt)
			state.candidateTemplates = collectTemplates(state.bundle, state.activeJourney, state.activeJourneyState, state.context)
			state.responseAnalysis = analyzeResponsePlan(ctx, state.router, state.context, responseAnalysisGuidelines(state.bundle, state.context, state.matchedGuidelines), state.candidateTemplates, modeOrDefault(state.bundle.CompositionMode, state.candidateTemplates), state.bundle.NoMatch)
		}
	}
	state.exposedTools, state.toolApprovals = resolveToolExposure(bundle.GuidelineToolAssociations, state.matchedObservations, state.matchedGuidelines, state.activeJourneyState, bundle.ToolPolicies, catalog)
	state.toolPlan, state.toolDecision = buildToolPlan(ctx, router, matchCtx, state.activeJourney, state.activeJourneyState, state.matchedGuidelines, state.exposedTools, state.toolApprovals, bundle.Relationships, catalog)

	mode := strings.ToLower(strings.TrimSpace(bundle.CompositionMode))
	if mode == "" {
		mode = inferCompositionMode(state.candidateTemplates)
	}
	if state.responseAnalysis.NeedsStrictMode {
		mode = "strict"
	}

	arqs := arqsFromState(state)
	arqs = append(arqs, ARQResult{Name: "tool_plan", Version: promptVersion("tool_plan"), Output: map[string]any{
		"candidates":         state.toolPlan.Candidates,
		"selected_tool":      state.toolPlan.SelectedTool,
		"selected_tools":     state.toolPlan.SelectedTools,
		"overlapping_groups": state.toolPlan.OverlappingGroups,
		"rationale":          state.toolPlan.Rationale,
	}})
	arqs = append(arqs, ARQResult{Name: "tool_decision", Version: promptVersion("tool_decision"), Output: map[string]any{
		"selected_tool":     state.toolDecision.SelectedTool,
		"arguments":         state.toolDecision.Arguments,
		"approval_required": state.toolDecision.ApprovalRequired,
		"can_run":           state.toolDecision.CanRun,
		"missing_arguments": state.toolDecision.MissingArguments,
		"invalid_arguments": state.toolDecision.InvalidArguments,
		"missing_issues":    state.toolDecision.MissingIssues,
		"invalid_issues":    state.toolDecision.InvalidIssues,
		"grounded":          state.toolDecision.Grounded,
		"rationale":         state.toolDecision.Rationale,
	}})
	state.batchResults = append(state.batchResults, BatchResult{
		Name:          "tool_plan",
		Strategy:      "generic",
		PromptVersion: promptVersion("tool_plan"),
		Output:        arqs[len(arqs)-2].Output,
	}, BatchResult{
		Name:          "tool_decision",
		Strategy:      "generic",
		PromptVersion: promptVersion("tool_decision"),
		Output:        arqs[len(arqs)-1].Output,
	})
	state.promptSetVersions["tool_plan"] = promptVersion("tool_plan")
	state.promptSetVersions["tool_decision"] = promptVersion("tool_decision")

	return ResolvedView{
		Bundle:               &bundle,
		Context:              matchCtx,
		Attention:            state.attention,
		ObservationMatches:   state.observationMatches,
		GuidelineMatches:     state.guidelineMatches,
		ReapplyDecisions:     state.reapplyDecisions,
		CustomerDecisions:    state.customerDecisions,
		MatchedObservations:  state.matchedObservations,
		MatchedGuidelines:    state.matchedGuidelines,
		SuppressedGuidelines: state.suppressedGuidelines,
		ActiveJourney:        state.activeJourney,
		ActiveJourneyState:   state.activeJourneyState,
		JourneyInstance:      state.journeyInstance,
		ProjectedNodes:       state.projectedNodes,
		JourneyDecision:      state.journeyDecision,
		ResolutionRecords:    state.resolutionRecords,
		ExposedTools:         state.exposedTools,
		ToolApprovals:        state.toolApprovals,
		ToolPlan:             state.toolPlan,
		ToolDecision:         state.toolDecision,
		ResponseAnalysis:     state.responseAnalysis,
		CandidateTemplates:   state.candidateTemplates,
		CompositionMode:      mode,
		NoMatch:              bundle.NoMatch,
		DisambiguationPrompt: state.disambiguationPrompt,
		BatchResults:         append([]BatchResult(nil), state.batchResults...),
		PromptSetVersions:    clonePromptVersions(state.promptSetVersions),
		ARQResults:           arqs,
	}, nil
}

func VerifyDraft(view ResolvedView, draft string, toolOutput map[string]any) VerificationResult {
	if strings.TrimSpace(view.DisambiguationPrompt) != "" {
		return VerificationResult{
			Status:      "block",
			Reasons:     []string{"disambiguation_required"},
			Replacement: view.DisambiguationPrompt,
		}
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		rendered := renderTemplate(view.CandidateTemplates, toolOutput)
		if rendered != "" && normalizeText(draft) != normalizeText(rendered) {
			return VerificationResult{
				Status:      "revise",
				Reasons:     []string{"strict_template_required"},
				Replacement: rendered,
			}
		}
		if rendered == "" {
			if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
				expected := strings.TrimSpace(view.ActiveJourneyState.Instruction)
				if normalizeText(draft) != normalizeText(expected) {
					return VerificationResult{
						Status:      "revise",
						Reasons:     []string{"strict_journey_instruction_required"},
						Replacement: expected,
					}
				}
				return VerificationResult{Status: "pass"}
			}
			return VerificationResult{
				Status:      "block",
				Reasons:     []string{"strict_no_template"},
				Replacement: strictNoMatchText(view.NoMatch),
			}
		}
	}
	for _, item := range view.ResponseAnalysis.AnalyzedGuidelines {
		if !item.RequiresTemplate {
			continue
		}
		if strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate) != "" && normalizeText(draft) != normalizeText(view.ResponseAnalysis.RecommendedTemplate) {
			return VerificationResult{
				Status:      "revise",
				Reasons:     []string{"response_analysis_template_required"},
				Replacement: view.ResponseAnalysis.RecommendedTemplate,
			}
		}
		if strings.EqualFold(view.CompositionMode, "strict") && strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate) == "" && view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
			expected := strings.TrimSpace(view.ActiveJourneyState.Instruction)
			if normalizeText(draft) != normalizeText(expected) {
				return VerificationResult{
					Status:      "revise",
					Reasons:     []string{"response_analysis_journey_instruction_required"},
					Replacement: expected,
				}
			}
		}
		break
	}
	if view.ResponseAnalysis.NeedsStrictMode && !strings.EqualFold(view.CompositionMode, "strict") {
		return VerificationResult{
			Status:  "revise",
			Reasons: []string{"response_analysis_requires_strict_mode"},
		}
	}
	if view.ResponseAnalysis.RecommendedTemplate != "" && normalizeText(draft) != normalizeText(view.ResponseAnalysis.RecommendedTemplate) {
		return VerificationResult{
			Status:      "revise",
			Reasons:     []string{"response_analysis_template_mismatch"},
			Replacement: view.ResponseAnalysis.RecommendedTemplate,
		}
	}
	if view.ToolDecision.SelectedTool != "" && len(toolOutput) == 0 && strings.Contains(strings.ToLower(view.ToolDecision.Rationale), "required") {
		return VerificationResult{
			Status:  "revise",
			Reasons: []string{"required_tool_missing"},
		}
	}
	for _, item := range view.ReapplyDecisions {
		if !item.ShouldReapply {
			continue
		}
		for _, guideline := range view.MatchedGuidelines {
			if guideline.ID == item.ID && strings.TrimSpace(guideline.Then) != "" && !containsAllKeywords(draft, guideline.Then) {
				return VerificationResult{
					Status:  "revise",
					Reasons: []string{"reapplied_guideline_missing"},
				}
			}
		}
	}
	return VerificationResult{Status: "pass"}
}

func strictNoMatchText(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}
	return "Not sure I understand. Could you please say that another way?"
}

func arqsFromState(state *matchingState) []ARQResult {
	results := []ARQResult{
		{Name: "policy_attention", Version: promptVersion("policy_attention"), Output: map[string]any{
			"critical_instruction_ids": state.attention.CriticalInstructionIDs,
			"context_signals":          state.attention.ContextSignals,
			"missing_information":      state.attention.MissingInformation,
		}},
	}
	for _, batch := range state.batchResults {
		results = append(results, ARQResult{
			Name:    batch.Name,
			Version: batch.PromptVersion,
			Output:  batch.Output,
		})
	}
	return results
}

func appendProjectedGuideline(ctx context.Context, state *matchingState, strategy guidelineMatchingStrategy, guideline policy.Guideline) {
	previousMatches := append([]Match(nil), state.guidelineMatches...)
	previousGuidelines := append([]policy.Guideline(nil), state.matchedGuidelines...)
	for _, batch := range strategy.CreateMatchingBatches(state, []policy.Guideline{guideline}) {
		switch batch.Name() {
		case "actionable_match", "low_criticality_match", "customer_dependency", "previously_applied", "relationship_resolution", "disambiguation":
			_ = batch.Process(ctx, state)
			if batch.Name() == "actionable_match" || batch.Name() == "low_criticality_match" {
				state.guidelineMatches = mergeProjectedMatches(previousMatches, state.guidelineMatches)
				state.matchedGuidelines = mergeProjectedGuidelines(previousGuidelines, state.matchedGuidelines, state.guidelineMatches)
				previousMatches = append([]Match(nil), state.guidelineMatches...)
				previousGuidelines = append([]policy.Guideline(nil), state.matchedGuidelines...)
			}
			recordBatchResult(state, batch.Name(), batch.Strategy(), batch.PromptVersion(), 0, 1, 0, state)
		}
	}
}

func mergeProjectedMatches(existing []Match, projected []Match) []Match {
	merged := append(append([]Match(nil), existing...), projected...)
	sortMatches(merged)
	return dedupeMatches(merged)
}

func mergeProjectedGuidelines(existing []policy.Guideline, projected []policy.Guideline, matches []Match) []policy.Guideline {
	merged := append(append([]policy.Guideline(nil), existing...), projected...)
	sortGuidelines(merged, matches)
	return dedupeGuidelines(merged)
}

func clonePromptVersions(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func analyzeResponsePlan(ctx context.Context, router *model.Router, matchCtx MatchingContext, guidelines []policy.Guideline, templates []policy.Template, mode string, noMatch string) ResponseAnalysis {
	analysis := ResponseAnalysis{
		AnalyzedGuidelines: baselineResponseAnalysis(matchCtx, guidelines, templates, mode),
	}
	if strings.EqualFold(mode, "strict") && len(templates) > 0 {
		analysis.RecommendedTemplate = templates[0].Text
		analysis.Rationale = "strict mode prefers the highest-ranked approved template"
	}
	if router != nil && len(guidelines) > 0 && (len(templates) > 0 || strings.EqualFold(mode, "strict") || len(matchCtx.AssistantHistory) > 0) {
		type structuredResponseAnalysisBatch struct {
			NeedsRevision       bool                `json:"needs_revision"`
			NeedsStrictMode     bool                `json:"needs_strict_mode"`
			RecommendedTemplate string              `json:"recommended_template"`
			Rationale           string              `json:"rationale"`
			Analyzed            []AnalyzedGuideline `json:"analyzed_guidelines"`
		}
		results, ok := processBatchesInParallel(ctx, chunkGeneric(guidelines, 5), func(ctx context.Context, batch []policy.Guideline) (structuredResponseAnalysisBatch, bool) {
			var structured structuredResponseAnalysisBatch
			prompt := buildResponseAnalysisPrompt(matchCtx, batch, templates, mode, noMatch)
			if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
				return structuredResponseAnalysisBatch{}, false
			}
			return structured, true
		})
		if ok {
			for _, structured := range results {
				if len(structured.Analyzed) > 0 {
					analysis.AnalyzedGuidelines = mergeStructuredResponseAnalysis(analysis.AnalyzedGuidelines, structured.Analyzed)
				}
				analysis.NeedsRevision = analysis.NeedsRevision || structured.NeedsRevision
				analysis.NeedsStrictMode = analysis.NeedsStrictMode || structured.NeedsStrictMode
				analysis.RecommendedTemplate = firstNonEmpty(structured.RecommendedTemplate, analysis.RecommendedTemplate)
				analysis.Rationale = firstNonEmpty(structured.Rationale, analysis.Rationale)
			}
		}
	}
	for _, item := range analysis.AnalyzedGuidelines {
		if item.RequiresTemplate {
			analysis.NeedsStrictMode = true
			if analysis.RecommendedTemplate == "" && len(templates) > 0 {
				analysis.RecommendedTemplate = templates[0].Text
			}
			if analysis.Rationale == "" {
				analysis.Rationale = "analyzed guidelines require a template-backed response"
			}
			break
		}
	}
	if strings.EqualFold(mode, "strict") && len(templates) == 0 && noMatch != "" {
		allSatisfied := true
		for _, item := range analysis.AnalyzedGuidelines {
			if item.RequiresResponse {
				allSatisfied = false
				break
			}
		}
		if !allSatisfied {
			analysis.NeedsStrictMode = true
			if analysis.Rationale == "" {
				analysis.Rationale = "strict mode has no template, so the runtime must fall back to the configured strict response path"
			}
		}
	}
	return analysis
}

func baselineResponseAnalysis(matchCtx MatchingContext, guidelines []policy.Guideline, templates []policy.Template, mode string) []AnalyzedGuideline {
	out := make([]AnalyzedGuideline, 0, len(guidelines))
	for _, guideline := range guidelines {
		item := AnalyzedGuideline{
			ID:               guideline.ID,
			AppliedDegree:    "none",
			RequiresResponse: strings.TrimSpace(guideline.Then) != "",
			Rationale:        "matched guideline still influences the pending response",
		}
		appliedDegree, source, satisfiedByTool, rationale := guidelineApplicationAssessment(matchCtx, guideline)
		item.AppliedDegree = appliedDegree
		item.SatisfactionSource = source
		item.SatisfiedByToolEvent = satisfiedByTool
		if rationale != "" {
			item.Rationale = rationale
		}
		if appliedDegree == "full" {
			item.AlreadySatisfied = true
			item.RequiresResponse = false
		} else if appliedDegree == "partial" {
			item.RequiresResponse = true
			if item.Rationale == "" {
				item.Rationale = "guideline action appears only partially handled and still needs a follow-up response"
			}
		}
		if strings.EqualFold(mode, "strict") && len(templates) > 0 && item.RequiresResponse {
			item.RequiresTemplate = true
		}
		out = append(out, item)
	}
	return out
}

func responseAnalysisGuidelines(bundle policy.Bundle, matchCtx MatchingContext, matched []policy.Guideline) []policy.Guideline {
	out := append([]policy.Guideline(nil), matched...)
	seen := make(map[string]struct{}, len(out))
	for _, item := range out {
		seen[item.ID] = struct{}{}
	}
	for _, item := range bundle.Guidelines {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		if !item.Track {
			continue
		}
		if degree, _, _, _ := guidelineApplicationAssessment(matchCtx, item); degree == "none" {
			continue
		}
		out = append(out, item)
		seen[item.ID] = struct{}{}
	}
	return out
}

func mergeStructuredResponseAnalysis(base []AnalyzedGuideline, structured []AnalyzedGuideline) []AnalyzedGuideline {
	if len(structured) == 0 {
		return base
	}
	baseByID := make(map[string]AnalyzedGuideline, len(base))
	for _, item := range base {
		baseByID[item.ID] = item
	}
	merged := make([]AnalyzedGuideline, 0, len(structured))
	for _, item := range structured {
		if prior, ok := baseByID[item.ID]; ok && prior.SatisfiedByToolEvent {
			item.SatisfiedByToolEvent = true
			item.AlreadySatisfied = true
			item.SatisfactionSource = firstNonEmpty(prior.SatisfactionSource, "tool_event")
			item.AppliedDegree = firstNonEmpty(prior.AppliedDegree, "full")
			item.RequiresResponse = false
			item.Rationale = firstNonEmpty(item.Rationale, prior.Rationale)
		} else if prior, ok := baseByID[item.ID]; ok {
			item.SatisfactionSource = firstNonEmpty(item.SatisfactionSource, prior.SatisfactionSource)
			item.AppliedDegree = strongerAppliedDegree(item.AppliedDegree, prior.AppliedDegree)
			if item.AppliedDegree == "full" {
				item.AlreadySatisfied = true
				item.RequiresResponse = false
			} else if item.AppliedDegree == "partial" && !item.AlreadySatisfied {
				item.RequiresResponse = true
			}
		}
		merged = append(merged, item)
		delete(baseByID, item.ID)
	}
	for _, item := range base {
		if _, ok := baseByID[item.ID]; ok {
			merged = append(merged, item)
		}
	}
	return merged
}

func guidelineLikelySatisfiedByToolHistory(matchCtx MatchingContext, guideline policy.Guideline) bool {
	degree, _, _, _ := guidelineApplicationAssessment(matchCtx, guideline)
	return degree == "full"
}

func guidelineLikelySatisfiedByAssistantHistory(matchCtx MatchingContext, guideline policy.Guideline) bool {
	degree, source, _, _ := guidelineApplicationAssessment(matchCtx, guideline)
	return degree == "full" && source == "assistant_message"
}

func guidelineSatisfiedByStagedToolCalls(calls []StagedToolCall, guideline policy.Guideline) bool {
	then := strings.ToLower(strings.TrimSpace(guideline.Then))
	if then == "" {
		return false
	}
	for _, call := range calls {
		summary := strings.ToLower(summarizeStagedToolCall(call))
		switch {
		case strings.Contains(then, "check the return status"):
			if strings.Contains(summary, "return") || strings.Contains(summary, "status") {
				return true
			}
		case strings.Contains(then, "check the order status"):
			if strings.Contains(summary, "order") || strings.Contains(summary, "status") {
				return true
			}
		case strings.Contains(then, "offer a discount"):
			if strings.Contains(summary, "discount") {
				return true
			}
		case strings.Contains(then, "lock the card"):
			if strings.Contains(summary, "lock_card") || strings.Contains(summary, "locked") {
				return true
			}
		default:
			if containsAllKeywords(summary, guideline.Then) {
				return true
			}
		}
	}
	return false
}

func AdvanceJourney(instance *journey.Instance, activeState *policy.JourneyNode, activeJourney *policy.Journey, decision JourneyDecision) *journey.Instance {
	if instance == nil || activeState == nil || activeJourney == nil {
		return instance
	}
	now := time.Now().UTC()
	if strings.EqualFold(decision.Action, "exit") {
		instance.Status = journey.StatusCompleted
		instance.UpdatedAt = now
		return instance
	}
	if strings.EqualFold(decision.Action, "backtrack") && strings.TrimSpace(decision.BacktrackTo) != "" {
		instance.StateID = decision.BacktrackTo
		instance.Path = trimJourneyPath(instance.Path, decision.BacktrackTo)
		if nextState := strings.TrimSpace(decision.NextState); nextState != "" && nextState != decision.BacktrackTo {
			if branch := journeyPathBetween(*activeJourney, decision.BacktrackTo, nextState); len(branch) > 0 {
				instance.Path = append(instance.Path, branch...)
				instance.StateID = nextState
			}
		}
		instance.UpdatedAt = now
		return instance
	}
	nextState := strings.TrimSpace(decision.NextState)
	if nextState == "" {
		if len(journeyNextStateIDs(*activeJourney, activeState.ID)) == 0 {
			instance.Status = journey.StatusCompleted
			instance.UpdatedAt = now
			return instance
		}
		instance.UpdatedAt = now
		return instance
	}
	instance.StateID = nextState
	instance.Path = appendJourneyPath(instance.Path, nextState)
	instance.UpdatedAt = now
	return instance
}

func buildMatchingContext(events []session.Event) MatchingContext {
	ctx := MatchingContext{OccurredAt: time.Now().UTC()}
	applied := map[string]struct{}{}
	for _, event := range events {
		if event.CreatedAt.After(ctx.OccurredAt) {
			ctx.OccurredAt = event.CreatedAt
		}
		for _, part := range event.Content {
			if call, ok := stagedToolCallFromPart(event, part); ok {
				ctx.StagedToolCalls = append(ctx.StagedToolCalls, call)
				if summary := summarizeStagedToolCall(call); strings.TrimSpace(summary) != "" {
					ctx.StagedToolText = append(ctx.StagedToolText, summary)
				}
			}
			if part.Type != "text" || strings.TrimSpace(part.Text) == "" {
				continue
			}
			switch event.Source {
			case "customer":
				ctx.CustomerHistory = append(ctx.CustomerHistory, part.Text)
				ctx.LatestCustomerText = part.Text
			case "ai_agent":
				ctx.AssistantHistory = append(ctx.AssistantHistory, part.Text)
				ctx.AppliedInstructions = append(ctx.AppliedInstructions, part.Text)
				for _, token := range keywords(part.Text) {
					if strings.HasPrefix(token, "guideline") {
						applied[token] = struct{}{}
					}
				}
			}
		}
		if ctx.SessionID == "" {
			ctx.SessionID = event.SessionID
		}
	}
	segments := append([]string(nil), ctx.CustomerHistory...)
	segments = append(segments, ctx.AssistantHistory...)
	segments = append(segments, ctx.StagedToolText...)
	ctx.ConversationText = strings.Join(segments, " ")
	for id := range applied {
		ctx.AppliedGuidelines = append(ctx.AppliedGuidelines, id)
	}
	sort.Strings(ctx.AppliedGuidelines)
	return ctx
}

func stagedToolCallFromPart(event session.Event, part session.ContentPart) (StagedToolCall, bool) {
	if strings.TrimSpace(event.Kind) != "tool" {
		return StagedToolCall{}, false
	}
	meta := part.Meta
	if meta == nil {
		return StagedToolCall{}, false
	}
	toolID := strings.TrimSpace(fmt.Sprint(meta["tool_id"]))
	if toolID == "" {
		return StagedToolCall{}, false
	}
	call := StagedToolCall{
		ToolID:     toolID,
		Arguments:  mapValue(meta["arguments"]),
		Result:     mapValue(meta["result"]),
		DocumentID: strings.TrimSpace(fmt.Sprint(meta["document_id"])),
		ModulePath: strings.TrimSpace(fmt.Sprint(meta["module_path"])),
	}
	return call, true
}

func mapValue(v any) map[string]any {
	if v == nil {
		return nil
	}
	if out, ok := v.(map[string]any); ok {
		return out
	}
	return nil
}

func summarizeStagedToolCall(call StagedToolCall) string {
	parts := []string{"tool", call.ToolID}
	if len(call.Arguments) > 0 {
		parts = append(parts, "arguments", summarizeMap(call.Arguments))
	}
	if len(call.Result) > 0 {
		parts = append(parts, "result", summarizeMap(call.Result))
		if ageText := stagedToolAgeText(call); ageText != "" {
			parts = append(parts, ageText)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func stagedToolAgeText(call StagedToolCall) string {
	if !strings.Contains(strings.ToLower(call.ToolID), "age") {
		return ""
	}
	value, ok := call.Result["data"]
	if !ok {
		return ""
	}
	switch item := value.(type) {
	case int:
		return fmt.Sprintf("age %d", item)
	case int64:
		return fmt.Sprintf("age %d", item)
	case float64:
		if item > 0 && item < 130 {
			return fmt.Sprintf("age %d", int(item))
		}
	case string:
		if age, err := strconv.Atoi(strings.TrimSpace(item)); err == nil && age > 0 && age < 130 {
			return fmt.Sprintf("age %d", age)
		}
	}
	return ""
}

func summarizeMap(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	replacer := strings.NewReplacer("{", " ", "}", " ", "[", " ", "]", " ", "\"", " ", ":", " ")
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}

func runPolicyAttentionARQ(ctx MatchingContext, bundle policy.Bundle, projected []ProjectedJourneyNode) PolicyAttention {
	out := PolicyAttention{}
	source := matchingSource(ctx)
	for _, item := range bundle.Observations {
		if scoreCondition(item.When, source) > 0 {
			out.ContextSignals = append(out.ContextSignals, item.ID)
		}
	}
	for _, item := range bundle.Guidelines {
		if scoreCondition(item.When, source) > 0 {
			out.CriticalInstructionIDs = append(out.CriticalInstructionIDs, item.ID)
		}
	}
	for _, item := range projected {
		if scoreCondition(item.Instruction, source) > 0 {
			out.ContextSignals = append(out.ContextSignals, item.ID)
		}
	}
	if len(bundle.Journeys) > 0 && !containsAnyKeyword(source, "order", "return", "refund", "cancel", "damaged") {
		out.MissingInformation = append(out.MissingInformation, "goal_or_domain")
	}
	out.CriticalInstructionIDs = dedupe(out.CriticalInstructionIDs)
	out.ContextSignals = dedupe(out.ContextSignals)
	return out
}

func splitLowCriticalityGuidelines(items []policy.Guideline) ([]policy.Guideline, []policy.Guideline) {
	regular := make([]policy.Guideline, 0, len(items))
	low := make([]policy.Guideline, 0, len(items))
	for _, item := range items {
		if item.Priority < 0 {
			low = append(low, item)
			continue
		}
		regular = append(regular, item)
	}
	return regular, low
}

func runObservationARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, items []policy.Observation) ([]Match, []policy.Observation) {
	var matches []Match
	var out []policy.Observation
	source := matchingSource(matchCtx)
	if router != nil && len(items) > 0 {
		index := map[string]policy.Observation{}
		adapted := make([]policy.Observation, 0, len(items))
		for _, item := range items {
			index[item.ID] = item
			adapted = append(adapted, item)
		}
		type observationBatch struct {
			Matches []Match
			Items   []policy.Observation
		}
		results, ok := processBatchesInParallel(ctx, chunkGeneric(adapted, defaultARQBatchSize), func(ctx context.Context, batch []policy.Observation) (observationBatch, bool) {
			var structured struct {
				Checks []struct {
					ID        string `json:"id"`
					Applies   bool   `json:"applies"`
					Rationale string `json:"rationale"`
				} `json:"checks"`
			}
			prompt := buildObservationPrompt(matchCtx, batch)
			if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
				return observationBatch{}, false
			}
			var result observationBatch
			for _, check := range structured.Checks {
				item, ok := index[check.ID]
				if !ok || !check.Applies {
					continue
				}
				score := scoreCondition(item.When, source)
				result.Matches = append(result.Matches, Match{ID: item.ID, Kind: "observation", Score: float64(maxInt(score, 1) + item.Priority), Rationale: firstNonEmpty(check.Rationale, "structured match")})
				result.Items = append(result.Items, item)
			}
			return result, true
		})
		if ok {
			for _, result := range results {
				matches = append(matches, result.Matches...)
				out = append(out, result.Items...)
			}
		} else {
			matches = nil
			out = nil
		}
		if len(matches) > 0 || len(out) > 0 {
			sortMatches(matches)
			sortObservations(out, matches)
			return matches, dedupeObservations(out)
		}
	}
	for _, item := range items {
		score := scoreCondition(item.When, source)
		if score <= 0 {
			continue
		}
		matches = append(matches, Match{
			ID:        item.ID,
			Kind:      "observation",
			Score:     float64(score) + float64(item.Priority),
			Rationale: "customer context matches observation condition",
		})
		out = append(out, item)
	}
	sortMatches(matches)
	sortObservations(out, matches)
	return matches, out
}

func runActionableARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, items []policy.Guideline) ([]Match, []policy.Guideline) {
	var matches []Match
	var out []policy.Guideline
	source := matchingSource(matchCtx)
	if router != nil && len(items) > 0 {
		index := map[string]policy.Guideline{}
		adapted := make([]policy.Guideline, 0, len(items))
		for _, item := range items {
			index[item.ID] = item
			adapted = append(adapted, item)
		}
		type actionableBatch struct {
			Matches []Match
			Items   []policy.Guideline
		}
		results, ok := processBatchesInParallel(ctx, chunkGeneric(adapted, defaultARQBatchSize), func(ctx context.Context, batch []policy.Guideline) (actionableBatch, bool) {
			var structured struct {
				Checks []struct {
					ID        string `json:"id"`
					Applies   bool   `json:"applies"`
					Rationale string `json:"rationale"`
				} `json:"checks"`
			}
			prompt := buildActionablePrompt(matchCtx, batch)
			if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
				return actionableBatch{}, false
			}
			var result actionableBatch
			for _, check := range structured.Checks {
				item, ok := index[check.ID]
				if !ok || !check.Applies {
					continue
				}
				kind := "guideline"
				if strings.HasPrefix(item.ID, "journey_node:") {
					kind = "journey_node"
				}
				score := scoreCondition(item.When, source)
				result.Matches = append(result.Matches, Match{ID: item.ID, Kind: kind, Score: float64(maxInt(score, 1) + item.Priority), Rationale: firstNonEmpty(check.Rationale, "structured match")})
				result.Items = append(result.Items, item)
			}
			return result, true
		})
		if ok {
			for _, result := range results {
				matches = append(matches, result.Matches...)
				out = append(out, result.Items...)
			}
		} else {
			matches = nil
			out = nil
		}
		seen := map[string]struct{}{}
		for _, item := range out {
			seen[item.ID] = struct{}{}
		}
		for _, item := range items {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			score := scoreCondition(item.When, source)
			if score < 3 {
				continue
			}
			kind := "guideline"
			if strings.HasPrefix(item.ID, "journey_node:") {
				kind = "journey_node"
			}
			matches = append(matches, Match{
				ID:        item.ID,
				Kind:      kind,
				Score:     float64(score) + float64(item.Priority),
				Rationale: "deterministic condition signal is strong enough to retain this actionable match",
			})
			out = append(out, item)
		}
		if len(matches) > 0 || len(out) > 0 {
			sortMatches(matches)
			sortGuidelines(out, matches)
			return matches, dedupeGuidelines(out)
		}
	}
	for _, item := range items {
		score := scoreCondition(item.When, source)
		if score <= 0 {
			continue
		}
		kind := "guideline"
		if strings.HasPrefix(item.ID, "journey_node:") {
			kind = "journey_node"
		}
		matches = append(matches, Match{
			ID:        item.ID,
			Kind:      kind,
			Score:     float64(score) + float64(item.Priority),
			Rationale: "customer context satisfies actionable condition",
		})
		out = append(out, item)
	}
	sortMatches(matches)
	sortGuidelines(out, matches)
	return matches, out
}

func runPreviouslyAppliedARQ(ctx MatchingContext, items []policy.Guideline, matches []Match) ([]ReapplyDecision, []policy.Guideline) {
	var decisions []ReapplyDecision
	var out []policy.Guideline
	matchByID := map[string]Match{}
	for _, item := range matches {
		matchByID[item.ID] = item
	}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Scope), "journey") {
			decision := ReapplyDecision{
				ID:            item.ID,
				ShouldReapply: true,
				Score:         matchByID[item.ID].Score,
				Rationale:     "journey-scoped guidance remains active while the journey state is active",
			}
			decisions = append(decisions, decision)
			out = append(out, item)
			continue
		}
		decision := ReapplyDecision{
			ID:            item.ID,
			ShouldReapply: true,
			Score:         matchByID[item.ID].Score,
			Rationale:     "guideline has not been clearly satisfied earlier in the conversation",
		}
		alreadyApplied := containsEquivalentInstruction(ctx.AppliedInstructions, item.Then) || customerDependentQuestionWasAsked(ctx.AppliedInstructions, item.Then)
		customerDependent := strings.Contains(strings.ToLower(item.Scope), "customer") || containsAnyKeyword(item.Then, "ask", "confirm", "clarify", "reason", "details", "status")
		newSignal := scoreCondition(item.When, ctx.LatestCustomerText) > scoreCondition(item.When, strings.Join(ctx.CustomerHistory[:maxInt(len(ctx.CustomerHistory)-1, 0)], " "))
		if alreadyApplied && !customerDependent && !newSignal {
			decision.ShouldReapply = false
			decision.Score = 0
			decision.Rationale = "same guideline action appears to have already been taken and no new trigger is present"
		} else if alreadyApplied && customerDependent && !newSignal && customerSatisfiedGuideline(ctx.LatestCustomerText, item) {
			decision.ShouldReapply = false
			decision.Score = 0
			decision.Rationale = "customer provided the requested follow-up information, so the guideline does not need to be repeated"
		} else if alreadyApplied && customerDependent && newSignal {
			decision.Rationale = "guideline should be reapplied because the customer introduced a fresh trigger"
		}
		decisions = append(decisions, decision)
		if decision.ShouldReapply {
			out = append(out, item)
		}
	}
	return decisions, out
}

func customerSatisfiedGuideline(text string, item policy.Guideline) bool {
	loweredText := strings.ToLower(text)
	loweredAction := strings.ToLower(item.Then)
	if strings.Contains(loweredAction, "inside or outside") {
		return containsAnyPhrase(loweredText, "inside", "outside")
	}
	if strings.Contains(loweredAction, "email") {
		return strings.Contains(loweredText, "@")
	}
	if strings.Contains(loweredAction, "phone") || strings.Contains(loweredAction, "number") {
		for _, token := range strings.Fields(loweredText) {
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
	}
	return false
}

func customerDependentQuestionWasAsked(history []string, instruction string) bool {
	loweredInstruction := strings.ToLower(instruction)
	if strings.Contains(loweredInstruction, "inside or outside") {
		for _, item := range history {
			if containsAnyPhrase(strings.ToLower(item), "inside", "outside") {
				return true
			}
		}
	}
	return false
}

func guidelineLikelySatisfiedByCustomerAnswer(ctx MatchingContext, item policy.Guideline) bool {
	degree, source, _, _ := guidelineApplicationAssessment(ctx, item)
	return degree == "full" && source == "customer_answer"
}

func guidelineApplicationAssessment(matchCtx MatchingContext, guideline policy.Guideline) (degree string, source string, satisfiedByTool bool, rationale string) {
	then := strings.TrimSpace(guideline.Then)
	if then == "" {
		return "none", "", false, ""
	}
	if guidelineSatisfiedByStagedToolCalls(matchCtx.StagedToolCalls, guideline) {
		return "full", "tool_event", true, "guideline action appears satisfied by a previous tool-backed assistant turn"
	}
	if len(matchCtx.AssistantHistory) > 0 {
		history := strings.ToLower(strings.Join(matchCtx.AssistantHistory, "\n"))
		if assistantHistoryReflectsToolResult(history, then) {
			return "full", "tool_event", true, "guideline action appears satisfied by a previous tool-backed assistant turn"
		}
	}
	if len(matchCtx.AssistantHistory) > 0 {
		history := strings.ToLower(strings.Join(matchCtx.AssistantHistory, "\n"))
		if degree := actionCoverageDegree(history, then); degree != "none" {
			if degree == "full" {
				return degree, "assistant_message", false, "guideline action appears satisfied by a previous assistant turn"
			}
			return degree, "assistant_message", false, "guideline action appears partially handled by a previous assistant turn"
		}
	}
	if customerSatisfiedGuideline(matchCtx.LatestCustomerText, guideline) {
		loweredScope := strings.ToLower(guideline.Scope)
		if strings.Contains(loweredScope, "customer") || customerDependentQuestionWasAsked(matchCtx.AppliedInstructions, guideline.Then) || containsAnyKeyword(guideline.Then, "ask", "confirm", "clarify", "reason", "details", "status", "inside", "outside") {
			return "full", "customer_answer", false, "guideline action appears satisfied by the customer's follow-up answer"
		}
	}
	return "none", "", false, ""
}

func actionCoverageDegree(history string, instruction string) string {
	if strings.TrimSpace(history) == "" || strings.TrimSpace(instruction) == "" {
		return "none"
	}
	if containsEquivalentInstruction([]string{history}, instruction) {
		return "full"
	}
	segments := splitActionSegments(instruction)
	if len(segments) == 0 {
		if segmentSatisfiedByHistory(history, instruction) {
			return "full"
		}
		return "none"
	}
	matched := 0
	for _, segment := range segments {
		if segmentSatisfiedByHistory(history, segment) {
			matched++
		}
	}
	switch {
	case matched == 0:
		return "none"
	case matched == len(segments):
		return "full"
	default:
		return "partial"
	}
}

func splitActionSegments(instruction string) []string {
	normalized := strings.NewReplacer(" and then ", ".", " then ", ".", " and ", ".", ";", ".", ",", ".").Replace(strings.ToLower(strings.TrimSpace(instruction)))
	raw := strings.Split(normalized, ".")
	var out []string
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 && strings.TrimSpace(instruction) != "" {
		return []string{strings.TrimSpace(strings.ToLower(instruction))}
	}
	return out
}

func segmentSatisfiedByHistory(history string, segment string) bool {
	lowered := strings.ToLower(strings.TrimSpace(segment))
	if lowered == "" {
		return false
	}
	switch {
	case strings.Contains(lowered, "apolog"):
		return containsAnyPhrase(history, "sorry", "apolog")
	case strings.Contains(lowered, "discount"):
		return strings.Contains(history, "discount")
	case strings.Contains(lowered, "check the return status"), strings.Contains(lowered, "return status"):
		return containsAnyPhrase(history, "status is", "return status", "tracking")
	case strings.Contains(lowered, "check the order status"), strings.Contains(lowered, "order status"):
		return containsAnyPhrase(history, "order status", "status is", "tracking")
	case strings.Contains(lowered, "lock the card"):
		return containsAnyPhrase(history, "card is now locked", "locked your card", "lock_card", "locked")
	default:
		return containsAllKeywords(history, lowered)
	}
}

func strongerAppliedDegree(values ...string) string {
	best := "none"
	rank := map[string]int{"none": 0, "partial": 1, "full": 2}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if rank[value] > rank[best] {
			best = value
		}
	}
	return best
}

func assistantHistoryReflectsToolResult(history string, instruction string) bool {
	then := strings.ToLower(strings.TrimSpace(instruction))
	switch {
	case strings.Contains(then, "check the return status"):
		return strings.Contains(history, "status is") || strings.Contains(history, "return status") || strings.Contains(history, "tracking")
	case strings.Contains(then, "check the order status"):
		return strings.Contains(history, "order status") || strings.Contains(history, "status is")
	case strings.Contains(then, "lock the card"):
		return strings.Contains(history, "card is now locked") || strings.Contains(history, "locked your card")
	default:
		return false
	}
}

func runCustomerDependentARQ(ctx MatchingContext, items []policy.Guideline) ([]CustomerDependencyDecision, []policy.Guideline) {
	var decisions []CustomerDependencyDecision
	var out []policy.Guideline
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Scope), "journey") {
			decision := CustomerDependencyDecision{
				ID:                item.ID,
				CustomerDependent: false,
				Rationale:         "journey-scoped guidance is controlled by the active journey state, not customer-dependent filtering",
			}
			decisions = append(decisions, decision)
			out = append(out, item)
			continue
		}
		customerDependent := strings.Contains(strings.ToLower(item.Scope), "customer") || containsAnyKeyword(item.Then, "ask", "confirm", "clarify", "reason", "details", "status")
		alreadyApplied := containsEquivalentInstruction(ctx.AppliedInstructions, item.Then) || customerDependentQuestionWasAsked(ctx.AppliedInstructions, item.Then)
		decision := CustomerDependencyDecision{
			ID:                item.ID,
			CustomerDependent: customerDependent,
			Rationale:         "guideline can proceed without extra customer data",
		}
		if customerDependent && !containsAnyKeyword(ctx.LatestCustomerText, "because", "reason", "damaged", "refund", "cancel", "return", "order", "item", "details", "status") {
			decision.MissingCustomerData = append(decision.MissingCustomerData, "customer_confirmation")
			decision.Rationale = "guideline depends on customer clarification before execution"
		}
		decisions = append(decisions, decision)
		if len(decision.MissingCustomerData) == 0 || alreadyApplied {
			out = append(out, item)
		}
	}
	return decisions, out
}

func resolveJourney(bundle policy.Bundle, instances []journey.Instance, ctx MatchingContext) (*policy.Journey, *policy.JourneyNode, *journey.Instance) {
	instanceByJourney := map[string]journey.Instance{}
	for _, item := range instances {
		if item.Status == journey.StatusActive {
			instanceByJourney[item.JourneyID] = item
		}
	}
	for _, j := range bundle.Journeys {
		if instance, ok := instanceByJourney[j.ID]; ok {
			state := findState(j, instance.StateID)
			if state != nil {
				copiedJourney := j
				copiedState := *state
				copiedInstance := instance
				return &copiedJourney, &copiedState, &copiedInstance
			}
		}
	}

	bestScore := 0
	var selected *policy.Journey
	for _, j := range bundle.Journeys {
		score := 0
		for _, cond := range j.When {
			if v := scoreCondition(cond, ctx.LatestCustomerText); v > score {
				score = v
			}
		}
		score += j.Priority
		if score > bestScore && journeyRootState(&j) != nil {
			copied := j
			selected = &copied
			bestScore = score
		}
	}
	root := journeyRootState(selected)
	if selected == nil || root == nil {
		return nil, nil, nil
	}
	startState, startPath := firstExecutableJourneyState(selected, root.ID)
	if startState == nil {
		startState = root
		startPath = []string{root.ID}
	}
	instance := journey.Instance{
		ID:        fmt.Sprintf("journey_%s", selected.ID),
		SessionID: "",
		JourneyID: selected.ID,
		StateID:   startState.ID,
		Path:      startPath,
		Status:    journey.StatusActive,
		UpdatedAt: time.Now().UTC(),
	}
	state := *startState
	return selected, &state, &instance
}

func firstExecutableJourneyState(flow *policy.Journey, rootID string) (*policy.JourneyNode, []string) {
	if flow == nil {
		return nil, nil
	}
	path := []string{}
	currentID := strings.TrimSpace(rootID)
	seen := map[string]struct{}{}
	for currentID != "" {
		if _, ok := seen[currentID]; ok {
			break
		}
		seen[currentID] = struct{}{}
		path = append(path, currentID)
		current := findState(*flow, currentID)
		if current == nil {
			return nil, nil
		}
		nextIDs := journeyNextStateIDs(*flow, currentID)
		if !shouldSkipStartupNode(*current, len(nextIDs)) {
			return current, path
		}
		if len(nextIDs) != 1 {
			return current, path
		}
		currentID = strings.TrimSpace(nextIDs[0])
	}
	return nil, path
}

func shouldSkipStartupNode(state policy.JourneyNode, nextCount int) bool {
	if nextCount != 1 {
		return false
	}
	if strings.EqualFold(state.Kind, "fork") {
		return true
	}
	if strings.EqualFold(state.Type, "tool") {
		return false
	}
	return strings.TrimSpace(state.Instruction) == ""
}

func runJourneyBacktrackARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, instance *journey.Instance) JourneyDecision {
	if activeJourney == nil || activeState == nil || instance == nil {
		return JourneyDecision{}
	}
	rootID := ""
	if root := journeyRootState(activeJourney); root != nil {
		rootID = root.ID
	}
	if router != nil {
		var structured struct {
			RequiresBacktracking bool   `json:"requires_backtracking"`
			BacktrackToSame      bool   `json:"backtrack_to_same_journey_process"`
			Rationale            string `json:"rationale"`
		}
		prompt := buildJourneyBacktrackPrompt(matchCtx, activeJourney, activeState, rootID)
		if generateStructuredWithRetry(ctx, router, prompt, &structured) && structured.RequiresBacktracking {
			target := rootID
			nextState := ""
			if structured.BacktrackToSame {
				if selected := selectBestBacktrackTarget(matchCtx, activeJourney, instance.Path, activeState.ID, false); selected != nil {
					target = selected.ID
					nextState = backtrackFastForwardState(matchCtx, activeJourney, selected.ID)
				} else if strings.TrimSpace(target) == "" {
					target = activeState.ID
				}
			}
			if !isLegalBacktrackTarget(instance.Path, target, rootID) {
				return JourneyDecision{}
			}
			return JourneyDecision{
				Action:       "backtrack",
				CurrentState: activeState.ID,
				BacktrackTo:  target,
				NextState:    nextState,
				Rationale:    firstNonEmpty(structured.Rationale, "journey should backtrack before proceeding"),
			}
		}
	}
	message := strings.ToLower(matchCtx.LatestCustomerText)
	sameProcessMarkers := []string{"actually", "change", "changed", "resume", "continue", "go back"}
	newPurposeMarkers := []string{"instead", "different", "another", "new", "start over", "again", "restart"}
	if containsAnyPhrase(message, sameProcessMarkers...) {
		if selected := selectBestBacktrackTarget(matchCtx, activeJourney, instance.Path, activeState.ID, false); selected != nil {
			nextState := backtrackFastForwardState(matchCtx, activeJourney, selected.ID)
			if !isLegalBacktrackTarget(instance.Path, selected.ID, rootID) {
				return JourneyDecision{}
			}
			return JourneyDecision{
				Action:       "backtrack",
				CurrentState: activeState.ID,
				BacktrackTo:  selected.ID,
				NextState:    nextState,
				Rationale:    "customer is revisiting a previous decision in the same journey process",
			}
		}
	}
	if containsAnyPhrase(message, newPurposeMarkers...) && !containsAnyPhrase(message, sameProcessMarkers...) && rootID != "" && activeState.ID != rootID {
		if !isLegalBacktrackTarget(instance.Path, rootID, rootID) {
			return JourneyDecision{}
		}
		return JourneyDecision{
			Action:       "backtrack",
			CurrentState: activeState.ID,
			BacktrackTo:  rootID,
			Rationale:    "customer changed the purpose of the journey, so restart from the beginning",
		}
	}
	return JourneyDecision{}
}

func runJourneyProgressARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, instance *journey.Instance, backtrackDecision JourneyDecision) JourneyDecision {
	if activeJourney == nil || activeState == nil || instance == nil {
		return JourneyDecision{}
	}
	if strings.EqualFold(backtrackDecision.Action, "backtrack") {
		return backtrackDecision
	}
	decision := JourneyDecision{
		Action:       "continue",
		CurrentState: activeState.ID,
		Rationale:    "active journey remains relevant for this turn",
	}
	if router != nil {
		if decision, ok := runJourneyProgressStructuredARQ(ctx, router, matchCtx, activeJourney, activeState); ok {
			return decision
		}
	}
	if len(activeState.When) > 0 && !matchesAnyCondition(activeState.When, matchCtx.LatestCustomerText) {
		rootID := ""
		if root := journeyRootState(activeJourney); root != nil {
			rootID = root.ID
		}
		if selected := selectBestBacktrackTarget(matchCtx, activeJourney, instance.Path, activeState.ID, true); selected != nil && isLegalBacktrackTarget(instance.Path, selected.ID, rootID) {
			nextState := backtrackFastForwardState(matchCtx, activeJourney, selected.ID)
			decision.Action = "backtrack"
			decision.BacktrackTo = selected.ID
			decision.NextState = nextState
			decision.Rationale = "active node no longer matches the customer context, so the journey should backtrack"
			return decision
		}
		decision.Missing = append(decision.Missing, "state_condition")
		decision.Rationale = "state entry condition is not yet satisfied"
		return decision
	}
	nextIDs := journeyNextStateIDs(*activeJourney, activeState.ID)
	if activeState.Type == "tool" && len(nextIDs) > 0 {
		if !journeyToolStateExecuted(matchCtx, *activeState) {
			decision.Missing = append(decision.Missing, "tool_execution")
			decision.Rationale = "tool state requires execution before the journey can advance"
			return decision
		}
		decision.Action = "advance"
		decision.NextState = nextIDs[0]
		decision.Rationale = "tool state can advance after execution"
		return decision
	}
	if strings.EqualFold(activeState.Kind, "fork") && len(nextIDs) > 0 {
		bestNextID := selectNextJourneyState(matchCtx, activeJourney, activeState, nextIDs)
		if bestNextID == "" && len(nextIDs) == 1 {
			bestNextID = nextIDs[0]
		}
		if bestNextID != "" {
			decision.Action = "advance"
			decision.NextState = bestNextID
			decision.Rationale = "fork state routes to the best matching legal follow-up"
			return decision
		}
	}
	bestNextID := selectNextJourneyState(matchCtx, activeJourney, activeState, nextIDs)
	if bestNextID != "" {
		decision.Action = "advance"
		decision.NextState = bestNextID
		decision.Rationale = "customer response best matches the selected journey follow-up"
		return decision
	}
	return decision
}

func runLowCriticalityARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, items []policy.Guideline) ([]Match, []policy.Guideline) {
	var matches []Match
	var out []policy.Guideline
	source := matchingSource(matchCtx)
	if len(items) == 0 {
		return nil, nil
	}
	if router != nil {
		index := map[string]policy.Guideline{}
		for _, item := range items {
			index[item.ID] = item
		}
		type lowBatch struct {
			Matches []Match
			Items   []policy.Guideline
		}
		results, ok := processBatchesInParallel(ctx, chunkGeneric(items, defaultARQBatchSize), func(ctx context.Context, batch []policy.Guideline) (lowBatch, bool) {
			var structured struct {
				Checks []struct {
					ID        string `json:"id"`
					Applies   bool   `json:"applies"`
					Rationale string `json:"rationale"`
				} `json:"checks"`
			}
			prompt := buildLowCriticalityPrompt(matchCtx, batch)
			if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
				return lowBatch{}, false
			}
			var result lowBatch
			for _, check := range structured.Checks {
				item, ok := index[check.ID]
				if !ok || !check.Applies {
					continue
				}
				score := scoreCondition(item.When, source)
				result.Matches = append(result.Matches, Match{ID: item.ID, Kind: "guideline", Score: float64(maxInt(score, 1) + item.Priority), Rationale: firstNonEmpty(check.Rationale, "structured low-criticality match")})
				result.Items = append(result.Items, item)
			}
			return result, true
		})
		if ok {
			for _, result := range results {
				matches = append(matches, result.Matches...)
				out = append(out, result.Items...)
			}
		} else {
			matches = nil
			out = nil
		}
		if len(matches) > 0 || len(out) > 0 {
			sortMatches(matches)
			sortGuidelines(out, matches)
			return matches, dedupeGuidelines(out)
		}
	}
	for _, item := range items {
		score := scoreCondition(item.When, source)
		if score <= 0 {
			continue
		}
		matches = append(matches, Match{
			ID:        item.ID,
			Kind:      "guideline",
			Score:     float64(score) + float64(item.Priority),
			Rationale: "lower-criticality guideline remains relevant to the current subtopic",
		})
		out = append(out, item)
	}
	sortMatches(matches)
	sortGuidelines(out, matches)
	return matches, out
}

func runDisambiguationARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, guidelines []policy.Guideline, existing string) string {
	if existing != "" || router == nil || len(guidelines) < 2 {
		return existing
	}
	var structured struct {
		IsAmbiguous         bool   `json:"is_ambiguous"`
		ClarificationAction string `json:"clarification_action"`
	}
	prompt := buildDisambiguationPrompt(matchCtx, guidelines)
	if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
		return existing
	}
	if structured.IsAmbiguous {
		return firstNonEmpty(structured.ClarificationAction, "Could you clarify which option you mean?")
	}
	return existing
}

func resolveToolExposure(associations []policy.GuidelineToolAssociation, observations []policy.Observation, guidelines []policy.Guideline, state *policy.JourneyNode, toolPolicies []policy.ToolPolicy, catalog []tool.CatalogEntry) ([]string, map[string]string) {
	allowed := map[string]struct{}{}
	serverAllowed := map[string]struct{}{}
	approvals := map[string]string{}

	addApprovalAliases := func(entry tool.CatalogEntry, approval string) {
		if approval == "" {
			return
		}
		approvals[entry.ID] = approval
		approvals[entry.Name] = approval
		approvals[entry.ProviderID+"."+entry.Name] = approval
	}

	for _, item := range toolPolicies {
		for _, id := range item.ToolIDs {
			allowed[id] = struct{}{}
			if strings.TrimSpace(item.Approval) != "" {
				approvals[id] = item.Approval
			}
		}
	}

	activeGuidelines := map[string]struct{}{}
	for _, item := range guidelines {
		activeGuidelines[item.ID] = struct{}{}
	}
	for _, assoc := range associations {
		if _, ok := activeGuidelines[assoc.GuidelineID]; !ok {
			continue
		}
		if strings.HasSuffix(assoc.ToolID, ".*") {
			serverAllowed[strings.TrimSuffix(assoc.ToolID, ".*")] = struct{}{}
			continue
		}
		allowed[assoc.ToolID] = struct{}{}
	}

	addTools := func(items []string, ref *policy.MCPRef) {
		for _, item := range items {
			if strings.TrimSpace(item) != "" {
				allowed[item] = struct{}{}
			}
		}
		if ref == nil {
			return
		}
		if ref.Server != "" {
			serverAllowed[ref.Server] = struct{}{}
		}
		if ref.Tool != "" {
			allowed[ref.Server+"."+ref.Tool] = struct{}{}
		}
		for _, item := range ref.Tools {
			allowed[ref.Server+"."+item] = struct{}{}
		}
	}

	for _, item := range observations {
		addTools(item.Tools, item.MCP)
	}
	for _, item := range guidelines {
		// Guidelines are exposed through compiled associations; inline refs are fallback only.
		addTools(item.Tools, item.MCP)
	}
	if state != nil {
		addTools([]string{state.Tool}, state.MCP)
	}

	var out []string
	for _, entry := range catalog {
		if _, ok := allowed[entry.ID]; ok {
			out = append(out, entry.Name)
			addApprovalAliases(entry, approvals[entry.ID])
			continue
		}
		if _, ok := allowed[entry.Name]; ok {
			out = append(out, entry.Name)
			addApprovalAliases(entry, approvals[entry.Name])
			continue
		}
		if _, ok := allowed[entry.ProviderID+"."+entry.Name]; ok {
			out = append(out, entry.Name)
			addApprovalAliases(entry, approvals[entry.ProviderID+"."+entry.Name])
			continue
		}
		if _, ok := serverAllowed[entry.ProviderID]; ok {
			out = append(out, entry.Name)
		}
	}
	sort.Strings(out)
	return dedupe(out), approvals
}

func runJourneyProgressStructuredARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode) (JourneyDecision, bool) {
	var structured struct {
		Action      string   `json:"action"`
		NextState   string   `json:"next_state"`
		BacktrackTo string   `json:"backtrack_to"`
		Rationale   string   `json:"rationale"`
		Missing     []string `json:"missing"`
	}
	prompt := buildJourneyPrompt(matchCtx, activeJourney, activeState)
	if !generateStructuredWithRetry(ctx, router, prompt, &structured) {
		return JourneyDecision{}, false
	}
	decision := JourneyDecision{
		Action:       strings.ToLower(strings.TrimSpace(structured.Action)),
		CurrentState: activeState.ID,
		NextState:    strings.TrimSpace(structured.NextState),
		BacktrackTo:  strings.TrimSpace(structured.BacktrackTo),
		Rationale:    structured.Rationale,
		Missing:      structured.Missing,
	}
	if decision.Action == "" {
		return JourneyDecision{}, false
	}
	return decision, true
}

func collectTemplates(bundle policy.Bundle, activeJourney *policy.Journey, activeState *policy.JourneyNode, ctx MatchingContext) []policy.Template {
	templates := append([]policy.Template(nil), bundle.Templates...)
	if activeJourney != nil {
		templates = append(templates, activeJourney.Templates...)
	}
	if activeState != nil && strings.TrimSpace(activeState.Mode) != "" {
		for i := range templates {
			if strings.TrimSpace(templates[i].Mode) == "" {
				templates[i].Mode = activeState.Mode
			}
		}
	}
	sort.SliceStable(templates, func(i, j int) bool {
		left := templateScore(templates[i], ctx.LatestCustomerText)
		right := templateScore(templates[j], ctx.LatestCustomerText)
		if left == right {
			return templates[i].ID < templates[j].ID
		}
		return left > right
	})
	return templates
}

func previousState(flow *policy.Journey, stateID string) *policy.JourneyNode {
	if flow == nil {
		return nil
	}
	for _, edge := range flow.Edges {
		if strings.TrimSpace(edge.Target) != strings.TrimSpace(stateID) {
			continue
		}
		if strings.TrimSpace(edge.Source) == strings.TrimSpace(flow.RootID) {
			return nil
		}
		return findState(*flow, edge.Source)
	}
	return nil
}

func previousVisitedState(flow *policy.Journey, path []string, currentStateID string) *policy.JourneyNode {
	if flow == nil {
		return nil
	}
	if len(path) == 0 {
		return previousState(flow, currentStateID)
	}
	lastSeen := -1
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == currentStateID {
			lastSeen = i
			break
		}
	}
	if lastSeen <= 0 {
		return previousState(flow, currentStateID)
	}
	target := path[lastSeen-1]
	return findState(*flow, target)
}

func selectBestBacktrackTarget(ctx MatchingContext, flow *policy.Journey, path []string, currentStateID string, allowRootFallback bool) *policy.JourneyNode {
	if flow == nil {
		return nil
	}
	candidates := visitedBacktrackCandidates(flow, path, currentStateID)
	if len(candidates) == 0 {
		if allowRootFallback {
			return previousState(flow, currentStateID)
		}
		return previousVisitedState(flow, path, currentStateID)
	}
	best := candidates[0]
	bestScore := backtrackTargetScore(ctx, flow, best)
	for _, candidate := range candidates[1:] {
		score := backtrackTargetScore(ctx, flow, candidate)
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	return findState(*flow, best)
}

func visitedBacktrackCandidates(flow *policy.Journey, path []string, currentStateID string) []string {
	if flow == nil || len(path) == 0 {
		return nil
	}
	lastSeen := -1
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == currentStateID {
			lastSeen = i
			break
		}
	}
	if lastSeen <= 0 {
		if previous := previousState(flow, currentStateID); previous != nil {
			return []string{previous.ID}
		}
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for i := lastSeen - 1; i >= 0; i-- {
		if _, ok := seen[path[i]]; ok {
			continue
		}
		seen[path[i]] = struct{}{}
		out = append(out, path[i])
	}
	return out
}

func isLegalBacktrackTarget(path []string, target string, rootID string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if target == rootID {
		return true
	}
	for _, item := range path {
		if item == target {
			return true
		}
	}
	return false
}

func backtrackTargetScore(ctx MatchingContext, flow *policy.Journey, stateID string) int {
	score := 0
	state := findState(*flow, stateID)
	if state == nil {
		return score
	}
	score += journeyNextStateScore(ctx, *state)
	if len(state.When) > 0 && matchesAnyCondition(state.When, ctx.LatestCustomerText) {
		score += 4
		matchedConditions := 0
		for _, condition := range state.When {
			if scoreCondition(condition, ctx.LatestCustomerText) > 0 {
				matchedConditions++
			}
		}
		score += matchedConditions
	}
	if strings.EqualFold(state.Type, "message") || strings.EqualFold(state.Kind, "chat") {
		score += 1
	}
	if strings.Contains(strings.ToLower(ctx.LatestCustomerText), strings.ToLower(state.ID)) {
		score += 2
	}
	if journeyStateSatisfiedByHistory(ctx, *state, "") {
		score -= 5
		if nextState := fastForwardJourneyState(ctx, flow, stateID); strings.TrimSpace(nextState) != "" {
			score += 2
			score += len(journeyPathBetween(*flow, stateID, nextState))
		}
	} else {
		score += 2
	}
	if len(journeyNextStateIDs(*flow, stateID)) > 0 {
		score += 1
	}
	return score
}

func selectNextJourneyState(ctx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, nextIDs []string) string {
	bestNextID := ""
	bestNextScore := 0
	for _, nextID := range nextIDs {
		nextID = strings.TrimSpace(nextID)
		if nextID == "" {
			continue
		}
		score := 0
		if nextState := findState(*activeJourney, nextID); nextState != nil {
			score = journeyNextStateScore(ctx, *nextState)
			if edge := journeyEdgeBetween(*activeJourney, activeState.ID, nextID); edge != nil {
				score += scoreCondition(strings.TrimSpace(edge.Condition), ctx.LatestCustomerText)
			}
			if strings.EqualFold(nextState.Type, "tool") {
				score -= 1
			}
			if strings.EqualFold(nextState.Kind, "fork") {
				score += 1
			}
		} else {
			score = scoreCondition(nextID, ctx.LatestCustomerText)
		}
		if score > bestNextScore {
			bestNextScore = score
			bestNextID = nextID
		}
	}
	return bestNextID
}

func backtrackFastForwardState(ctx MatchingContext, activeJourney *policy.Journey, startStateID string) string {
	if activeJourney == nil {
		return ""
	}
	startState := findState(*activeJourney, startStateID)
	if startState == nil || !journeyStateSatisfiedByLatestTurn(ctx, *startState, "") {
		return ""
	}
	return fastForwardJourneyState(ctx, activeJourney, startStateID)
}

func fastForwardJourneyState(ctx MatchingContext, activeJourney *policy.Journey, startStateID string) string {
	if activeJourney == nil {
		return ""
	}
	currentID := strings.TrimSpace(startStateID)
	bestUnresolved := ""
	for currentID != "" {
		nextIDs := journeyNextStateIDs(*activeJourney, currentID)
		if len(nextIDs) == 0 {
			return bestUnresolved
		}
		advanced := false
		for _, nextID := range nextIDs {
			nextState := findState(*activeJourney, nextID)
			if nextState == nil {
				continue
			}
			if strings.EqualFold(nextState.Type, "tool") {
				return nextState.ID
			}
			edgeCondition := ""
			if edge := journeyEdgeBetween(*activeJourney, currentID, nextID); edge != nil {
				edgeCondition = edge.Condition
			}
			if !journeyStateSatisfiedByLatestTurn(ctx, *nextState, edgeCondition) {
				if bestUnresolved == "" {
					bestUnresolved = nextState.ID
				}
				continue
			}
			bestUnresolved = nextState.ID
			currentID = nextState.ID
			advanced = true
			break
		}
		if !advanced {
			return bestUnresolved
		}
	}
	return bestUnresolved
}

func journeyStateSatisfiedByLatestTurn(ctx MatchingContext, state policy.JourneyNode, edgeCondition string) bool {
	return journeyStateSatisfiedByText(ctx.LatestCustomerText, state, edgeCondition)
}

func journeyStateSatisfiedByHistory(ctx MatchingContext, state policy.JourneyNode, edgeCondition string) bool {
	return journeyStateSatisfiedByText(firstNonEmpty(ctx.ConversationText, ctx.LatestCustomerText), state, edgeCondition)
}

func journeyStateSatisfiedByText(text string, state policy.JourneyNode, edgeCondition string) bool {
	if strings.EqualFold(state.Type, "tool") {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if len(state.When) > 0 && matchesAnyCondition(state.When, text) {
		return true
	}
	if strings.TrimSpace(edgeCondition) != "" && scoreCondition(edgeCondition, text) > 0 {
		return true
	}
	pseudo := policy.Guideline{Then: state.Instruction, Scope: "customer"}
	return customerSatisfiedGuideline(text, pseudo)
}

func journeyToolStateExecuted(ctx MatchingContext, state policy.JourneyNode) bool {
	if !strings.EqualFold(state.Type, "tool") {
		return false
	}
	toolIDs := []string{}
	if strings.TrimSpace(state.Tool) != "" {
		toolIDs = append(toolIDs, strings.TrimSpace(state.Tool))
	}
	if state.MCP != nil {
		if strings.TrimSpace(state.MCP.Tool) != "" {
			toolIDs = append(toolIDs, strings.TrimSpace(state.MCP.Server)+"."+strings.TrimSpace(state.MCP.Tool))
		}
		for _, item := range state.MCP.Tools {
			if strings.TrimSpace(item) == "" {
				continue
			}
			toolIDs = append(toolIDs, strings.TrimSpace(state.MCP.Server)+"."+strings.TrimSpace(item))
		}
	}
	for _, call := range ctx.StagedToolCalls {
		for _, toolID := range toolIDs {
			if strings.TrimSpace(toolID) == "" {
				continue
			}
			if stagedToolMatchesToolID(call.ToolID, toolID) {
				return true
			}
		}
	}
	return false
}

func stagedToolMatchesToolID(callToolID string, expected string) bool {
	callToolID = strings.TrimSpace(callToolID)
	expected = strings.TrimSpace(expected)
	if callToolID == "" || expected == "" {
		return false
	}
	if callToolID == expected {
		return true
	}
	callTail := strings.TrimPrefix(callToolID, "local:")
	expectedTail := strings.TrimPrefix(expected, "local:")
	if callTail == expectedTail {
		return true
	}
	callDot := strings.ReplaceAll(callToolID, ":", ".")
	expectedDot := strings.ReplaceAll(expected, ":", ".")
	return callDot == expectedDot
}

func journeyPathBetween(flow policy.Journey, fromID string, toID string) []string {
	fromID = strings.TrimSpace(fromID)
	toID = strings.TrimSpace(toID)
	if fromID == "" || toID == "" || fromID == toID {
		return nil
	}
	type nodePath struct {
		id   string
		path []string
	}
	queue := []nodePath{{id: fromID, path: nil}}
	visited := map[string]struct{}{fromID: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, nextID := range journeyNextStateIDs(flow, current.id) {
			if _, ok := visited[nextID]; ok {
				continue
			}
			visited[nextID] = struct{}{}
			nextPath := append(append([]string(nil), current.path...), nextID)
			if nextID == toID {
				return nextPath
			}
			queue = append(queue, nodePath{id: nextID, path: nextPath})
		}
	}
	return nil
}

func trimJourneyPath(path []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return append([]string(nil), path...)
	}
	last := -1
	for i, item := range path {
		if item == target {
			last = i
		}
	}
	if last >= 0 {
		return append([]string(nil), path[:last+1]...)
	}
	return appendJourneyPath(path, target)
}

func appendJourneyPath(path []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return append([]string(nil), path...)
	}
	out := append([]string(nil), path...)
	if len(out) == 0 || out[len(out)-1] != next {
		out = append(out, next)
	}
	return out
}

func projectedNodeGuideline(j policy.Journey, state policy.JourneyNode) policy.Guideline {
	when := strings.Join(state.When, " ")
	if strings.TrimSpace(when) == "" {
		when = strings.Join(j.When, " ")
	}
	return policy.Guideline{
		ID:       "journey_node:" + j.ID + ":" + state.ID,
		When:     when,
		Then:     state.Instruction,
		Tools:    []string{state.Tool},
		MCP:      state.MCP,
		Scope:    "journey",
		Priority: j.Priority + state.Priority,
	}
}

func collectJourneyGuidelines(bundle policy.Bundle) []policy.Guideline {
	var out []policy.Guideline
	for _, item := range bundle.Journeys {
		out = append(out, item.Guidelines...)
		for _, state := range item.States {
			out = append(out, projectedNodeGuideline(item, state))
		}
	}
	return out
}

func matchedGuidelinesFromMatches(bundle policy.Bundle, matches []Match, activeJourney *policy.Journey) []policy.Guideline {
	index := map[string]policy.Guideline{}
	for _, item := range bundle.Guidelines {
		index[item.ID] = item
	}
	for _, item := range collectJourneyGuidelines(bundle) {
		index[item.ID] = item
	}
	if activeJourney != nil {
		for _, item := range activeJourney.Guidelines {
			index[item.ID] = item
		}
	}
	var out []policy.Guideline
	for _, match := range matches {
		item, ok := index[match.ID]
		if !ok {
			continue
		}
		out = append(out, item)
	}
	sortGuidelines(out, matches)
	return dedupeGuidelines(out)
}

func inferCompositionMode(templates []policy.Template) string {
	for _, tmpl := range templates {
		if strings.EqualFold(tmpl.Mode, "strict") {
			return "strict"
		}
	}
	return "fluid"
}

func templateScore(tmpl policy.Template, text string) int {
	return scoreCondition(firstNonEmpty(tmpl.When, tmpl.Text), text)
}

func renderTemplate(templates []policy.Template, toolOutput map[string]any) string {
	if len(templates) == 0 {
		return ""
	}
	out := templates[0].Text
	for key, value := range toolOutput {
		out = strings.ReplaceAll(out, "{{"+key+"}}", fmt.Sprint(value))
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return ""
	}
	return strings.TrimSpace(out)
}

func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return raw[start : end+1]
}

func buildObservationPrompt(ctx MatchingContext, items []policy.Observation) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"checks":[{"id":"string","applies":true,"rationale":"string"}]}`,
		"Determine which observational guidelines apply to the latest customer message.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Conversation context: " + firstNonEmpty(ctx.ConversationText, ctx.LatestCustomerText),
		"Only mark a condition as applicable if it is relevant to the latest user turn or to a clearly related sub-issue of the same unresolved topic.",
		"Do not keep a condition active if the conversation has clearly shifted to a different topic.",
		"Persistent user facts can remain applicable even when the current turn is about a different subtopic.",
		"Examples:",
	}
	lines = append(lines, formatShots(observationShots)...)
	lines = append(lines,
		"Guidelines:",
	)
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- id=%s condition=%q", item.ID, item.When))
	}
	return strings.Join(lines, "\n")
}

func buildActionablePrompt(ctx MatchingContext, items []policy.Guideline) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"checks":[{"id":"string","applies":true,"rationale":"string"}]}`,
		"Determine which actionable guidelines should influence the next reply.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Conversation context: " + firstNonEmpty(ctx.ConversationText, ctx.LatestCustomerText),
		"Assume the action has not been carried out yet for this stage. Focus on whether the guideline should influence the next reply now.",
		"Guidelines remain applicable across a related sub-issue of the same unresolved topic, but not after a clear topic shift.",
		"Examples:",
	}
	lines = append(lines, formatShots(actionableShots)...)
	lines = append(lines,
		"Guidelines:",
	)
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- id=%s condition=%q action=%q", item.ID, item.When, item.Then))
	}
	return strings.Join(lines, "\n")
}

func buildLowCriticalityPrompt(ctx MatchingContext, items []policy.Guideline) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"checks":[{"id":"string","applies":true,"rationale":"string"}]}`,
		"Determine which lower-criticality guidelines still matter for the latest turn.",
		"Only keep these guidelines if they remain relevant to the exact current turn or a closely related follow-up.",
		"Prefer precision over recall because these guidelines are lower priority than the main actionable set.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Conversation context: " + firstNonEmpty(ctx.ConversationText, ctx.LatestCustomerText),
		"Examples:",
	}
	lines = append(lines, formatShots(lowCriticalityShots)...)
	lines = append(lines, "Guidelines:")
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- id=%s condition=%q action=%q priority=%d", item.ID, item.When, item.Then, item.Priority))
	}
	return strings.Join(lines, "\n")
}

func buildDisambiguationPrompt(ctx MatchingContext, items []policy.Guideline) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"is_ambiguous":true,"clarification_action":"string"}`,
		"Decide whether the customer's latest message makes the active guidelines ambiguous.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Candidate guidelines:",
	}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- id=%s condition=%q action=%q", item.ID, item.When, item.Then))
	}
	lines = append(lines, "Examples:")
	lines = append(lines, formatShots(disambiguationShots)...)
	return strings.Join(lines, "\n")
}

func buildJourneyPrompt(ctx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode) string {
	nextIDs := journeyNextStateIDs(*activeJourney, activeState.ID)
	lines := []string{
		"Return only JSON.",
		`Schema: {"action":"continue|advance|backtrack|exit","next_state":"string","backtrack_to":"string","rationale":"string","missing":["string"]}`,
		"Choose the next journey action based on the latest customer message.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Journey: " + activeJourney.ID,
		fmt.Sprintf("Current state: id=%s type=%s instruction=%q next=%v", activeState.ID, activeState.Type, activeState.Instruction, nextIDs),
	}
	if len(nextIDs) > 0 {
		lines = append(lines, "Reachable follow-up states:")
		for _, nextID := range nextIDs {
			nextState := findState(*activeJourney, nextID)
			if nextState == nil {
				lines = append(lines, "- id="+nextID)
				continue
			}
			lines = append(lines, fmt.Sprintf("- id=%s type=%s when=%q instruction=%q", nextState.ID, nextState.Type, strings.Join(nextState.When, " OR "), nextState.Instruction))
		}
	}
	lines = append(lines, "Examples:")
	lines = append(lines, formatShots(journeyProgressShots)...)
	return strings.Join(lines, "\n")
}

func buildJourneyBacktrackPrompt(ctx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, rootID string) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"requires_backtracking":true,"backtrack_to_same_journey_process":true,"rationale":"string"}`,
		"Decide whether the active journey should backtrack before continuing.",
		"Backtracking is needed when the customer changes a previous decision, wants to restart the same process, or wants to do the same journey again for a new purpose.",
		"If the customer is changing a decision within the same ongoing process, set backtrack_to_same_journey_process=true.",
		"If the customer wants a different purpose or to start over, set backtrack_to_same_journey_process=false so the journey restarts from the beginning.",
		"Latest customer message: " + ctx.LatestCustomerText,
		fmt.Sprintf("Journey: %s root=%s current_state=%s", activeJourney.ID, rootID, activeState.ID),
		"Examples:",
	}
	lines = append(lines, formatShots(journeyBacktrackShots)...)
	return strings.Join(lines, "\n")
}

func buildToolDecisionPrompt(ctx MatchingContext, guidelines []policy.Guideline, activeJourney *policy.Journey, activeState *policy.JourneyNode, tools []string) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"selected_tool":"string","approval_required":false,"arguments":{"key":"value"},"rationale":"string"}`,
		"Choose the single best tool to run for the current turn.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Conversation context: " + firstNonEmpty(ctx.ConversationText, ctx.LatestCustomerText),
	}
	if activeJourney != nil {
		lines = append(lines, "Active journey: "+activeJourney.ID)
	}
	if activeState != nil {
		lines = append(lines, fmt.Sprintf("Active journey state: id=%s instruction=%q", activeState.ID, activeState.Instruction))
	}
	if len(guidelines) > 0 {
		lines = append(lines, "Matched guidelines:")
		for _, item := range guidelines {
			lines = append(lines, fmt.Sprintf("- id=%s action=%q", item.ID, item.Then))
		}
	}
	lines = append(lines, "Candidate tools:")
	for _, item := range tools {
		lines = append(lines, "- "+item)
	}
	lines = append(lines,
		"Example: prefer a tool explicitly referenced by the active journey state over a generic exposed tool.",
		"Example: avoid selecting a tool when no candidate is clearly justified by the current request.",
	)
	return strings.Join(lines, "\n")
}

func buildResponseAnalysisPrompt(ctx MatchingContext, guidelines []policy.Guideline, templates []policy.Template, mode string, noMatch string) string {
	lines := []string{
		"Return only JSON.",
		`Schema: {"needs_revision":true,"needs_strict_mode":false,"recommended_template":"string","rationale":"string","analyzed_guidelines":[{"id":"string","already_satisfied":false,"satisfied_by_tool_event":false,"satisfaction_source":"assistant_message|tool_event|customer_answer","applied_degree":"none|partial|full","requires_response":true,"requires_template":false,"rationale":"string"}]}`,
		"Analyze whether the matched guidelines still require new response content for the latest customer turn.",
		"Latest customer message: " + ctx.LatestCustomerText,
		"Previous assistant messages: " + firstNonEmpty(strings.Join(ctx.AssistantHistory, " | "), "none"),
		"Composition mode: " + mode,
		"No-match response: " + firstNonEmpty(noMatch, "none"),
		"Guidelines:",
	}
	for _, item := range guidelines {
		lines = append(lines, fmt.Sprintf("- id=%s condition=%q action=%q", item.ID, item.When, item.Then))
	}
	if len(templates) > 0 {
		lines = append(lines, "Candidate templates:")
		for _, tmpl := range templates {
			lines = append(lines, fmt.Sprintf("- id=%s mode=%s text=%q", tmpl.ID, tmpl.Mode, tmpl.Text))
		}
	}
	lines = append(lines, "Examples:")
	lines = append(lines, formatShots(responseAnalysisShots)...)
	return strings.Join(lines, "\n")
}

func formatShots(shots []stageShot) []string {
	out := make([]string, 0, len(shots)*2)
	for i, shot := range shots {
		out = append(out, fmt.Sprintf("%d. Input: %s", i+1, shot.Input))
		out = append(out, fmt.Sprintf("   Output: %s", shot.Output))
	}
	return out
}

func scoreCondition(condition, text string) int {
	conditionWords := keywords(condition)
	textWords := map[string]struct{}{}
	for _, token := range keywords(text) {
		textWords[token] = struct{}{}
	}
	score := 0
	for _, token := range conditionWords {
		if _, ok := textWords[token]; ok {
			score++
		}
	}
	score += ageConditionScore(strings.ToLower(condition), strings.ToLower(text))
	return score
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
	lowered := strings.ToLower(text)
	parts := strings.Fields(lowered)
	for i, token := range parts {
		token = strings.Trim(token, ".,!?;:\"'()[]{}")
		value, err := strconv.Atoi(token)
		if err != nil || value <= 0 || value >= 130 {
			continue
		}
		prev := ""
		next := ""
		if i > 0 {
			prev = strings.Trim(parts[i-1], ".,!?;:\"'()[]{}")
		}
		if i+1 < len(parts) {
			next = strings.Trim(parts[i+1], ".,!?;:\"'()[]{}")
		}
		if prev == "i'm" || prev == "im" || prev == "aged" || next == "years" || next == "year-old" || next == "yo" {
			return value, true
		}
		if i > 0 && parts[i-1] == "age" {
			return value, true
		}
	}
	return 0, false
}

func keywords(input string) []string {
	input = strings.NewReplacer("_", " ", "/", " ", "-", " ").Replace(input)
	raw := strings.Fields(strings.ToLower(input))
	var out []string
	stop := map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "to": {}, "for": {}, "with": {}, "of": {}, "is": {}, "are": {}, "be": {}, "i": {}, "you": {}, "my": {}, "your": {}, "it": {}, "this": {}, "that": {}, "do": {}, "does": {},
	}
	aliases := map[string]string{
		"hi":        "hello",
		"hey":       "hello",
		"greetings": "hello",
		"greet":     "hello",
		"greeting":  "hello",
		"says":      "say",
		"said":      "say",
		"saying":    "say",
	}
	for _, token := range raw {
		token = strings.Trim(token, ".,!?;:\"'()[]{}")
		if canonical, ok := aliases[token]; ok {
			token = canonical
		}
		if len(token) < 3 {
			continue
		}
		if _, ok := stop[token]; ok {
			continue
		}
		out = append(out, token)
	}
	return out
}

func sortMatches(items []Match) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].ID < items[j].ID
		}
		return items[i].Score > items[j].Score
	})
}

func sortGuidelines(items []policy.Guideline, matches []Match) {
	scoreByID := map[string]float64{}
	for _, item := range matches {
		scoreByID[item.ID] = item.Score
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := scoreByID[items[i].ID] + float64(items[i].Priority)
		right := scoreByID[items[j].ID] + float64(items[j].Priority)
		if left == right {
			return items[i].ID < items[j].ID
		}
		return left > right
	})
}

func sortObservations(items []policy.Observation, matches []Match) {
	scoreByID := map[string]float64{}
	for _, item := range matches {
		scoreByID[item.ID] = item.Score
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := scoreByID[items[i].ID] + float64(items[i].Priority)
		right := scoreByID[items[j].ID] + float64(items[j].Priority)
		if left == right {
			return items[i].ID < items[j].ID
		}
		return left > right
	})
}

func dedupeObservations(items []policy.Observation) []policy.Observation {
	seen := map[string]struct{}{}
	out := make([]policy.Observation, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeGuidelines(items []policy.Guideline) []policy.Guideline {
	seen := map[string]struct{}{}
	out := make([]policy.Guideline, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeMatches(items []Match) []Match {
	seen := map[string]struct{}{}
	out := make([]Match, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func findState(j policy.Journey, stateID string) *policy.JourneyNode {
	for _, state := range j.States {
		if state.ID == stateID {
			copied := state
			return &copied
		}
	}
	return nil
}

func journeyRootState(j *policy.Journey) *policy.JourneyNode {
	if j == nil {
		return nil
	}
	rootID := strings.TrimSpace(j.RootID)
	if rootID == "" && len(j.States) > 0 {
		rootID = j.States[0].ID
	}
	if rootID == "" {
		return nil
	}
	if state := findState(*j, rootID); state != nil {
		return state
	}
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.Source) != rootID {
			continue
		}
		targetID := strings.TrimSpace(edge.Target)
		if targetID == "" {
			continue
		}
		if state := findState(*j, targetID); state != nil {
			return state
		}
	}
	return nil
}

func journeyNextStateIDs(j policy.Journey, stateID string) []string {
	var out []string
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.Source) != strings.TrimSpace(stateID) {
			continue
		}
		out = append(out, strings.TrimSpace(edge.Target))
	}
	if len(out) > 0 {
		return dedupe(out)
	}
	if state := findState(j, stateID); state != nil {
		return dedupe(append([]string(nil), state.Next...))
	}
	return nil
}

func journeyEdgeBetween(j policy.Journey, sourceID string, targetID string) *policy.JourneyEdge {
	for i := range j.Edges {
		edge := &j.Edges[i]
		if strings.TrimSpace(edge.Source) == strings.TrimSpace(sourceID) && strings.TrimSpace(edge.Target) == strings.TrimSpace(targetID) {
			return edge
		}
	}
	return nil
}

func dedupe(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func matchesAnyCondition(conditions []string, text string) bool {
	for _, condition := range conditions {
		if scoreCondition(condition, text) > 0 {
			return true
		}
	}
	return false
}

func containsAnyKeyword(text string, words ...string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	textWords := map[string]struct{}{}
	for _, word := range keywords(text) {
		textWords[word] = struct{}{}
	}
	for _, word := range words {
		for _, token := range keywords(word) {
			if _, ok := textWords[token]; ok {
				return true
			}
		}
	}
	return false
}

func containsAnyPhrase(text string, phrases ...string) bool {
	text = strings.ToLower(text)
	for _, phrase := range phrases {
		if strings.Contains(text, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

func containsAllKeywords(text string, sample string) bool {
	required := keywords(sample)
	if len(required) == 0 {
		return true
	}
	present := map[string]struct{}{}
	for _, word := range keywords(text) {
		present[word] = struct{}{}
	}
	for _, token := range required {
		if _, ok := present[token]; !ok {
			return false
		}
	}
	return true
}

func containsEquivalentInstruction(history []string, instruction string) bool {
	instruction = normalizeText(instruction)
	if instruction == "" {
		return false
	}
	for _, item := range history {
		if containsAllKeywords(item, instruction) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func matchingSource(ctx MatchingContext) string {
	if len(ctx.StagedToolText) > 0 && strings.TrimSpace(ctx.ConversationText) != "" {
		return ctx.ConversationText
	}
	if strings.TrimSpace(ctx.LatestCustomerText) != "" {
		return ctx.LatestCustomerText
	}
	return ctx.ConversationText
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func modeOrDefault(mode string, templates []policy.Template) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" {
		return mode
	}
	return inferCompositionMode(templates)
}

func journeyNextStateScore(ctx MatchingContext, state policy.JourneyNode) int {
	score := 0
	for _, condition := range state.When {
		if v := scoreCondition(condition, ctx.LatestCustomerText); v > score {
			score = v
		}
	}
	if v := scoreCondition(state.Instruction, ctx.LatestCustomerText); v > score {
		score = v
	}
	if v := scoreCondition(state.ID, ctx.LatestCustomerText); v > score {
		score = v
	}
	return score
}

func findToolCatalogEntry(catalog []tool.CatalogEntry, selected string) (tool.CatalogEntry, bool) {
	selected = strings.TrimSpace(selected)
	for _, entry := range catalog {
		if entry.Name == selected || entry.ID == selected || entry.ProviderID+"."+entry.Name == selected {
			return entry, true
		}
	}
	return tool.CatalogEntry{}, false
}

type toolArgumentSpec struct {
	Required     bool
	Hidden       bool
	HasDefault   bool
	DefaultValue any
	Choices      []string
	Significance string
}

func extractToolRequirements(entry tool.CatalogEntry) map[string]toolArgumentSpec {
	var raw map[string]any
	if strings.TrimSpace(entry.Schema) == "" || json.Unmarshal([]byte(entry.Schema), &raw) != nil {
		return nil
	}
	specs := map[string]toolArgumentSpec{}

	if schemaSpecs, ok := extractJSONSchemaRequirements(raw); ok {
		return schemaSpecs
	}

	if params, ok := raw["parameters"].([]any); ok {
		for _, item := range params {
			param, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(fmt.Sprint(param["name"]))
			if name == "" {
				continue
			}
			spec := specs[name]
			spec.Required = spec.Required || truthyValue(param["required"])
			spec.Hidden = spec.Hidden || truthyValue(param["x-parmesan-hidden"]) || truthyValue(param["x-hidden"]) || truthyValue(param["x-internal"])
			spec.Significance = firstNonEmpty(spec.Significance, significanceForSchema(param))
			if schema, ok := param["schema"].(map[string]any); ok {
				mergeToolSpec(&spec, extractToolSpecFromSchema(schema))
			}
			specs[name] = normalizeToolSpec(spec)
		}
	}
	if requestBody, ok := raw["requestBody"].(map[string]any); ok {
		if content, ok := requestBody["content"].(map[string]any); ok {
			for _, media := range content {
				mediaMap, ok := media.(map[string]any)
				if !ok {
					continue
				}
				schema, ok := mediaMap["schema"].(map[string]any)
				if !ok {
					continue
				}
				if bodySpecs, ok := extractJSONSchemaRequirements(schema); ok {
					for key, spec := range bodySpecs {
						existing := specs[key]
						mergeToolSpec(&existing, spec)
						specs[key] = normalizeToolSpec(existing)
					}
					break
				}
			}
		}
	}
	return specs
}

func extractJSONSchemaRequirements(schema map[string]any) (map[string]toolArgumentSpec, bool) {
	propsRaw, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	required := map[string]bool{}
	if reqs, ok := schema["required"].([]any); ok {
		for _, item := range reqs {
			required[fmt.Sprint(item)] = true
		}
	}
	specs := map[string]toolArgumentSpec{}
	for key, propRaw := range propsRaw {
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		spec := extractToolSpecFromSchema(prop)
		spec.Required = required[key]
		specs[key] = normalizeToolSpec(spec)
	}
	return specs, true
}

func extractToolSpecFromSchema(schema map[string]any) toolArgumentSpec {
	spec := toolArgumentSpec{
		Significance: significanceForSchema(schema),
		Hidden:       truthyValue(schema["x-parmesan-hidden"]) || truthyValue(schema["x-hidden"]) || truthyValue(schema["x-internal"]),
	}
	if _, ok := schema["default"]; ok {
		spec.HasDefault = true
		spec.DefaultValue = schema["default"]
	}
	if enumValues, ok := schema["enum"].([]any); ok {
		for _, value := range enumValues {
			spec.Choices = append(spec.Choices, fmt.Sprint(value))
		}
	}
	return spec
}

func mergeToolSpec(dst *toolArgumentSpec, src toolArgumentSpec) {
	dst.Required = dst.Required || src.Required
	dst.Hidden = dst.Hidden || src.Hidden
	dst.HasDefault = dst.HasDefault || src.HasDefault
	if dst.DefaultValue == nil && src.DefaultValue != nil {
		dst.DefaultValue = src.DefaultValue
	}
	dst.Significance = firstNonEmpty(dst.Significance, src.Significance)
	dst.Choices = dedupe(append(dst.Choices, src.Choices...))
}

func normalizeToolSpec(spec toolArgumentSpec) toolArgumentSpec {
	spec.Choices = dedupe(spec.Choices)
	if spec.Significance == "" {
		switch {
		case spec.Hidden:
			spec.Significance = "internal"
		case spec.Required:
			spec.Significance = "critical"
		default:
			spec.Significance = "contextual"
		}
	}
	return spec
}

func significanceForSchema(schema map[string]any) string {
	return strings.TrimSpace(firstNonEmpty(
		stringValue(schema["x-parmesan-significance"]),
		stringValue(schema["x-significance"]),
	))
}

func truthyValue(v any) bool {
	switch item := v.(type) {
	case bool:
		return item
	case string:
		return strings.EqualFold(strings.TrimSpace(item), "true")
	default:
		return false
	}
}

func stringValue(v any) string {
	switch item := v.(type) {
	case string:
		return item
	default:
		return ""
	}
}

func isEmptyArgumentValue(v any) bool {
	if v == nil {
		return true
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	return s == "" || s == "<nil>"
}

func issueReasonForMissing(spec toolArgumentSpec) string {
	switch spec.Significance {
	case "critical":
		return "critical required parameter is missing"
	case "internal":
		return "internal parameter is missing and could not be derived"
	default:
		return "required parameter is missing"
	}
}

func inferHiddenArgumentValue(field string, spec toolArgumentSpec, args map[string]any) (any, bool) {
	if !spec.Hidden {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "session_id", "sessionid":
		return args["session_id"], args["session_id"] != nil && !isEmptyArgumentValue(args["session_id"])
	case "customer_message", "message", "query", "prompt":
		for _, key := range []string{"customer_message", "conversation_excerpt"} {
			if value, ok := args[key]; ok && !isEmptyArgumentValue(value) {
				return value, true
			}
		}
	case "conversation_excerpt", "conversation", "transcript":
		for _, key := range []string{"conversation_excerpt", "customer_message"} {
			if value, ok := args[key]; ok && !isEmptyArgumentValue(value) {
				return value, true
			}
		}
	case "journey_id":
		return args["journey_id"], args["journey_id"] != nil && !isEmptyArgumentValue(args["journey_id"])
	case "journey_state", "state_id":
		for _, key := range []string{"journey_state", "state_id"} {
			if value, ok := args[key]; ok && !isEmptyArgumentValue(value) {
				return value, true
			}
		}
	}
	return nil, false
}

func stringInSlice(value string, items []string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func normalizeText(input string) string {
	return strings.Join(keywords(input), " ")
}

func recordSuppressed(out *[]SuppressedGuideline, idx map[string]int, id string, reason string, related ...string) {
	if i, ok := idx[id]; ok {
		(*out)[i].RelatedIDs = dedupe(append((*out)[i].RelatedIDs, related...))
		return
	}
	item := SuppressedGuideline{
		ID:         id,
		Reason:     reason,
		RelatedIDs: dedupe(append([]string(nil), related...)),
	}
	idx[id] = len(*out)
	*out = append(*out, item)
}

func guidelinePriority(item policy.Guideline, match Match) float64 {
	return float64(item.Priority) + match.Score
}

func journeyID(item *policy.Journey) string {
	if item == nil {
		return ""
	}
	return item.ID
}

func matchesJourneyDependencyTarget(target string, activeJourney *policy.Journey) bool {
	if activeJourney == nil {
		return false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	return target == activeJourney.ID || target == "journey:"+activeJourney.ID
}

func shareConditionTopic(left string, right string) bool {
	leftWords := keywords(strings.ToLower(left))
	rightWords := keywords(strings.ToLower(right))
	rightSet := make(map[string]struct{}, len(rightWords))
	for _, word := range rightWords {
		if isAgeQualifierWord(word) {
			continue
		}
		rightSet[word] = struct{}{}
	}
	for _, word := range leftWords {
		if isAgeQualifierWord(word) {
			continue
		}
		if _, ok := rightSet[word]; ok {
			return true
		}
	}
	return false
}

func isAgeQualifierWord(word string) bool {
	switch word {
	case "under", "over", "older", "younger", "below", "least", "traveler":
		return true
	default:
		return false
	}
}

func hasDirectRelationship(items []policy.Relationship, left string, right string) bool {
	for _, item := range items {
		if (item.Source == left && item.Target == right) || (item.Source == right && item.Target == left) {
			return true
		}
	}
	return false
}
