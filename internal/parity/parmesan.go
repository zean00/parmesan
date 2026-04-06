package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func RunParmesan(ctx context.Context, s Scenario) (NormalizedResult, error) {
	bundle := buildBundleForScenario(s)
	journeyInstances := buildJourneyInstancesForScenario(s, bundle)
	catalog := buildCatalogForScenario(s)
	events := buildEventsForScenario(s)
	var router *model.Router
	if hasProviderEnv() {
		router = model.NewRouter(config.Load("parity").Provider)
	}

	view, err := policyruntime.ResolveWithRouter(ctx, router, events, []policy.Bundle{bundle}, journeyInstances, catalog)
	if err != nil {
		return NormalizedResult{}, err
	}

	toolOutput, toolCalls := runFixtureTool(view, catalog)
	response := renderParityResponse(view, toolOutput)
	verification := policyruntime.VerifyDraft(view, response, toolOutput)
	if (verification.Status == "revise" || verification.Status == "block") && strings.TrimSpace(verification.Replacement) != "" {
		response = verification.Replacement
	}

	noMatch := false
	if strings.EqualFold(view.CompositionMode, "strict") && strings.TrimSpace(response) == strings.TrimSpace(strictNoMatchForParity(view.NoMatch)) {
		noMatch = true
	}

	out := normalizeParmesan(view, catalog, response, toolCalls, noMatch, verification.Status)
	if strings.TrimSpace(s.PriorState.ActiveJourney) == "" && out.ActiveJourney != "" && out.ActiveJourneyNode != "" {
		out.JourneyDecision = "start"
		out.NextJourneyNode = out.ActiveJourneyNode
	}
	return out, nil
}

func hasProviderEnv() bool {
	return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" || strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func normalizeParmesan(view policyruntime.EngineResult, catalog []tool.CatalogEntry, response string, toolCalls []ToolCall, noMatch bool, verification string) NormalizedResult {
	journeyDecision := view.JourneyProgressStage.Decision
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	matchedObservations := view.ObservationStage.Observations
	exposed := normalizeToolNames(view.ToolExposureStage.ExposedTools, catalog)
	selected := normalizeSelectedTool(toolDecision.SelectedTool, toolCalls, catalog)
	effectiveJourneyState := effectiveJourneyState(view)
	analysis := view.ResponseAnalysisStage.Analysis
	out := NormalizedResult{
		MatchedObservations:              idsFromObservations(matchedObservations),
		MatchedGuidelines:                normalizeMatchedGuidelines(view),
		SuppressedGuidelines:             normalizedSuppressedGuidelines(view.SuppressedGuidelines, view.ResolutionRecords, view.MatchFinalizeStage.MatchedGuidelines),
		SuppressionReasons:               suppressionReasons(view.SuppressedGuidelines),
		ResolutionRecords:                normalizeResolutionRecords(view.ResolutionRecords),
		ProjectedFollowUps:               projectedFollowUps(view.ProjectedNodes),
		LegalFollowUps:                   legalFollowUps(view.ProjectedNodes),
		ExposedTools:                     exposed,
		ToolCandidates:                   normalizeToolCandidates(toolPlan.Candidates, catalog),
		ToolCandidateStates:              normalizeToolCandidateStates(toolPlan.Candidates, catalog),
		ToolCandidateRejectedBy:          normalizeToolCandidateRejectedBy(toolPlan.Candidates, catalog),
		ToolCandidateReasons:             normalizeToolCandidateReasons(toolPlan.Candidates, catalog),
		ToolCandidateTandemWith:          normalizeToolCandidateTandemWith(toolPlan.Candidates, catalog),
		OverlappingToolGroups:            normalizeToolGroups(toolPlan.OverlappingGroups, catalog),
		SelectedTool:                     selected,
		SelectedTools:                    normalizeSelectedTools(toolPlan.SelectedTools, toolCalls, catalog),
		ToolCallTools:                    normalizeToolCallTools(toolCalls),
		ResponseMode:                     normalizeMode(view.CompositionMode),
		NoMatch:                          noMatch,
		ResponseText:                     strings.TrimSpace(response),
		ToolCalls:                        toolCalls,
		VerificationOutcome:              verification,
		ResponseAnalysisStillRequired:    responseAnalysisStillRequired(analysis),
		ResponseAnalysisAlreadySatisfied: responseAnalysisAlreadySatisfied(analysis),
		ResponseAnalysisPartiallyApplied: responseAnalysisPartiallyApplied(analysis),
		ResponseAnalysisToolSatisfied:    responseAnalysisToolSatisfied(analysis),
		ResponseAnalysisSources:          responseAnalysisSources(analysis),
	}
	if view.ActiveJourney != nil {
		out.ActiveJourney = view.ActiveJourney.ID
	}
	if effectiveJourneyState != nil {
		out.ActiveJourneyNode = effectiveJourneyState.ID
	}
	if journeyDecision.Action != "" {
		out.JourneyDecision = journeyDecision.Action
		out.NextJourneyNode = firstNonEmpty(journeyDecision.NextState, journeyDecision.BacktrackTo)
	} else {
		out.JourneyDecision = "ignore"
	}
	if out.NextJourneyNode == "" && out.JourneyDecision == "continue" && out.ActiveJourneyNode != "" {
		out.NextJourneyNode = out.ActiveJourneyNode
	}
	if toolDecision.CanRun || len(toolDecision.MissingArguments) > 0 || len(toolDecision.InvalidArguments) > 0 {
		canRun := toolDecision.CanRun
		out.ToolCanRun = &canRun
	}
	if strings.TrimSpace(analysis.RecommendedTemplate) != "" {
		out.SelectedTemplate = strings.TrimSpace(analysis.RecommendedTemplate)
	}
	return out
}

