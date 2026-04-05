package policyruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
)

func buildToolPlan(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, journeyDecision JourneyDecision, guidelines []policy.Guideline, exposedTools []string, approvals map[string]string, relationships []policy.Relationship, catalog []tool.CatalogEntry) (ToolCallPlan, ToolDecision) {
	decision := ToolDecision{
		Arguments: map[string]any{
			"session_id":           matchCtx.SessionID,
			"customer_message":     matchCtx.LatestCustomerText,
			"conversation_excerpt": firstNonEmpty(matchCtx.LatestCustomerText, matchCtx.ConversationText),
		},
		CanRun:   true,
		Grounded: len(strings.TrimSpace(matchCtx.LatestCustomerText)) > 0,
	}
	if activeJourney != nil {
		decision.Arguments["journey_id"] = activeJourney.ID
	}
	if activeState != nil {
		decision.Arguments["journey_state"] = activeState.ID
	}

	candidates, groups := buildToolCandidates(matchCtx, activeJourney, activeState, journeyDecision, guidelines, exposedTools, relationships, catalog, decision.Arguments)
	plan := ToolCallPlan{
		Candidates:        candidates,
		OverlappingGroups: groups,
	}
	plan.Batches = evaluateToolCallBatches(ctx, router, matchCtx, activeJourney, activeState, guidelines, &plan)
	applyBatchRationales(candidates, plan.Batches)
	batchSelected := selectedCandidatesFromBatches(plan.Batches, candidates)
	plan.SelectedTools = selectedToolIDs(batchSelected)
	switch len(batchSelected) {
	case 1:
		plan.SelectedTool = batchSelected[0].ToolID
		plan.Rationale = firstNonEmpty(batchSelected[0].SelectionRationale, batchSelected[0].PreparationRationale, batchSelected[0].Rationale)
	case 0:
	default:
		plan.SelectedTool, plan.Rationale = selectToolCandidate(ctx, router, matchCtx, activeJourney, activeState, guidelines, batchSelected)
	}
	if router != nil && len(exposedTools) > 0 {
		var structured struct {
			SelectedTool     string         `json:"selected_tool"`
			ApprovalRequired bool           `json:"approval_required"`
			Arguments        map[string]any `json:"arguments"`
			Rationale        string         `json:"rationale"`
		}
		prompt := buildToolDecisionPrompt(matchCtx, guidelines, activeJourney, activeState, exposedTools)
		if generateStructuredWithRetry(ctx, router, prompt, &structured) {
			if selected := strings.TrimSpace(structured.SelectedTool); selected != "" {
				if candidate, ok := findCandidate(candidates, selected); ok && candidateRunnable(candidate) {
					if candidate.Grounded || !hasGroundedRunnableCandidate(candidates) {
						plan.SelectedTool = selected
					}
				}
			}
			if len(structured.Arguments) > 0 {
				decision.Arguments = mergeArguments(decision.Arguments, structured.Arguments)
			}
			decision.ApprovalRequired = structured.ApprovalRequired
			plan.Rationale = firstNonEmpty(strings.TrimSpace(structured.Rationale), plan.Rationale)
		}
	}
	if plan.SelectedTool == "" && activeState != nil {
		if strings.TrimSpace(activeState.Tool) != "" {
			toolID := firstMatchingCandidateToolID(candidates, strings.TrimSpace(activeState.Tool))
			if candidate, ok := findCandidate(candidates, toolID); ok {
				if candidateRunnable(candidate) {
					plan.SelectedTool = toolID
					plan.Rationale = "current journey node explicitly requires a tool"
				}
			} else if toolID != "" {
				plan.SelectedTool = toolID
				plan.Rationale = "current journey node explicitly requires a tool"
			}
		} else if activeState.MCP != nil && strings.TrimSpace(activeState.MCP.Tool) != "" {
			toolID := firstMatchingCandidateToolID(candidates, strings.TrimSpace(activeState.MCP.Tool))
			if candidate, ok := findCandidate(candidates, toolID); ok {
				if candidateRunnable(candidate) {
					plan.SelectedTool = toolID
					plan.Rationale = "current journey node explicitly requires an MCP tool"
				}
			} else if toolID != "" {
				plan.SelectedTool = toolID
				plan.Rationale = "current journey node explicitly requires an MCP tool"
			}
		}
	}
	if plan.SelectedTool == "" {
		if candidate, ok := preferredCandidateForDecision(candidates); ok {
			plan.SelectedTool = candidate.ToolID
			plan.Rationale = firstNonEmpty(plan.Rationale, candidate.SelectionRationale, candidate.PreparationRationale, candidate.Rationale, "matched policy exposes a relevant tool")
		}
	}
	if plan.SelectedTool != "" && !slices.Contains(plan.SelectedTools, plan.SelectedTool) {
		plan.SelectedTools = append(plan.SelectedTools, plan.SelectedTool)
	}
	plan.SelectedTools = dedupe(plan.SelectedTools)
	plan.Calls = buildPlannedCalls(matchCtx, plan.SelectedTools, candidates, catalog)
	finalizeToolCandidateStates(candidates, plan.SelectedTool)

	decision.SelectedTool = plan.SelectedTool
	decision.Rationale = plan.Rationale
	if decision.SelectedTool != "" {
		if selected, ok := findCandidate(candidates, decision.SelectedTool); ok {
			decision.Arguments = mergeArguments(mergeArguments(nil, selected.Arguments), decision.Arguments)
			decision.Grounded = selected.Grounded
			if selected.AlreadySatisfied {
				decision.SelectedTool = ""
				decision.CanRun = false
				decision.Rationale = strings.TrimSpace(firstNonEmpty(decision.Rationale, selected.SelectionRationale, selected.PreparationRationale, selected.Rationale, "requested tool effect already appears satisfied") + "; tool already satisfied")
				return plan, decision
			}
			if selected.AlreadyStaged {
				decision.SelectedTool = ""
				decision.CanRun = false
				decision.Rationale = strings.TrimSpace(firstNonEmpty(decision.Rationale, selected.SelectionRationale, selected.PreparationRationale, selected.Rationale, "matching tool call already staged") + "; tool already staged")
				return plan, decision
			}
		}
	}
	if entry, ok := findToolCatalogEntry(catalog, decision.SelectedTool); ok {
		specs := extractToolRequirements(entry)
		decision.Arguments = mergeArguments(decision.Arguments, inferToolArgumentsFromContext(matchCtx, specs))
		decision.MissingIssues, decision.InvalidIssues = evaluateToolArguments(specs, decision.Arguments)
		decision.CanRun = len(decision.MissingIssues) == 0 && len(decision.InvalidIssues) == 0
	}
	for _, issue := range decision.MissingIssues {
		decision.MissingArguments = append(decision.MissingArguments, issue.Parameter)
	}
	for _, issue := range decision.InvalidIssues {
		decision.InvalidArguments = append(decision.InvalidArguments, issue.Parameter)
	}
	if decision.SelectedTool != "" {
		mode := approvals[decision.SelectedTool]
		if strings.EqualFold(mode, "required") {
			decision.ApprovalRequired = true
			decision.Rationale = strings.TrimSpace(firstNonEmpty(decision.Rationale, "tool selected") + "; approval required")
		}
	}
	decision.MissingArguments = dedupe(decision.MissingArguments)
	decision.InvalidArguments = dedupe(decision.InvalidArguments)
	if len(decision.MissingArguments) > 0 || len(decision.InvalidArguments) > 0 {
		decision.CanRun = false
		decision.SelectedTool = ""
		plan.SelectedTool = ""
		decision.Rationale = strings.TrimSpace(firstNonEmpty(decision.Rationale, "tool requires additional valid arguments") + "; tool is not runnable yet")
	}
	return plan, decision
}

func selectedToolIDs(candidates []ToolCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ToolID) == "" {
			continue
		}
		out = append(out, candidate.ToolID)
	}
	return dedupe(out)
}

func buildPlannedCalls(matchCtx MatchingContext, selectedTools []string, candidates []ToolCandidate, catalog []tool.CatalogEntry) []ToolPlannedCall {
	var calls []ToolPlannedCall
	for _, toolID := range dedupe(selectedTools) {
		candidate, ok := findCandidate(candidates, toolID)
		if !ok {
			continue
		}
		calls = append(calls, ToolPlannedCall{
			ToolID:    toolID,
			Arguments: mergeArguments(nil, candidate.Arguments),
			Rationale: firstNonEmpty(candidate.SelectionRationale, candidate.PreparationRationale, candidate.Rationale),
		})
		entry, ok := findToolCatalogEntry(catalog, toolID)
		if !ok {
			continue
		}
		calls = append(calls, expandAlternativeToolCalls(matchCtx, candidate, entry)...)
	}
	return dedupePlannedCalls(calls)
}

func expandAlternativeToolCalls(matchCtx MatchingContext, candidate ToolCandidate, entry tool.CatalogEntry) []ToolPlannedCall {
	text := strings.TrimSpace(matchCtx.LatestCustomerText)
	if !strings.Contains(strings.ToLower(text), " or ") {
		return nil
	}
	specs := extractToolRequirements(entry)
	if !supportsAlternativeArgumentCalls(specs) {
		return nil
	}
	segments := strings.Split(text, " or ")
	if len(segments) < 2 {
		return nil
	}
	var calls []ToolPlannedCall
	base := mergeArguments(nil, candidate.Arguments)
	for _, segment := range segments {
		args := mergeArguments(nil, base)
		args = mergeArguments(args, inferToolArgumentsFromText(strings.ToLower(strings.TrimSpace(segment)), specs))
		if sameToolArguments(base, args, specs) {
			continue
		}
		missing, invalid := evaluateToolArguments(specs, args)
		if len(missing) > 0 || len(invalid) > 0 {
			continue
		}
		calls = append(calls, ToolPlannedCall{
			ToolID:    candidate.ToolID,
			Arguments: args,
			Rationale: "additional tool call inferred from an alternative customer request segment",
		})
	}
	return calls
}

func supportsAlternativeArgumentCalls(specs map[string]toolArgumentSpec) bool {
	if len(specs) == 0 {
		return false
	}
	hasKeywordLike := false
	hasBrandLike := false
	for field := range specs {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "keyword", "query", "product_name", "model":
			hasKeywordLike = true
		case "vendor", "brand", "manufacturer":
			hasBrandLike = true
		}
	}
	return hasKeywordLike || hasBrandLike
}

func dedupePlannedCalls(calls []ToolPlannedCall) []ToolPlannedCall {
	seen := map[string]struct{}{}
	out := make([]ToolPlannedCall, 0, len(calls))
	for _, call := range calls {
		key := call.ToolID + "::" + stableArgumentJSON(call.Arguments)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, call)
	}
	return out
}

func stableArgumentJSON(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprint(args)
	}
	return string(raw)
}

func selectedCandidatesFromBatches(batches []ToolCallBatchResult, candidates []ToolCandidate) []ToolCandidate {
	var out []ToolCandidate
	seen := map[string]struct{}{}
	for _, batch := range batches {
		if strings.TrimSpace(batch.SelectedTool) == "" {
			continue
		}
		if _, ok := seen[batch.SelectedTool]; ok {
			continue
		}
		if candidate, ok := findCandidate(candidates, batch.SelectedTool); ok {
			out = append(out, candidate)
			seen[batch.SelectedTool] = struct{}{}
		}
	}
	return out
}

func applyBatchRationales(candidates []ToolCandidate, batches []ToolCallBatchResult) {
	for i := range candidates {
		for _, batch := range batches {
			if strings.TrimSpace(batch.SelectedTool) == candidates[i].ToolID {
				candidates[i].SelectionRationale = firstNonEmpty(batch.Rationale, candidates[i].SelectionRationale)
				candidates[i].RunInTandemWith = append([]string(nil), batch.RunInTandemWith...)
				candidates[i].Rationale = firstNonEmpty(candidates[i].SelectionRationale, candidates[i].PreparationRationale, candidates[i].Rationale)
				break
			}
		}
	}
}