func normalizeMatchedGuidelines(view policyruntime.EngineResult) []string {
	ids := idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines)
	if view.ActiveJourney == nil {
		return ids
	}
	journeyEntityID := "journey:" + view.ActiveJourney.ID
	journeyActive := false
	for _, item := range view.ResolutionRecords {
		if item.EntityID == journeyEntityID && item.Kind == policyruntime.ResolutionNone {
			journeyActive = true
			break
		}
	}
	var out []string
	for _, id := range ids {
		if strings.HasPrefix(id, "journey_node:"+view.ActiveJourney.ID+":") {
			continue
		}
		out = append(out, id)
	}
	if journeyActive {
		out = append(out, journeyEntityID)
	}
	return dedupeAndSort(out)
}

func effectiveJourneyState(view policyruntime.EngineResult) *policy.JourneyNode {
	return view.ActiveJourneyState
}

func normalizedSuppressedGuidelines(items []policyruntime.SuppressedGuideline, resolutions []policyruntime.ResolutionRecord, matched []policy.Guideline) []string {
	out := idsFromSuppressed(items)
	finalKinds := map[string]policyruntime.ResolutionKind{}
	for _, item := range resolutions {
		finalKinds[strings.TrimSpace(item.EntityID)] = item.Kind
	}
	activeJourneys := map[string]struct{}{}
	activeEntities := map[string]struct{}{}
	for _, item := range matched {
		activeEntities[strings.TrimSpace(item.ID)] = struct{}{}
	}
	for entityID, kind := range finalKinds {
		if kind != policyruntime.ResolutionNone {
			continue
		}
		activeEntities[entityID] = struct{}{}
		if strings.HasPrefix(entityID, "journey:") {
			activeJourneys[strings.TrimSpace(strings.TrimPrefix(entityID, "journey:"))] = struct{}{}
		}
	}
	filtered := out[:0]
	for _, id := range out {
		if _, ok := activeEntities[strings.TrimSpace(id)]; ok {
			continue
		}
		if strings.HasPrefix(id, "journey_node:") {
			parts := strings.SplitN(strings.TrimSpace(id), ":", 3)
			if len(parts) >= 3 {
				if _, ok := activeJourneys[parts[1]]; ok {
					continue
				}
			}
		}
		filtered = append(filtered, id)
	}
	out = filtered
	for _, item := range resolutions {
		switch item.Kind {
		case policyruntime.ResolutionDeprioritized, policyruntime.ResolutionUnmetDependency, policyruntime.ResolutionUnmetDependencyAny:
			if _, ok := activeEntities[strings.TrimSpace(item.EntityID)]; ok {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(item.EntityID), "journey:") && strings.Contains(strings.ToLower(item.Details.Description), "higher numerical priority entity") {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(item.EntityID), "journey_node:") {
				parts := strings.SplitN(strings.TrimSpace(item.EntityID), ":", 3)
				if len(parts) >= 3 {
					if _, ok := activeJourneys[parts[1]]; ok {
						continue
					}
				}
			}
			out = append(out, strings.TrimSpace(item.EntityID))
		}
	}
	return dedupeAndSort(out)
}

func buildEventsForScenario(s Scenario) []session.Event {
	sessionID := scenarioSessionID(s)
	now := time.Now().UTC()
	events := make([]session.Event, 0, len(s.Transcript)+len(s.PolicySetup.StagedToolCalls))
	for i, item := range s.Transcript {
		source := "customer"
		if item.Role == "agent" {
			source = "ai_agent"
		}
		events = append(events, session.Event{
			ID:        fmt.Sprintf("%s_evt_%d", s.ID, i+1),
			SessionID: sessionID,
			Source:    source,
			Kind:      "message",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Content: []session.ContentPart{
				{Type: "text", Text: item.Text},
			},
		})
	}
	for i, call := range s.PolicySetup.StagedToolCalls {
		meta := map[string]any{
			"tool_id": call.ToolID,
		}
		if len(call.Arguments) > 0 {
			meta["arguments"] = call.Arguments
		}
		if len(call.Result) > 0 {
			meta["result"] = call.Result
		}
		if strings.TrimSpace(call.DocumentID) != "" {
			meta["document_id"] = strings.TrimSpace(call.DocumentID)
		}
		if strings.TrimSpace(call.ModulePath) != "" {
			meta["module_path"] = strings.TrimSpace(call.ModulePath)
		}
		events = append(events, session.Event{
			ID:        fmt.Sprintf("%s_tool_%d", s.ID, i+1),
			SessionID: sessionID,
			Source:    "ai_agent",
			Kind:      "tool",
			CreatedAt: now.Add(time.Duration(len(s.Transcript)+i) * time.Second),
			Content: []session.ContentPart{
				{Type: "tool_call", Meta: meta},
			},
		})
	}
	return events
}

func buildJourneyInstancesForScenario(s Scenario, bundle policy.Bundle) []journey.Instance {
	if strings.TrimSpace(s.PriorState.ActiveJourney) == "" {
		return nil
	}
	for _, item := range bundle.Journeys {
		if item.ID != s.PriorState.ActiveJourney {
			continue
		}
		stateID := ""
		if len(s.PriorState.JourneyPath) > 0 {
			stateID = s.PriorState.JourneyPath[len(s.PriorState.JourneyPath)-1]
		}
		path := append([]string(nil), s.PriorState.JourneyPath...)
		return []journey.Instance{{
			ID:        "journey_" + s.ID,
			SessionID: scenarioSessionID(s),
			JourneyID: item.ID,
			StateID:   stateID,
			Path:      path,
			Status:    journey.StatusActive,
			UpdatedAt: time.Now().UTC(),
		}}
	}
	return nil
}

func buildCatalogForScenario(s Scenario) []tool.CatalogEntry {
	var out []tool.CatalogEntry
	for _, item := range s.PolicySetup.Tools {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		providerID, name := splitToolID(id)
		metadata := map[string]any{}
		if item.Consequential {
			metadata["consequential"] = true
		}
		if strings.TrimSpace(item.OverlapGroup) != "" {
			metadata["overlap_group"] = strings.TrimSpace(item.OverlapGroup)
		}
		if strings.TrimSpace(item.ModulePath) != "" {
			metadata["module_path"] = strings.TrimSpace(item.ModulePath)
		}
		if strings.TrimSpace(item.DocumentID) != "" {
			metadata["document_id"] = strings.TrimSpace(item.DocumentID)
		}
		metadataJSON := ""
		if len(metadata) > 0 {
			if raw, err := json.Marshal(metadata); err == nil {
				metadataJSON = string(raw)
			}
		}
		schema := builtInToolSchema(id)
		if len(item.Schema) > 0 {
			if raw, err := json.Marshal(item.Schema); err == nil {
				schema = string(raw)
			}
		}
		out = append(out, tool.CatalogEntry{
			ID:              id,
			ProviderID:      providerID,
			Name:            name,
			Description:     firstNonEmpty(strings.TrimSpace(item.Description), name),
			RuntimeProtocol: "mcp",
			Schema:          schema,
			MetadataJSON:    metadataJSON,
			ImportedAt:      time.Now().UTC(),
		})
	}
	return out
}