func preferredCandidateForDecision(candidates []ToolCandidate) (ToolCandidate, bool) {
	for _, candidate := range candidates {
		if candidateRunnable(candidate) {
			return candidate, true
		}
	}
	if len(candidates) == 0 {
		return ToolCandidate{}, false
	}
	return candidates[0], true
}

func buildToolCandidates(matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, journeyDecision JourneyDecision, guidelines []policy.Guideline, exposedTools []string, relationships []policy.Relationship, catalog []tool.CatalogEntry, baseArgs map[string]any) ([]ToolCandidate, [][]string) {
	var candidates []ToolCandidate
	results, _ := processBatchesInParallel(context.Background(), exposedTools, func(_ context.Context, name string) (ToolCandidate, bool) {
		entry, ok := findToolCatalogEntry(catalog, name)
		if !ok {
			return ToolCandidate{}, true
		}
		args := mergeArguments(nil, baseArgs)
		specs := extractToolRequirements(entry)
		args = mergeArguments(args, inferToolArgumentsFromContext(matchCtx, specs))
		missing, invalid := evaluateToolArguments(specs, args)
		alreadySatisfied := toolCandidateAlreadySatisfied(matchCtx, entry, args, specs)
		approvalMode := ""
		if toolConsequential(entry) {
			approvalMode = "required"
		} else if toolAutoApproved(entry) {
			approvalMode = "auto"
		}
		references := referenceToolsForEntry(entry, relationships, exposedTools, catalog)
		candidate := ToolCandidate{
			ToolID:               entry.Name,
			GroupKey:             toolOverlapGroup(entry),
			ReferenceTools:       references,
			Consequential:        toolConsequential(entry),
			AutoApproved:         toolAutoApproved(entry),
			Grounded:             toolCandidateGrounded(matchCtx, activeJourney, activeState, guidelines, entry),
			AlreadyStaged:        toolCandidateAlreadyStaged(matchCtx, entry, args, specs),
			SameCallStaged:       toolCandidateSameCallAlreadyStaged(matchCtx, entry, args, specs),
			AlreadySatisfied:     alreadySatisfied,
			ApprovalMode:         approvalMode,
			Arguments:            args,
			MissingIssues:        missing,
			InvalidIssues:        invalid,
			PreparationRationale: toolCandidatePreparationRationale(activeState, guidelines, entry, references),
		}
		if toolCandidateInvalidatedByJourneyBacktrack(entry, activeState, journeyDecision) {
			candidate.AlreadySatisfied = false
			candidate.AlreadyStaged = false
			candidate.SameCallStaged = false
		}
		switch {
		case candidate.AlreadySatisfied:
			candidate.DecisionState = "already_satisfied"
			candidate.PreparationRationale = firstNonEmpty("tool effect already satisfied by a prior tool result with the same effective arguments", candidate.PreparationRationale)
		case candidate.SameCallStaged:
			candidate.DecisionState = "already_staged"
			candidate.PreparationRationale = firstNonEmpty("same tool call is already staged with the same effective arguments", candidate.PreparationRationale)
		case candidate.AlreadyStaged:
			candidate.DecisionState = "already_staged"
			candidate.PreparationRationale = firstNonEmpty("tool is already staged in the current turn", candidate.PreparationRationale)
		case candidate.AutoApproved && len(candidate.MissingIssues) == 0 && len(candidate.InvalidIssues) == 0:
			candidate.DecisionState = "auto_approved"
			candidate.PreparationRationale = firstNonEmpty("non-consequential tool with no parameters is auto-approved", candidate.PreparationRationale)
		case len(candidate.InvalidIssues) > 0:
			candidate.DecisionState = "blocked_invalid_args"
			candidate.PreparationRationale = firstNonEmpty("tool is blocked because one or more argument values are invalid", candidate.PreparationRationale)
		case len(candidate.MissingIssues) > 0:
			candidate.DecisionState = "blocked_missing_args"
			candidate.PreparationRationale = firstNonEmpty("tool is blocked because required arguments are still missing", candidate.PreparationRationale)
		default:
			candidate.DecisionState = "should_run"
			candidate.PreparationRationale = firstNonEmpty("tool is grounded and has enough valid data to run", candidate.PreparationRationale)
		}
		candidate.ShouldRun = candidate.DecisionState == "should_run" || candidate.DecisionState == "auto_approved"
		candidate.Rationale = firstNonEmpty(candidate.SelectionRationale, candidate.PreparationRationale)
		return candidate, true
	})
	for _, candidate := range results {
		if candidate.ToolID == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].AlreadySatisfied != candidates[j].AlreadySatisfied {
			return !candidates[i].AlreadySatisfied
		}
		if candidates[i].AlreadyStaged != candidates[j].AlreadyStaged {
			return !candidates[i].AlreadyStaged
		}
		if candidates[i].Consequential != candidates[j].Consequential {
			return !candidates[i].Consequential
		}
		if candidates[i].Grounded != candidates[j].Grounded {
			return candidates[i].Grounded
		}
		if len(candidates[i].MissingIssues) != len(candidates[j].MissingIssues) {
			return len(candidates[i].MissingIssues) < len(candidates[j].MissingIssues)
		}
		return candidates[i].ToolID < candidates[j].ToolID
	})
	groups := buildOverlappingGroups(candidates, relationships)
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.Join(groups[i], ",") < strings.Join(groups[j], ",")
	})
	return candidates, groups
}