func buildBundleForScenario(s Scenario) policy.Bundle {
	mode := normalizeMode(s.Mode)
	if mode == "" {
		mode = "guided"
	}
	bundle := policy.Bundle{
		ID:              "bundle_" + s.ID,
		Version:         "parity-v1",
		ImportedAt:      time.Now().UTC(),
		CompositionMode: mode,
		NoMatch:         defaultNoMatchForScenario(s),
	}
	for _, g := range s.PolicySetup.ExtraGuidelines {
		switch strings.ToLower(strings.TrimSpace(g.Kind)) {
		case "observation":
			bundle.Observations = append(bundle.Observations, policy.Observation{
				ID:   g.ID,
				When: g.Condition,
			})
		default:
			scope := ""
			if g.CustomerDependent {
				scope = "customer"
			}
			bundle.Guidelines = append(bundle.Guidelines, policy.Guideline{
				ID:          g.ID,
				When:        g.Condition,
				Then:        g.Action,
				Scope:       scope,
				Matcher:     g.Matcher,
				Criticality: g.Criticality,
				Tags:        append([]string(nil), g.Tags...),
				Track:       g.Track == nil || *g.Track,
				Continuous:  g.Continuous,
				Priority:    g.Priority,
			})
		}
	}
	for _, item := range scenarioJourneys(s) {
		bundle.Journeys = append(bundle.Journeys, item)
	}
	for _, item := range s.PolicySetup.Relationships {
		bundle.Relationships = append(bundle.Relationships, policy.Relationship{
			Source: item.Source,
			Kind:   item.Kind,
			Target: item.Target,
		})
	}
	for i, text := range s.PolicySetup.CannedResponses {
		bundle.Templates = append(bundle.Templates, policy.Template{
			ID:   fmt.Sprintf("template_%d", i+1),
			Mode: normalizeMode(s.Mode),
			Text: text,
		})
	}
	for _, assoc := range s.PolicySetup.Associations {
		bundle.GuidelineToolAssociations = append(bundle.GuidelineToolAssociations, policy.GuidelineToolAssociation{
			GuidelineID: assoc.Guideline,
			ToolID:      assoc.Tool,
		})
		bundle.ToolPolicies = append(bundle.ToolPolicies, policy.ToolPolicy{
			ID:       "allow_" + assoc.Tool,
			ToolIDs:  []string{assoc.Tool},
			Exposure: "allow",
		})
	}
	return bundle
}

func scenarioJourneys(s Scenario) []policy.Journey {
	var out []policy.Journey
	for _, item := range s.PolicySetup.Journeys {
		out = append(out, fixtureJourneyToPolicy(item))
	}
	return out
}

func fixtureJourneyToPolicy(item FixtureJourney) policy.Journey {
	out := policy.Journey{
		ID:              item.ID,
		When:            append([]string(nil), item.When...),
		RootID:          strings.TrimSpace(item.RootID),
		Priority:        item.Priority,
		Tags:            append([]string(nil), item.Tags...),
		Labels:          append([]string(nil), item.Labels...),
		Metadata:        cloneMap(item.Metadata),
		CompositionMode: item.CompositionMode,
	}
	for _, state := range item.States {
		out.States = append(out.States, policy.JourneyNode{
			ID:              state.ID,
			Type:            state.Type,
			Instruction:     state.Instruction,
			Description:     state.Description,
			Tool:            state.Tool,
			When:            append([]string(nil), state.When...),
			Next:            append([]string(nil), state.Next...),
			Mode:            state.Mode,
			Kind:            state.Kind,
			Labels:          append([]string(nil), state.Labels...),
			Metadata:        cloneMap(state.Metadata),
			CompositionMode: state.CompositionMode,
			Priority:        state.Priority,
		})
	}
	for _, edge := range item.Edges {
		out.Edges = append(out.Edges, policy.JourneyEdge{
			ID:        edge.ID,
			Source:    edge.Source,
			Target:    edge.Target,
			Condition: edge.Condition,
			Metadata:  cloneMap(edge.Metadata),
		})
	}
	if strings.TrimSpace(out.RootID) == "" && len(out.States) > 0 {
		out.RootID = strings.TrimSpace(out.States[0].ID)
	}
	if len(out.Edges) == 0 {
		for _, state := range out.States {
			for _, nextID := range state.Next {
				out.Edges = append(out.Edges, policy.JourneyEdge{
					Source: strings.TrimSpace(state.ID),
					Target: strings.TrimSpace(nextID),
				})
			}
		}
	}
	if strings.TrimSpace(out.RootID) != "" {
		hasIncomingRootEdge := false
		hasOutgoingRootEdge := false
		for _, edge := range out.Edges {
			source := strings.TrimSpace(edge.Source)
			target := strings.TrimSpace(edge.Target)
			if (source == "__journey_root__" || source == out.RootID) && target == out.RootID {
				hasIncomingRootEdge = true
			}
			if source == out.RootID {
				hasOutgoingRootEdge = true
			}
		}
		if hasOutgoingRootEdge && !hasIncomingRootEdge {
			out.Edges = append([]policy.JourneyEdge{{
				Source: "__journey_root__",
				Target: out.RootID,
			}}, out.Edges...)
		}
	}
	return out
}

func defaultNoMatchForScenario(s Scenario) string {
	for _, item := range s.PolicySetup.CannedResponses {
		if containsFold(item, "do not know") || containsFold(item, "no information") {
			return item
		}
	}
	if strings.EqualFold(s.Mode, "strict") {
		return "I'm sorry but I have no information about that"
	}
	return ""
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "strict", "canned_strict":
		return "strict"
	case "", "guided", "canned_fluid", "canned_composited", "fluid":
		return "guided"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func runFixtureTool(view policyruntime.EngineResult, catalog []tool.CatalogEntry) (map[string]any, []ToolCall) {
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if len(toolPlan.Calls) > 0 {
		allowed := runnablePlannedTools(view, catalog)
		var calls []ToolCall
		outputs := map[string]any{}
		for _, call := range toolPlan.Calls {
			if len(allowed) > 0 {
				if _, ok := allowed[fullToolID(call.ToolID, catalog)]; !ok {
					continue
				}
			}
			candidate, ok := findParityCandidate(toolPlan.Candidates, call.ToolID)
			if ok && (candidate.AlreadySatisfied || candidate.AlreadyStaged || len(candidate.MissingIssues) > 0 || len(candidate.InvalidIssues) > 0) {
				continue
			}
			fullID := fullToolID(call.ToolID, catalog)
			documentID, modulePath := toolCallMetadata(fullID, catalog)
			output := builtInToolOutput(call.ToolID, call.Arguments)
			if output == nil {
				output = builtInToolOutput(fullID, call.Arguments)
			}
			calls = append(calls, ToolCall{
				ToolID:     fullID,
				Arguments:  cloneMap(call.Arguments),
				DocumentID: documentID,
				ModulePath: modulePath,
			})
			if output != nil {
				outputs[fullID] = output
			}
		}
		if len(calls) == 0 {
			return nil, nil
		}
		if len(outputs) == 1 {
			for _, value := range outputs {
				return value.(map[string]any), calls
			}
		}
		if len(outputs) > 0 {
			return map[string]any{"tools": outputs}, calls
		}
		return nil, calls
	}
	selectedTools := append([]string(nil), toolPlan.SelectedTools...)
	if len(selectedTools) == 0 && strings.TrimSpace(toolDecision.SelectedTool) != "" && toolDecision.CanRun {
		selectedTools = append(selectedTools, toolDecision.SelectedTool)
	}
	if len(selectedTools) == 0 {
		return nil, nil
	}
	var calls []ToolCall
	outputs := map[string]any{}
	for _, name := range selectedTools {
		candidate, ok := findParityCandidate(toolPlan.Candidates, name)
		if !ok {
			if strings.TrimSpace(toolDecision.SelectedTool) != strings.TrimSpace(name) || !toolDecision.CanRun {
				continue
			}
			candidate = policyruntime.ToolCandidate{
				ToolID:    name,
				Arguments: cloneMap(toolDecision.Arguments),
			}
		}
		if candidate.AlreadySatisfied || candidate.AlreadyStaged || len(candidate.MissingIssues) > 0 || len(candidate.InvalidIssues) > 0 {
			continue
		}
		fullID := fullToolID(name, catalog)
		documentID, modulePath := toolCallMetadata(fullID, catalog)
		output := builtInToolOutput(name, candidate.Arguments)
		if output == nil {
			output = builtInToolOutput(fullID, candidate.Arguments)
		}
		calls = append(calls, ToolCall{
			ToolID:     fullID,
			Arguments:  cloneMap(candidate.Arguments),
			DocumentID: documentID,
			ModulePath: modulePath,
		})
		if output != nil {
			outputs[fullID] = output
		}
	}
	if len(calls) == 0 {
		return nil, nil
	}
	if len(outputs) == 1 {
		for _, value := range outputs {
			return value.(map[string]any), calls
		}
	}
	if len(outputs) > 0 {
		return map[string]any{"tools": outputs}, calls
	}
	return nil, calls
}

func runnablePlannedTools(view policyruntime.EngineResult, catalog []tool.CatalogEntry) map[string]struct{} {
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	out := map[string]struct{}{}
	for _, name := range toolPlan.SelectedTools {
		candidate, ok := findParityCandidate(toolPlan.Candidates, name)
		if ok && (candidate.AlreadySatisfied || candidate.AlreadyStaged || len(candidate.MissingIssues) > 0 || len(candidate.InvalidIssues) > 0) {
			continue
		}
		out[fullToolID(name, catalog)] = struct{}{}
	}
	if strings.TrimSpace(toolDecision.SelectedTool) != "" && toolDecision.CanRun {
		out[fullToolID(toolDecision.SelectedTool, catalog)] = struct{}{}
	}
	return out
}

func toolCallMetadata(fullID string, catalog []tool.CatalogEntry) (string, string) {
	for _, entry := range catalog {
		if entry.ID != fullID {
			continue
		}
		meta := map[string]any{}
		if strings.TrimSpace(entry.MetadataJSON) != "" {
			_ = json.Unmarshal([]byte(entry.MetadataJSON), &meta)
		}
		documentID, _ := meta["document_id"].(string)
		modulePath, _ := meta["module_path"].(string)
		return strings.TrimSpace(documentID), strings.TrimSpace(modulePath)
	}
	return "", ""
}

func normalizeToolNames(names []string, catalog []tool.CatalogEntry) []string {
	if len(names) == 0 {
		return nil
	}
	mapped := make([]string, 0, len(names))
	for _, name := range names {
		mapped = append(mapped, fullToolID(name, catalog))
	}
	return dedupeAndSort(mapped)
}

func normalizeToolCandidates(candidates []policyruntime.ToolCandidate, catalog []tool.CatalogEntry) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, fullToolID(candidate.ToolID, catalog))
	}
	return dedupeAndSort(out)
}