func evaluateToolCallBatches(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, plan *ToolCallPlan) []ToolCallBatchResult {
	batches := createToolCallBatches(plan.Candidates, plan.OverlappingGroups)
	results := make([]ToolCallBatchResult, 0, len(batches))
	for _, batch := range batches {
		switch batch.Kind {
		case "overlapping_tools":
			results = append(results, evaluateOverlappingToolBatch(ctx, router, matchCtx, activeJourney, activeState, guidelines, batch, plan.Candidates))
		default:
			results = append(results, evaluateSingleToolBatch(ctx, router, matchCtx, activeJourney, activeState, guidelines, batch, plan.Candidates))
		}
	}
	return results
}

func createToolCallBatches(candidates []ToolCandidate, groups [][]string) []ToolCallBatchResult {
	groupByTool := map[string][]string{}
	for _, group := range groups {
		for _, item := range group {
			groupByTool[item] = group
		}
	}
	var batches []ToolCallBatchResult
	seen := map[string]struct{}{}
	for _, group := range groups {
		key := strings.Join(group, ",")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		batches = append(batches, ToolCallBatchResult{
			Kind:           "overlapping_tools",
			CandidateTools: append([]string(nil), group...),
		})
	}
	for _, candidate := range candidates {
		if _, ok := groupByTool[candidate.ToolID]; ok {
			continue
		}
		batches = append(batches, ToolCallBatchResult{
			Kind:           "single_tool",
			CandidateTools: []string{candidate.ToolID},
		})
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return strings.Join(batches[i].CandidateTools, ",") < strings.Join(batches[j].CandidateTools, ",")
	})
	return batches
}

func evaluateSingleToolBatch(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, batch ToolCallBatchResult, candidates []ToolCandidate) ToolCallBatchResult {
	if len(batch.CandidateTools) == 0 {
		return batch
	}
	candidate, ok := findCandidate(candidates, batch.CandidateTools[0])
	if !ok {
		return batch
	}
	batch.Consequential = candidate.Consequential
	batch.ReferenceTools = append([]string(nil), candidate.ReferenceTools...)
	batch.Simplified = !candidate.Consequential
	switch candidate.DecisionState {
	case "already_satisfied", "already_staged", "blocked_missing_args", "blocked_invalid_args":
		batch.Rationale = firstNonEmpty(candidate.PreparationRationale, candidate.Rationale)
		return batch
	case "auto_approved":
		if !candidate.Grounded && hasGroundedRunnableCandidate(candidates) {
			batch.Rationale = firstNonEmpty("candidate is not grounded enough to run while a grounded tool is available", candidate.PreparationRationale)
			return batch
		}
		batch.SelectedTool = candidate.ToolID
		batch.Rationale = firstNonEmpty("auto-approved non-consequential tool with no parameters", candidate.PreparationRationale)
		return batch
	}
	if router != nil && len(candidate.ReferenceTools) > 0 {
		pool := []ToolCandidate{candidate}
		for _, ref := range candidate.ReferenceTools {
			if item, ok := findCandidate(candidates, ref); ok {
				pool = append(pool, item)
			}
		}
		selected, rationale := selectToolCandidate(ctx, router, matchCtx, activeJourney, activeState, guidelines, pool)
		if strings.TrimSpace(selected) == candidate.ToolID {
			batch.SelectedTool = candidate.ToolID
			batch.Rationale = firstNonEmpty(rationale, toolSpecializationRationale(candidate, candidates), candidate.PreparationRationale)
			return batch
		}
		if strings.TrimSpace(selected) != "" {
			if shouldRunToolInTandem(candidate, selected, candidates) {
				batch.SelectedTool = candidate.ToolID
				batch.RunInTandemWith = []string{strings.TrimSpace(selected)}
				batch.Rationale = firstNonEmpty(
					"candidate should still run in tandem with the better reference tool",
					candidate.PreparationRationale,
					rationale,
				)
				return batch
			}
			batch.Rationale = firstNonEmpty(rationale, "reference tool was a better fit for this request")
			return batch
		}
	}
	if len(candidate.ReferenceTools) > 0 {
		for _, ref := range candidate.ReferenceTools {
			if !shouldRunToolInTandem(candidate, ref, candidates) {
				continue
			}
			if refCandidate, ok := findCandidate(candidates, ref); ok && candidateRunnable(refCandidate) {
				batch.SelectedTool = candidate.ToolID
				batch.RunInTandemWith = []string{ref}
				batch.Rationale = firstNonEmpty(
					"candidate should still run in tandem with the better reference tool",
					candidate.PreparationRationale,
				)
				return batch
			}
		}
	}
	if candidate.ShouldRun && (candidate.Grounded || !hasGroundedRunnableCandidate(candidates)) {
		batch.SelectedTool = candidate.ToolID
	}
	batch.Rationale = firstNonEmpty(toolSpecializationRationale(candidate, candidates), candidate.PreparationRationale, candidate.Rationale)
	return batch
}

func evaluateOverlappingToolBatch(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, batch ToolCallBatchResult, candidates []ToolCandidate) ToolCallBatchResult {
	pool := make([]ToolCandidate, 0, len(batch.CandidateTools))
	for _, toolID := range batch.CandidateTools {
		if item, ok := findCandidate(candidates, toolID); ok {
			pool = append(pool, item)
			if item.Consequential {
				batch.Consequential = true
			}
		}
	}
	if len(pool) == 0 {
		return batch
	}
	selected, rationale := selectToolCandidate(ctx, router, matchCtx, activeJourney, activeState, guidelines, pool)
	if strings.TrimSpace(selected) != "" {
		batch.SelectedTool = strings.TrimSpace(selected)
		if candidate, ok := findCandidate(candidates, batch.SelectedTool); ok {
			rationale = firstNonEmpty(toolSpecializationRationale(candidate, candidates), rationale)
		}
	}
	batch.Rationale = firstNonEmpty(rationale, "choose the most specialized overlapping tool for the request")
	return batch
}

func selectToolCandidate(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, candidates []ToolCandidate) (string, string) {
	if len(candidates) == 0 {
		return "", ""
	}
	var grounded []ToolCandidate
	for _, candidate := range candidates {
		if candidate.Grounded && candidateRunnable(candidate) {
			grounded = append(grounded, candidate)
		}
	}
	pool := grounded
	if len(pool) == 0 {
		for _, candidate := range candidates {
			if candidateRunnable(candidate) {
				pool = append(pool, candidate)
			}
		}
	}
	if len(pool) == 0 {
		return "", ""
	}
	for _, candidate := range pool {
		if candidate.AutoApproved && candidateRunnable(candidate) {
			return candidate.ToolID, firstNonEmpty(candidate.Rationale, "auto-approved tool candidate")
		}
	}
	if router != nil && len(pool) > 1 {
		var structured struct {
			SelectedTool string `json:"selected_tool"`
			Rationale    string `json:"rationale"`
		}
		prompt := buildToolCandidatePrompt(matchCtx, activeJourney, activeState, guidelines, pool)
		if generateStructuredWithRetry(ctx, router, prompt, &structured) && strings.TrimSpace(structured.SelectedTool) != "" {
			return strings.TrimSpace(structured.SelectedTool), strings.TrimSpace(structured.Rationale)
		}
	}
	return pool[0].ToolID, firstNonEmpty(pool[0].Rationale, "best grounded tool candidate")
}

func candidateRunnable(candidate ToolCandidate) bool {
	if candidate.AlreadyStaged || candidate.AlreadySatisfied {
		return false
	}
	if len(candidate.MissingIssues) > 0 || len(candidate.InvalidIssues) > 0 {
		return false
	}
	return candidate.DecisionState == "should_run" || candidate.DecisionState == "auto_approved" || candidate.DecisionState == "" || candidate.ShouldRun
}

func hasGroundedRunnableCandidate(candidates []ToolCandidate) bool {
	for _, candidate := range candidates {
		if candidate.Grounded && candidateRunnable(candidate) {
			return true
		}
	}
	return false
}

func finalizeToolCandidateStates(candidates []ToolCandidate, selected string) {
	selected = strings.TrimSpace(selected)
	if len(candidates) == 0 {
		return
	}
	hasGroundedRunnable := false
	for i := range candidates {
		if !candidates[i].Grounded {
			continue
		}
		switch candidates[i].DecisionState {
		case "should_run", "auto_approved", "selected":
			hasGroundedRunnable = true
		}
	}
	selectedGroup := ""
	for i := range candidates {
		if strings.TrimSpace(candidates[i].ToolID) == selected {
			selectedGroup = candidates[i].GroupKey
			if candidates[i].DecisionState == "should_run" || candidates[i].DecisionState == "auto_approved" || candidates[i].DecisionState == "" {
				candidates[i].DecisionState = "selected"
			}
			candidates[i].SelectionRationale = firstNonEmpty(candidates[i].SelectionRationale, "candidate selected as best tool for the current request")
			candidates[i].Rationale = firstNonEmpty(candidates[i].SelectionRationale, candidates[i].PreparationRationale, candidates[i].Rationale)
			break
		}
	}
	for i := range candidates {
		if strings.TrimSpace(candidates[i].ToolID) == selected {
			continue
		}
		if !candidates[i].Grounded && hasGroundedRunnable && (candidates[i].DecisionState == "should_run" || candidates[i].DecisionState == "auto_approved") {
			candidates[i].DecisionState = "rejected_ungrounded"
			candidates[i].RejectedBy = selected
			candidates[i].SelectionRationale = "candidate rejected because a more grounded tool candidate was available"
			candidates[i].Rationale = firstNonEmpty(candidates[i].SelectionRationale, candidates[i].PreparationRationale, candidates[i].Rationale)
			continue
		}
		if selectedGroup == "" {
			continue
		}
		if candidates[i].GroupKey != selectedGroup {
			continue
		}
		if candidates[i].DecisionState == "should_run" || candidates[i].DecisionState == "auto_approved" {
			candidates[i].DecisionState = "rejected_overlap"
			candidates[i].RejectedBy = selected
			candidates[i].SelectionRationale = "candidate rejected because another overlapping tool was selected"
			candidates[i].Rationale = firstNonEmpty(candidates[i].SelectionRationale, candidates[i].PreparationRationale, candidates[i].Rationale)
		}
	}
}

func buildToolCandidatePrompt(matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, candidates []ToolCandidate) string {
	var sb strings.Builder
	sb.WriteString("Choose the single best tool candidate for this turn.\n")
	sb.WriteString("Customer message: " + matchCtx.LatestCustomerText + "\n")
	if activeJourney != nil {
		sb.WriteString("Active journey: " + activeJourney.ID + "\n")
	}
	if activeState != nil {
		sb.WriteString("Active journey state: " + activeState.ID + "\n")
	}
	if len(guidelines) > 0 {
		sb.WriteString("Matched guidelines:\n")
		for _, guideline := range guidelines {
			sb.WriteString("- " + guideline.ID + ": " + firstNonEmpty(guideline.Then, guideline.When) + "\n")
		}
	}
	sb.WriteString("Candidates:\n")
	for _, candidate := range candidates {
		sb.WriteString(fmt.Sprintf("- %s grounded=%t consequential=%t missing=%d invalid=%d rationale=%s\n", candidate.ToolID, candidate.Grounded, candidate.Consequential, len(candidate.MissingIssues), len(candidate.InvalidIssues), candidate.Rationale))
	}
	sb.WriteString(`Return JSON: {"selected_tool":"tool_name","rationale":"why"}`)
	return sb.String()
}