func normalizeToolCandidateStates(candidates []policyruntime.ToolCandidate, catalog []tool.CatalogEntry) map[string]string {
	if len(candidates) == 0 {
		return nil
	}
	out := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		out[fullToolID(candidate.ToolID, catalog)] = strings.TrimSpace(candidate.DecisionState)
	}
	return out
}

func normalizeToolCandidateRejectedBy(candidates []policyruntime.ToolCandidate, catalog []tool.CatalogEntry) map[string]string {
	if len(candidates) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.RejectedBy) == "" {
			continue
		}
		out[fullToolID(candidate.ToolID, catalog)] = fullToolID(candidate.RejectedBy, catalog)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeToolCandidateReasons(candidates []policyruntime.ToolCandidate, catalog []tool.CatalogEntry) map[string]string {
	if len(candidates) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, candidate := range candidates {
		reason := strings.TrimSpace(firstNonEmpty(candidate.SelectionRationale, candidate.PreparationRationale, candidate.Rationale))
		if reason == "" {
			continue
		}
		out[fullToolID(candidate.ToolID, catalog)] = reason
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeToolCandidateTandemWith(candidates []policyruntime.ToolCandidate, catalog []tool.CatalogEntry) map[string][]string {
	if len(candidates) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, candidate := range candidates {
		if len(candidate.RunInTandemWith) == 0 {
			continue
		}
		items := make([]string, 0, len(candidate.RunInTandemWith))
		for _, item := range candidate.RunInTandemWith {
			items = append(items, fullToolID(item, catalog))
		}
		out[fullToolID(candidate.ToolID, catalog)] = dedupeAndSort(items)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeToolGroups(groups [][]string, catalog []tool.CatalogEntry) [][]string {
	if len(groups) == 0 {
		return nil
	}
	out := make([][]string, 0, len(groups))
	for _, group := range groups {
		items := make([]string, 0, len(group))
		for _, item := range group {
			items = append(items, fullToolID(item, catalog))
		}
		out = append(out, dedupeAndSort(items))
	}
	return out
}

func normalizeResolutionRecords(items []policyruntime.ResolutionRecord) []NormalizedResolution {
	out := make([]NormalizedResolution, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizedResolution{
			EntityID: normalizeProjectedID(strings.TrimSpace(item.EntityID)),
			Kind:     strings.TrimSpace(string(item.Kind)),
		})
	}
	return out
}

func projectedFollowUps(items []policyruntime.ProjectedJourneyNode) map[string][]string {
	out := map[string][]string{}
	for _, item := range items {
		if len(item.FollowUps) == 0 {
			continue
		}
		key := normalizeProjectedID(item.ID)
		values := make([]string, 0, len(item.FollowUps))
		for _, followUp := range item.FollowUps {
			values = append(values, normalizeProjectedID(followUp))
		}
		out[key] = dedupeAndSort(values)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func legalFollowUps(items []policyruntime.ProjectedJourneyNode) map[string][]string {
	out := map[string][]string{}
	for _, item := range items {
		if len(item.LegalFollowUps) == 0 {
			continue
		}
		key := normalizeProjectedID(item.ID)
		values := make([]string, 0, len(item.LegalFollowUps))
		for _, followUp := range item.LegalFollowUps {
			values = append(values, normalizeProjectedID(followUp))
		}
		out[key] = dedupeAndSort(values)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeProjectedID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	reIndexedSuffix := regexp.MustCompile(`#\d+$`)
	id = reIndexedSuffix.ReplaceAllString(id, "")
	parts := strings.Split(id, ":")
	if len(parts) >= 4 && parts[0] == "journey_node" {
		journeyID := parts[1]
		stateID := parts[2]
		edgePart := strings.Join(parts[3:], ":")
		rootEdgePrefix := journeyID + ":__journey_root__->" + stateID
		if edgePart == rootEdgePrefix {
			return strings.Join(parts[:3], ":")
		}
	}
	return id
}

func responseAnalysisStillRequired(analysis policyruntime.ResponseAnalysis) []string {
	var out []string
	for _, item := range analysis.AnalyzedGuidelines {
		if item.RequiresResponse || item.RequiresTemplate {
			out = append(out, item.ID)
		}
	}
	return dedupeAndSort(out)
}

func responseAnalysisAlreadySatisfied(analysis policyruntime.ResponseAnalysis) []string {
	var out []string
	for _, item := range analysis.AnalyzedGuidelines {
		if item.AlreadySatisfied {
			out = append(out, item.ID)
		}
	}
	return dedupeAndSort(out)
}

func responseAnalysisPartiallyApplied(analysis policyruntime.ResponseAnalysis) []string {
	var out []string
	for _, item := range analysis.AnalyzedGuidelines {
		if strings.EqualFold(item.AppliedDegree, "partial") {
			out = append(out, item.ID)
		}
	}
	return dedupeAndSort(out)
}

func responseAnalysisToolSatisfied(analysis policyruntime.ResponseAnalysis) []string {
	var out []string
	for _, item := range analysis.AnalyzedGuidelines {
		if item.SatisfiedByToolEvent {
			out = append(out, item.ID)
		}
	}
	return dedupeAndSort(out)
}

func responseAnalysisSources(analysis policyruntime.ResponseAnalysis) map[string]string {
	out := map[string]string{}
	for _, item := range analysis.AnalyzedGuidelines {
		if strings.TrimSpace(item.SatisfactionSource) == "" {
			continue
		}
		out[item.ID] = strings.TrimSpace(item.SatisfactionSource)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSelectedTool(name string, calls []ToolCall, catalog []tool.CatalogEntry) string {
	if len(calls) > 0 && strings.TrimSpace(calls[0].ToolID) != "" {
		return calls[0].ToolID
	}
	return fullToolID(name, catalog)
}

func normalizeSelectedTools(names []string, calls []ToolCall, catalog []tool.CatalogEntry) []string {
	if len(calls) > 0 {
		out := make([]string, 0, len(calls))
		for _, call := range calls {
			out = append(out, call.ToolID)
		}
		return dedupeAndSort(out)
	}
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, fullToolID(name, catalog))
	}
	return dedupeAndSort(out)
}

func normalizeToolCallTools(calls []ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.ToolID)
	}
	return out
}

func findParityCandidate(candidates []policyruntime.ToolCandidate, toolID string) (policyruntime.ToolCandidate, bool) {
	toolID = strings.TrimSpace(toolID)
	for _, item := range candidates {
		if strings.TrimSpace(item.ToolID) == toolID {
			return item, true
		}
	}
	return policyruntime.ToolCandidate{}, false
}

func fullToolID(name string, catalog []tool.CatalogEntry) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.Contains(name, ":") {
		return name
	}
	for _, entry := range catalog {
		if entry.Name == name || entry.ID == name || entry.ProviderID+"."+entry.Name == name {
			return entry.ID
		}
	}
	return name
}

func renderParityResponse(view policyruntime.EngineResult, toolOutput map[string]any) string {
	analysis := view.ResponseAnalysisStage.Analysis
	if strings.TrimSpace(view.DisambiguationPrompt) != "" {
		return strings.TrimSpace(view.DisambiguationPrompt)
	}
	if rendered := renderParityTemplateText(analysis.RecommendedTemplate, toolOutput); rendered != "" {
		return rendered
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		if rendered := renderParityTemplate(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); rendered != "" {
			return rendered
		}
		return strictNoMatchForParity(view.NoMatch)
	}
	if rendered := renderParityTemplate(view.ResponseAnalysisStage.CandidateTemplates, toolOutput); rendered != "" {
		return rendered
	}
	if rendered := renderParityToolOutput(toolOutput); rendered != "" {
		return rendered
	}
	guidelines := view.MatchFinalizeStage.MatchedGuidelines
	if len(guidelines) > 0 {
		parts := make([]string, 0, len(guidelines))
		for _, item := range guidelines {
			if strings.TrimSpace(item.Then) != "" {
				parts = append(parts, strings.TrimSpace(item.Then))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if state := effectiveJourneyState(view); state != nil && strings.TrimSpace(state.Instruction) != "" {
		return strings.TrimSpace(state.Instruction)
	}
	return ""
}

func renderParityTemplate(templates []policy.Template, toolOutput map[string]any) string {
	if len(templates) == 0 {
		return ""
	}
	return renderParityTemplateText(templates[0].Text, toolOutput)
}

func renderParityTemplateText(text string, toolOutput map[string]any) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for key, value := range toolOutput {
		text = strings.ReplaceAll(text, "{{"+key+"}}", fmt.Sprint(value))
	}
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return ""
	}
	return strings.TrimSpace(text)
}

func strictNoMatchForParity(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}
	return "Not sure I understand. Could you please say that another way?"
}

func renderParityToolOutput(toolOutput map[string]any) string {
	if len(toolOutput) == 0 {
		return ""
	}
	if value, ok := toolOutput["qualification_info"]; ok {
		return fmt.Sprintf("In terms of years of experience, the requirement is %v.", value)
	}
	if value, ok := toolOutput["available_toppings"]; ok {
		if items, ok := value.([]string); ok && len(items) > 0 {
			return fmt.Sprintf("You might consider trying %s as your pizza toppings. They are all available!", joinNaturalList(items))
		}
	}
	raw, _ := json.Marshal(toolOutput)
	return string(raw)
}

func builtInToolOutput(name string, args map[string]any) map[string]any {
	switch name {
	case "get_qualification_info", "local:get_qualification_info":
		return map[string]any{"qualification_info": "5+ years of experience"}
	case "get_available_toppings", "local:get_available_toppings":
		return map[string]any{"available_toppings": []string{"Pepperoni", "Mushrooms", "Olives"}}
	default:
		return nil
	}
}

func builtInToolSchema(id string) string {
	switch id {
	case "local:get_qualification_info":
		return `{"type":"object","properties":{},"required":[]}`
	case "local:get_available_toppings":
		return `{"type":"object","properties":{},"required":[]}`
	default:
		return `{"type":"object","properties":{},"required":[]}`
	}
}

func splitToolID(id string) (string, string) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "local", id
}

func scenarioSessionID(s Scenario) string {
	return "session_" + s.ID
}

func idsFromObservations(items []policy.Observation) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return dedupeAndSort(out)
}

func idsFromGuidelines(items []policy.Guideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return dedupeAndSort(out)
}

func idsFromSuppressed(items []policyruntime.SuppressedGuideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return dedupeAndSort(out)
}

func suppressionReasons(items []policyruntime.SuppressedGuideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.ID, "journey_node:") {
			continue
		}
		if strings.TrimSpace(item.Reason) != "" {
			out = append(out, item.Reason)
		}
	}
	return dedupeAndSort(out)
}

func joinTranscript(items []Turn) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Text)
	}
	return strings.Join(parts, "\n")
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func joinNaturalList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	if len(items) == 2 {
		return items[0] + " or " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
}