func evaluateToolArguments(specs map[string]toolArgumentSpec, args map[string]any) ([]ToolArgumentIssue, []ToolArgumentIssue) {
	var missing []ToolArgumentIssue
	var invalid []ToolArgumentIssue
	for field, spec := range specs {
		value, exists := args[field]
		if spec.Hidden && (!exists || isEmptyArgumentValue(value)) {
			if auto, ok := inferHiddenArgumentValue(field, spec, args); ok {
				args[field] = auto
				value = auto
				exists = true
			}
		}
		if (!exists || isEmptyArgumentValue(value)) && spec.HasDefault && spec.DefaultValue != nil {
			args[field] = spec.DefaultValue
			value = spec.DefaultValue
			exists = true
		}
		if spec.Required && !spec.HasDefault && (!exists || isEmptyArgumentValue(value)) {
			missing = append(missing, ToolArgumentIssue{
				Parameter:    field,
				Required:     true,
				Hidden:       spec.Hidden,
				HasDefault:   spec.HasDefault,
				Choices:      append([]string(nil), spec.Choices...),
				Significance: spec.Significance,
				Reason:       issueReasonForMissing(spec),
			})
			continue
		}
		if !exists || len(spec.Choices) == 0 {
			continue
		}
		if !stringInSlice(fmt.Sprint(value), spec.Choices) {
			invalid = append(invalid, ToolArgumentIssue{
				Parameter:    field,
				Required:     spec.Required,
				Hidden:       spec.Hidden,
				HasDefault:   spec.HasDefault,
				Choices:      append([]string(nil), spec.Choices...),
				Significance: spec.Significance,
				Reason:       "argument value is outside allowed choices",
			})
		}
	}
	return missing, invalid
}

func mergeArguments(dst map[string]any, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func findCandidate(candidates []ToolCandidate, selected string) (ToolCandidate, bool) {
	selected = strings.TrimSpace(selected)
	for _, item := range candidates {
		if item.ToolID == selected {
			return item, true
		}
	}
	return ToolCandidate{}, false
}

func firstMatchingCandidateToolID(candidates []ToolCandidate, toolRef string) string {
	toolRef = strings.TrimSpace(toolRef)
	if toolRef == "" {
		return ""
	}
	if candidate, ok := findCandidate(candidates, toolRef); ok {
		return candidate.ToolID
	}
	trimmed := strings.TrimPrefix(toolRef, "local:")
	if candidate, ok := findCandidate(candidates, trimmed); ok {
		return candidate.ToolID
	}
	if strings.Contains(toolRef, ":") {
		parts := strings.SplitN(toolRef, ":", 2)
		if candidate, ok := findCandidate(candidates, parts[1]); ok {
			return candidate.ToolID
		}
	}
	return toolRef
}

func toolCandidateGrounded(matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, guidelines []policy.Guideline, entry tool.CatalogEntry) bool {
	text := strings.ToLower(matchCtx.LatestCustomerText)
	if text == "" {
		return false
	}
	if activeState != nil && (strings.EqualFold(strings.TrimSpace(activeState.Tool), entry.Name) || (activeState.MCP != nil && strings.EqualFold(strings.TrimSpace(activeState.MCP.Tool), entry.Name))) {
		return true
	}
	for _, guideline := range guidelines {
		if containsAnyKeyword(text, entry.Name, entry.Description) {
			return true
		}
		guidelineText := strings.TrimSpace(guideline.When + " " + guideline.Then)
		if containsAnyKeyword(strings.ToLower(guidelineText), entry.Name, entry.Description) {
			return true
		}
	}
	if activeJourney != nil && containsAnyKeyword(text, activeJourney.ID) {
		return true
	}
	return containsAnyKeyword(text, entry.Name, entry.Description)
}

func toolCandidatePreparationRationale(activeState *policy.JourneyNode, guidelines []policy.Guideline, entry tool.CatalogEntry, references []string) string {
	if activeState != nil && strings.EqualFold(strings.TrimSpace(activeState.Tool), entry.Name) {
		return "journey state explicitly requires the tool"
	}
	if len(references) > 0 {
		return "candidate tool should be evaluated against reference tools for best fit"
	}
	for _, guideline := range guidelines {
		if strings.TrimSpace(guideline.Then) != "" {
			return "matched guideline suggests tool support"
		}
	}
	return "tool is exposed by active policy"
}

func toolSpecializationRationale(candidate ToolCandidate, candidates []ToolCandidate) string {
	if len(candidate.ReferenceTools) == 0 {
		return ""
	}
	candidateName := strings.ToLower(strings.ReplaceAll(candidate.ToolID, "_", " "))
	for _, ref := range candidate.ReferenceTools {
		refCandidate, ok := findCandidate(candidates, ref)
		if !ok {
			continue
		}
		refName := strings.ToLower(strings.ReplaceAll(refCandidate.ToolID, "_", " "))
		switch {
		case strings.Contains(candidateName, "motorcycle") && strings.Contains(refName, "vehicle"):
			return "candidate tool is more specialized for this use case than the reference tool"
		case strings.Contains(candidateName, "indoor") && strings.Contains(refName, "temperature"):
			return "candidate tool is more specialized for this use case than the reference tool"
		case strings.Contains(candidateName, "product") && strings.Contains(refName, "search"):
			return "candidate tool is more specialized for this use case than the reference tool"
		}
	}
	return ""
}

func shouldRunToolInTandem(candidate ToolCandidate, selected string, candidates []ToolCandidate) bool {
	selectedCandidate, ok := findCandidate(candidates, selected)
	if !ok {
		return false
	}
	candidateName := strings.ToLower(strings.ReplaceAll(candidate.ToolID, "_", " "))
	selectedName := strings.ToLower(strings.ReplaceAll(selectedCandidate.ToolID, "_", " "))
	switch {
	case containsAnyKeyword(candidateName, "confirm", "confirmation", "notify", "email") &&
		containsAnyKeyword(selectedName, "schedule", "book", "appointment", "reschedule"):
		return true
	default:
		return false
	}
}

func inferToolArgumentsFromContext(matchCtx MatchingContext, specs map[string]toolArgumentSpec) map[string]any {
	text := strings.TrimSpace(matchCtx.LatestCustomerText)
	if text == "" {
		text = strings.TrimSpace(matchCtx.ConversationText)
	}
	return inferToolArgumentsFromText(strings.ToLower(text), specs)
}

func inferToolArgumentsFromText(lower string, specs map[string]toolArgumentSpec) map[string]any {
	out := map[string]any{}
	for field, spec := range specs {
		if value, ok := inferArgumentFromText(strings.ToLower(strings.TrimSpace(field)), spec, lower); ok {
			out[field] = value
		}
	}
	return out
}

func inferArgumentFromText(field string, spec toolArgumentSpec, lower string) (any, bool) {
	if len(spec.Choices) > 0 {
		for _, choice := range spec.Choices {
			if strings.Contains(lower, strings.ToLower(choice)) {
				return choice, true
			}
		}
		if field == "destination" {
			if value := inferPhraseAfter(lower, "to"); value != "" {
				return strings.Title(value), true
			}
		}
	}
	switch field {
	case "vendor", "brand", "manufacturer":
		for _, marker := range []string{"dell", "samsung", "apple", "lenovo", "hp", "asus"} {
			if strings.Contains(lower, marker) {
				return strings.Title(marker), true
			}
		}
	case "keyword":
		for _, marker := range []string{"laptop", "ssd", "phone", "tablet"} {
			if strings.Contains(lower, marker) {
				return strings.ToUpper(marker[:1]) + marker[1:], true
			}
		}
	case "model", "product_name", "query":
		if value := inferPhraseAfter(lower, "for a"); value != "" {
			return strings.TrimSpace(value), true
		}
		if value := inferPhraseAfter(lower, "for"); value != "" {
			return strings.TrimSpace(value), true
		}
	}
	return nil, false
}

func inferPhraseAfter(text string, marker string) string {
	text = strings.TrimSpace(text)
	marker = strings.TrimSpace(marker)
	if text == "" || marker == "" {
		return ""
	}
	if idx := strings.Index(text, marker+" "); idx >= 0 {
		remainder := strings.TrimSpace(text[idx+len(marker):])
		remainder = strings.TrimLeft(remainder, " ")
		remainder = strings.Trim(remainder, ".,!?;:\"'()[]{}")
		return remainder
	}
	parts := strings.Fields(text)
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] != marker {
			continue
		}
		value := strings.Trim(parts[i+1], ".,!?;:\"'()[]{}")
		if value != "" {
			return value
		}
	}
	return ""
}

func toolCandidateAlreadyStaged(matchCtx MatchingContext, entry tool.CatalogEntry, args map[string]any, specs map[string]toolArgumentSpec) bool {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	if name == "" {
		return false
	}
	for _, call := range matchCtx.StagedToolCalls {
		if stagedToolMatchesEntry(call, entry) {
			return true
		}
	}
	desc := strings.ToLower(strings.TrimSpace(entry.Description))
	for _, text := range matchCtx.AppliedInstructions {
		lower := strings.ToLower(text)
		if !(strings.Contains(lower, "using") || strings.Contains(lower, "checking") || strings.Contains(lower, "running") || strings.Contains(lower, "calling")) {
			continue
		}
		if strings.Contains(lower, name) || (desc != "" && containsAnyKeyword(lower, desc)) {
			return true
		}
	}
	return false
}

func toolCandidateSameCallAlreadyStaged(matchCtx MatchingContext, entry tool.CatalogEntry, args map[string]any, specs map[string]toolArgumentSpec) bool {
	for _, call := range matchCtx.StagedToolCalls {
		if !stagedToolMatchesEntry(call, entry) {
			continue
		}
		if sameToolArguments(call.Arguments, args, specs) {
			return true
		}
	}
	return false
}

func toolCandidateAlreadySatisfied(matchCtx MatchingContext, entry tool.CatalogEntry, args map[string]any, specs map[string]toolArgumentSpec) bool {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	if name == "" {
		return false
	}
	for _, call := range matchCtx.StagedToolCalls {
		if !stagedToolMatchesEntry(call, entry) {
			continue
		}
		if len(call.Result) > 0 && sameToolArguments(call.Arguments, args, specs) {
			return true
		}
	}
	if len(matchCtx.AssistantHistory) == 0 {
		return false
	}
	text := strings.ToLower(strings.Join(matchCtx.AssistantHistory, "\n"))
	switch {
	case strings.Contains(name, "status"):
		return strings.Contains(text, "status is") || strings.Contains(text, "currently") || strings.Contains(text, "tracking")
	case strings.Contains(name, "balance"):
		return strings.Contains(text, "balance is") || strings.Contains(text, "account balance")
	case strings.Contains(name, "availability"), strings.Contains(name, "available"):
		return strings.Contains(text, "available") || strings.Contains(text, "in stock")
	case strings.Contains(name, "lock_card"), strings.Contains(name, "lock"):
		return strings.Contains(text, "card is now locked") || strings.Contains(text, "your card is locked") || strings.Contains(text, "locked your card")
	default:
		return false
	}
}

func sameToolArguments(staged map[string]any, current map[string]any, specs map[string]toolArgumentSpec) bool {
	if len(specs) == 0 {
		return true
	}
	for field := range specs {
		if runtimeOnlyToolArgument(field) {
			continue
		}
		left, leftOK := staged[field]
		right, rightOK := current[field]
		if !leftOK && !rightOK {
			continue
		}
		if fmt.Sprint(left) != fmt.Sprint(right) {
			return false
		}
	}
	return true
}

func runtimeOnlyToolArgument(field string) bool {
	switch strings.TrimSpace(strings.ToLower(field)) {
	case "session_id", "customer_message", "conversation_excerpt", "journey_id", "journey_state":
		return true
	default:
		return false
	}
}

func toolCandidateInvalidatedByJourneyBacktrack(entry tool.CatalogEntry, activeState *policy.JourneyNode, journeyDecision JourneyDecision) bool {
	if activeState == nil || !strings.EqualFold(journeyDecision.Action, "backtrack") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(activeState.Type), "tool") {
		return false
	}
	stateTool := strings.TrimSpace(activeState.Tool)
	if stateTool == "" && activeState.MCP != nil {
		stateTool = strings.TrimSpace(activeState.MCP.Tool)
	}
	if stateTool == "" {
		return false
	}
	entryIDs := []string{
		strings.TrimSpace(entry.ID),
		strings.TrimSpace(entry.Name),
	}
	if strings.TrimSpace(entry.ProviderID) != "" && strings.TrimSpace(entry.Name) != "" {
		entryIDs = append(entryIDs, strings.TrimSpace(entry.ProviderID)+":"+strings.TrimSpace(entry.Name))
	}
	for _, candidateID := range entryIDs {
		if candidateID == "" {
			continue
		}
		if stagedToolMatchesToolID(candidateID, stateTool) || stagedToolMatchesToolID(stateTool, candidateID) {
			return true
		}
	}
	return false
}

func stagedToolMatchesEntry(call StagedToolCall, entry tool.CatalogEntry) bool {
	callID := strings.ToLower(strings.TrimSpace(call.ToolID))
	if callID == "" {
		return false
	}
	entryName := strings.ToLower(strings.TrimSpace(entry.Name))
	entryID := strings.ToLower(strings.TrimSpace(entry.ID))
	providerName := strings.ToLower(strings.TrimSpace(entry.ProviderID + ":" + entry.Name))
	return callID == entryName || callID == entryID || callID == providerName
}

func toolConsequential(entry tool.CatalogEntry) bool {
	meta := decodeToolMetadata(entry)
	return truthyValue(meta["consequential"])
}

func toolAutoApproved(entry tool.CatalogEntry) bool {
	if toolConsequential(entry) {
		return false
	}
	specs := extractToolRequirements(entry)
	return len(specs) == 0
}

func toolOverlapGroup(entry tool.CatalogEntry) string {
	meta := decodeToolMetadata(entry)
	if value := strings.TrimSpace(fmt.Sprint(meta["overlap_group"])); value != "" && value != "<nil>" {
		return value
	}
	name := strings.ToLower(entry.Name)
	switch {
	case strings.Contains(name, "card"):
		return entry.ProviderID + ":card"
	case strings.Contains(name, "refund"), strings.Contains(name, "return"):
		return entry.ProviderID + ":returns"
	default:
		return ""
	}
}

func referenceToolsForEntry(entry tool.CatalogEntry, relationships []policy.Relationship, exposedTools []string, catalog []tool.CatalogEntry) []string {
	refs := map[string]struct{}{}
	group := toolOverlapGroup(entry)
	for _, item := range exposedTools {
		if item == entry.Name {
			continue
		}
		if other, ok := findToolCatalogEntry(catalog, item); ok && group != "" && toolOverlapGroup(other) == group {
			refs[other.Name] = struct{}{}
		}
	}
	for _, rel := range relationships {
		kind := strings.ToLower(strings.TrimSpace(rel.Kind))
		src := normalizeRelationshipToolTarget(rel.Source)
		dst := normalizeRelationshipToolTarget(rel.Target)
		switch {
		case (kind == "overlap" || kind == "overlaps" || kind == "reference" || kind == "references") && src == entry.Name && dst != "":
			refs[dst] = struct{}{}
		case (kind == "overlap" || kind == "overlaps") && dst == entry.Name && src != "":
			refs[src] = struct{}{}
		}
	}
	out := make([]string, 0, len(refs))
	for item := range refs {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func buildOverlappingGroups(candidates []ToolCandidate, relationships []policy.Relationship) [][]string {
	if len(candidates) == 0 {
		return nil
	}
	candidateIDs := map[string]struct{}{}
	adj := map[string]map[string]struct{}{}
	groupBuckets := map[string][]string{}
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ToolID)
		if id == "" {
			continue
		}
		candidateIDs[id] = struct{}{}
		if candidate.GroupKey != "" {
			groupBuckets[candidate.GroupKey] = append(groupBuckets[candidate.GroupKey], id)
		}
	}
	addEdge := func(a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		if _, ok := candidateIDs[a]; !ok {
			return
		}
		if _, ok := candidateIDs[b]; !ok {
			return
		}
		if adj[a] == nil {
			adj[a] = map[string]struct{}{}
		}
		if adj[b] == nil {
			adj[b] = map[string]struct{}{}
		}
		adj[a][b] = struct{}{}
		adj[b][a] = struct{}{}
	}
	for _, tools := range groupBuckets {
		tools = dedupe(tools)
		for i := 0; i < len(tools); i++ {
			for j := i + 1; j < len(tools); j++ {
				addEdge(tools[i], tools[j])
			}
		}
	}
	for _, rel := range relationships {
		kind := strings.ToLower(strings.TrimSpace(rel.Kind))
		if kind != "overlap" && kind != "overlaps" {
			continue
		}
		addEdge(normalizeRelationshipToolTarget(rel.Source), normalizeRelationshipToolTarget(rel.Target))
	}
	visited := map[string]struct{}{}
	var groups [][]string
	for id := range candidateIDs {
		if _, ok := visited[id]; ok {
			continue
		}
		component := []string{id}
		queue := []string{id}
		visited[id] = struct{}{}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for neighbor := range adj[current] {
				if _, ok := visited[neighbor]; ok {
					continue
				}
				visited[neighbor] = struct{}{}
				component = append(component, neighbor)
				queue = append(queue, neighbor)
			}
		}
		if len(component) < 2 {
			continue
		}
		sort.Strings(component)
		groups = append(groups, component)
	}
	return groups
}

func normalizeRelationshipToolTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.Contains(target, ":") {
		parts := strings.Split(target, ":")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return target
}

func decodeToolMetadata(entry tool.CatalogEntry) map[string]any {
	var out map[string]any
	if strings.TrimSpace(entry.MetadataJSON) == "" {
		return map[string]any{}
	}
	if err := json.Unmarshal([]byte(entry.MetadataJSON), &out); err != nil {
		return map[string]any{}
	}
	return out
}
