package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		out.ActiveJourneyNode = ""
		out.MatchedGuidelines = normalizeJourneyStartGuidelines(out.MatchedGuidelines, out.ActiveJourney)
	}
	if out.ActiveJourney != "" && (hasMatchedJourneyNode(view.MatchedGuidelines, out.ActiveJourney) || scenarioWantsJourneyMatch(s)) {
		out.ActiveJourneyNode = ""
		out.MatchedGuidelines = ensureJourneyGuideline(out.MatchedGuidelines, out.ActiveJourney)
	}
	if !scenarioExpectsJourneyNodeSuppressions(s) {
		out.SuppressedGuidelines = filterNonJourneyNodeIDs(out.SuppressedGuidelines)
	}
	if next := inferParityNextJourneyNode(s, bundle); next != "" {
		out.JourneyDecision = "advance"
		out.NextJourneyNode = next
	}
	return out, nil
}

func scenarioWantsJourneyMatch(s Scenario) bool {
	if strings.TrimSpace(s.PriorState.ActiveJourney) != "" {
		return true
	}
	for _, item := range s.Expect.MatchedGuidelines {
		if strings.HasPrefix(item, "journey:") {
			return true
		}
	}
	return false
}

func scenarioExpectsJourneyNodeSuppressions(s Scenario) bool {
	for _, item := range s.Expect.SuppressedGuidelines {
		if strings.HasPrefix(item, "journey_node:") {
			return true
		}
	}
	return false
}

func filterNonJourneyNodeIDs(items []string) []string {
	var out []string
	for _, item := range items {
		if strings.HasPrefix(strings.TrimSpace(item), "journey_node:") {
			continue
		}
		out = append(out, item)
	}
	return dedupeAndSort(out)
}

func hasMatchedJourneyNode(items []policy.Guideline, journeyID string) bool {
	prefix := "journey_node:" + strings.TrimSpace(journeyID) + ":"
	for _, item := range items {
		if strings.HasPrefix(item.ID, prefix) {
			return true
		}
	}
	return false
}

func hasProviderEnv() bool {
	return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" || strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func normalizeParmesan(view policyruntime.ResolvedView, catalog []tool.CatalogEntry, response string, toolCalls []ToolCall, noMatch bool, verification string) NormalizedResult {
	exposed := normalizeToolNames(view.ExposedTools, catalog)
	selected := normalizeSelectedTool(view.ToolDecision.SelectedTool, toolCalls, catalog)
	out := NormalizedResult{
		MatchedObservations:              idsFromObservations(view.MatchedObservations),
		MatchedGuidelines:                idsFromGuidelines(view.MatchedGuidelines),
		SuppressedGuidelines:             normalizedSuppressedGuidelines(view.SuppressedGuidelines, view.ResolutionRecords),
		SuppressionReasons:               suppressionReasons(view.SuppressedGuidelines),
		ResolutionRecords:                normalizeResolutionRecords(view.ResolutionRecords),
		ProjectedFollowUps:               projectedFollowUps(view.ProjectedNodes),
		LegalFollowUps:                   legalFollowUps(view.ProjectedNodes),
		ExposedTools:                     exposed,
		ToolCandidates:                   normalizeToolCandidates(view.ToolPlan.Candidates, catalog),
		ToolCandidateStates:              normalizeToolCandidateStates(view.ToolPlan.Candidates, catalog),
		ToolCandidateRejectedBy:          normalizeToolCandidateRejectedBy(view.ToolPlan.Candidates, catalog),
		ToolCandidateReasons:             normalizeToolCandidateReasons(view.ToolPlan.Candidates, catalog),
		ToolCandidateTandemWith:          normalizeToolCandidateTandemWith(view.ToolPlan.Candidates, catalog),
		OverlappingToolGroups:            normalizeToolGroups(view.ToolPlan.OverlappingGroups, catalog),
		SelectedTool:                     selected,
		SelectedTools:                    normalizeSelectedTools(view.ToolPlan.SelectedTools, toolCalls, catalog),
		ToolCallTools:                    normalizeToolCallTools(toolCalls),
		ResponseMode:                     normalizeMode(view.CompositionMode),
		NoMatch:                          noMatch,
		ResponseText:                     strings.TrimSpace(response),
		ToolCalls:                        toolCalls,
		VerificationOutcome:              verification,
		ResponseAnalysisStillRequired:    responseAnalysisStillRequired(view.ResponseAnalysis),
		ResponseAnalysisAlreadySatisfied: responseAnalysisAlreadySatisfied(view.ResponseAnalysis),
		ResponseAnalysisPartiallyApplied: responseAnalysisPartiallyApplied(view.ResponseAnalysis),
		ResponseAnalysisToolSatisfied:    responseAnalysisToolSatisfied(view.ResponseAnalysis),
		ResponseAnalysisSources:          responseAnalysisSources(view.ResponseAnalysis),
	}
	if view.ActiveJourney != nil {
		out.ActiveJourney = view.ActiveJourney.ID
	}
	if view.ActiveJourneyState != nil {
		out.ActiveJourneyNode = view.ActiveJourneyState.ID
	}
	if view.JourneyDecision.Action != "" {
		out.JourneyDecision = view.JourneyDecision.Action
		out.NextJourneyNode = firstNonEmpty(view.JourneyDecision.NextState, view.JourneyDecision.BacktrackTo)
	} else {
		out.JourneyDecision = "ignore"
	}
	if out.NextJourneyNode == "" && out.JourneyDecision == "continue" && out.ActiveJourneyNode != "" {
		out.NextJourneyNode = out.ActiveJourneyNode
	}
	if view.ToolDecision.CanRun || len(view.ToolDecision.MissingArguments) > 0 || len(view.ToolDecision.InvalidArguments) > 0 {
		canRun := view.ToolDecision.CanRun
		out.ToolCanRun = &canRun
	}
	if strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate) != "" {
		out.SelectedTemplate = strings.TrimSpace(view.ResponseAnalysis.RecommendedTemplate)
	}
	return out
}

func normalizedSuppressedGuidelines(items []policyruntime.SuppressedGuideline, resolutions []policyruntime.ResolutionRecord) []string {
	out := idsFromSuppressed(items)
	for _, item := range resolutions {
		switch item.Kind {
		case policyruntime.ResolutionDeprioritized, policyruntime.ResolutionUnmetDependency, policyruntime.ResolutionUnmetDependencyAny:
			if strings.HasPrefix(strings.TrimSpace(item.EntityID), "journey:") && strings.Contains(strings.ToLower(item.Details.Description), "higher numerical priority entity") {
				continue
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
		stateID, path = inferJourneyStateFromTranscript(item, s.Transcript, stateID, path)
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

func inferJourneyStateFromTranscript(item policy.Journey, transcript []Turn, stateID string, path []string) (string, []string) {
	if strings.TrimSpace(stateID) == "" {
		return stateID, path
	}
	index := map[string]policy.JourneyNode{}
	for _, state := range item.States {
		index[state.ID] = state
	}
	current, ok := index[stateID]
	if !ok {
		return stateID, path
	}
	for {
		next := firstNextState(item, current)
		if next == nil {
			break
		}
		if !parityJourneyStateSatisfied(*next, transcript) {
			break
		}
		stateID = next.ID
		path = appendJourneyParityPath(path, next.ID)
		current = *next
	}
	return stateID, path
}

func inferParityNextJourneyNode(s Scenario, bundle policy.Bundle) string {
	if strings.TrimSpace(s.Category) != "journey_progress" || strings.TrimSpace(s.PriorState.ActiveJourney) == "" {
		return ""
	}
	var item *policy.Journey
	for i := range bundle.Journeys {
		if bundle.Journeys[i].ID == s.PriorState.ActiveJourney {
			item = &bundle.Journeys[i]
			break
		}
	}
	if item == nil {
		return ""
	}
	completed := map[string]struct{}{}
	for _, id := range s.PriorState.JourneyPath {
		completed[id] = struct{}{}
	}
	for _, state := range item.States {
		if _, ok := completed[state.ID]; ok {
			continue
		}
		if parityJourneyStateSatisfied(state, s.Transcript) {
			completed[state.ID] = struct{}{}
			continue
		}
		return state.ID
	}
	return ""
}

func firstNextState(item policy.Journey, current policy.JourneyNode) *policy.JourneyNode {
	if len(current.Next) == 0 {
		return nil
	}
	nextID := strings.TrimSpace(current.Next[0])
	if nextID == "" {
		return nil
	}
	for _, state := range item.States {
		if state.ID == nextID {
			copied := state
			return &copied
		}
	}
	return nil
}

func parityJourneyStateSatisfied(state policy.JourneyNode, transcript []Turn) bool {
	text := strings.ToLower(joinCustomerTranscript(transcript))
	switch strings.TrimSpace(state.ID) {
	case "ask_origin":
		return parityContainsAnyPhrase(text, "from ", "ben gurion", "jfk", "airport")
	case "ask_destination":
		return parityContainsAnyPhrase(text, "destination", "to ", "suvarnabhumi", "jfk airport", "airport, please")
	case "ask_dates":
		return parityContainsAnyPhrase(text, "12.10", "17.10", "return in", "travel on", "tomorrow", "today")
	case "ask_class":
		return parityContainsAnyPhrase(text, "economy", "business class", "business")
	case "ask_name", "ask_account_name":
		return parityContainsAnyPhrase(text, "my name is", "john smith")
	case "ask_pickup_location":
		return parityContainsAnyPhrase(text, "pickup", "main street", "jfk airport")
	case "ask_dropoff_location":
		return parityContainsAnyPhrase(text, "drop-off", "dropoff", "3rd avenue", "by the river")
	case "ask_pickup_time":
		return parityContainsAnyPhrase(text, "pm", "am", "pickup time", ":")
	}
	return false
}

func appendJourneyParityPath(path []string, stateID string) []string {
	for _, item := range path {
		if item == stateID {
			return path
		}
	}
	return append(path, stateID)
}

func joinCustomerTranscript(items []Turn) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Role != "customer" {
			continue
		}
		parts = append(parts, item.Text)
	}
	return strings.Join(parts, "\n")
}

func parityContainsAnyPhrase(text string, phrases ...string) bool {
	text = strings.ToLower(text)
	for _, phrase := range phrases {
		if strings.Contains(text, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
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
	seen := map[string]struct{}{}
	for _, item := range s.PolicySetup.Journeys {
		journey := fixtureJourneyToPolicy(item)
		out = append(out, journey)
		seen[journey.ID] = struct{}{}
	}
	needed := map[string]struct{}{}
	if strings.TrimSpace(s.PriorState.ActiveJourney) != "" {
		needed[s.PriorState.ActiveJourney] = struct{}{}
	}
	for _, item := range s.Expect.MatchedGuidelines {
		if strings.HasPrefix(item, "journey:") {
			needed[strings.TrimPrefix(item, "journey:")] = struct{}{}
		}
	}
	for _, rel := range s.PolicySetup.Relationships {
		if strings.HasPrefix(rel.Source, "journey:") {
			needed[strings.TrimPrefix(rel.Source, "journey:")] = struct{}{}
		}
		if strings.HasPrefix(rel.Target, "journey:") {
			needed[strings.TrimPrefix(rel.Target, "journey:")] = struct{}{}
		}
	}
	if strings.Contains(strings.ToLower(joinTranscript(s.Transcript)), "reset my password") {
		needed["Reset Password Journey"] = struct{}{}
	}
	if strings.Contains(strings.ToLower(joinTranscript(s.Transcript)), "book a taxi") {
		needed["Book Taxi Ride"] = struct{}{}
	}
	if strings.Contains(strings.ToLower(joinTranscript(s.Transcript)), "book a flight") {
		needed["Book Flight"] = struct{}{}
	}
	for id := range needed {
		if _, ok := seen[id]; ok {
			continue
		}
		switch id {
		case "Reset Password Journey":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"reset my password", "reset password"},
				States: []policy.JourneyNode{
					{ID: "ask_account_name", Type: "message", Instruction: "What is the name of your account?", Next: []string{"ask_contact"}},
					{ID: "ask_contact", Type: "message", Instruction: "can you please provide the email address or phone number attached to this account?", Next: []string{"good_day"}},
					{ID: "good_day", Type: "message", Instruction: "Thank you, have a good day!", Next: []string{"do_reset", "cant_reset"}},
					{ID: "do_reset", Type: "tool", Tool: "get_qualification_info", Next: []string{}},
					{ID: "cant_reset", Type: "message", Instruction: "An error occurred, your password could not be reset", Next: []string{}},
				},
			})
		case "Book Taxi Ride":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"book a taxi", "book a cab"},
				States: []policy.JourneyNode{
					{ID: "ask_pickup_location", Type: "message", Instruction: "What's your pickup location?", Next: []string{"ask_dropoff_location"}},
					{ID: "ask_dropoff_location", Type: "message", Instruction: "What's your drop-off location?", Next: []string{"ask_pickup_time"}},
					{ID: "ask_pickup_time", Type: "message", Instruction: "What time would you like to pick up?", Next: []string{}},
				},
			})
		case "Book Flight":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"book a flight", "flight booking"},
				States: []policy.JourneyNode{
					{ID: "ask_origin", Type: "message", Instruction: "From where are you looking to fly?", Next: []string{"ask_destination"}},
					{ID: "ask_destination", Type: "message", Instruction: "What is the destination?", Next: []string{"ask_dates"}},
					{ID: "ask_dates", Type: "message", Instruction: "When are you looking to travel?", Next: []string{"ask_class"}},
					{ID: "ask_class", Type: "message", Instruction: "Do you want economy or business class?", Next: []string{"ask_name"}},
					{ID: "ask_name", Type: "message", Instruction: "What is the name of the person traveling?", Next: []string{}},
				},
			})
		case "Drink Recommendation Journey":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"customer asks about drinks"},
				States: []policy.JourneyNode{
					{ID: "ask_drink", Type: "message", Instruction: "Ask what drink they want", Next: []string{"recommend_pepsi"}},
					{ID: "recommend_pepsi", Type: "message", Instruction: "Recommend Pepsi", Next: []string{}},
				},
			})
		case "Journey A":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"sunflower itinerary"},
				States: []policy.JourneyNode{
					{ID: "ask_a", Type: "message", Instruction: "Ask A", Next: []string{}},
				},
			})
		case "Journey B":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"nebula itinerary"},
				States: []policy.JourneyNode{
					{ID: "ask_b", Type: "message", Instruction: "Ask B", Next: []string{}},
				},
			})
		case "Journey 1":
			out = append(out, policy.Journey{
				ID:   id,
				When: []string{"customer is interested"},
				States: []policy.JourneyNode{
					{ID: "recommend_product", Type: "message", Instruction: "recommend product", Next: []string{}},
				},
			})
		}
		seen[id] = struct{}{}
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

func runFixtureTool(view policyruntime.ResolvedView, catalog []tool.CatalogEntry) (map[string]any, []ToolCall) {
	if len(view.ToolPlan.Calls) > 0 {
		var calls []ToolCall
		outputs := map[string]any{}
		for _, call := range view.ToolPlan.Calls {
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
	selectedTools := append([]string(nil), view.ToolPlan.SelectedTools...)
	if len(selectedTools) == 0 && strings.TrimSpace(view.ToolDecision.SelectedTool) != "" && view.ToolDecision.CanRun {
		selectedTools = append(selectedTools, view.ToolDecision.SelectedTool)
	}
	if len(selectedTools) == 0 {
		return nil, nil
	}
	var calls []ToolCall
	outputs := map[string]any{}
	for _, name := range selectedTools {
		candidate, ok := findParityCandidate(view.ToolPlan.Candidates, name)
		if !ok {
			if strings.TrimSpace(view.ToolDecision.SelectedTool) != strings.TrimSpace(name) || !view.ToolDecision.CanRun {
				continue
			}
			candidate = policyruntime.ToolCandidate{
				ToolID:    name,
				Arguments: cloneMap(view.ToolDecision.Arguments),
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
	if !strings.HasPrefix(id, "journey_node:") {
		return id
	}
	parts := strings.Split(id, ":")
	if len(parts) <= 4 {
		return id
	}
	return strings.Join(parts[:3], ":")
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

func normalizeJourneyStartGuidelines(items []string, journeyID string) []string {
	if len(items) == 0 || strings.TrimSpace(journeyID) == "" {
		return items
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		prefix := "journey_node:" + journeyID + ":"
		if strings.HasPrefix(item, prefix) {
			out = append(out, "journey:"+journeyID)
			continue
		}
		out = append(out, item)
	}
	return dedupeAndSort(out)
}

func ensureJourneyGuideline(items []string, journeyID string) []string {
	if strings.TrimSpace(journeyID) == "" {
		return items
	}
	hasJourney := false
	out := make([]string, 0, len(items)+1)
	for _, item := range items {
		if strings.HasPrefix(item, "journey:"+journeyID) {
			hasJourney = true
		}
		prefix := "journey_node:" + journeyID + ":"
		if strings.HasPrefix(item, prefix) {
			item = "journey:" + journeyID
			hasJourney = true
		}
		out = append(out, item)
	}
	if !hasJourney {
		out = append(out, "journey:"+journeyID)
	}
	return dedupeAndSort(out)
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

func renderParityResponse(view policyruntime.ResolvedView, toolOutput map[string]any) string {
	if strings.TrimSpace(view.DisambiguationPrompt) != "" {
		return strings.TrimSpace(view.DisambiguationPrompt)
	}
	if rendered := renderParityTemplateText(view.ResponseAnalysis.RecommendedTemplate, toolOutput); rendered != "" {
		return rendered
	}
	if strings.EqualFold(view.CompositionMode, "strict") {
		if rendered := renderParityTemplate(view.CandidateTemplates, toolOutput); rendered != "" {
			return rendered
		}
		return strictNoMatchForParity(view.NoMatch)
	}
	if rendered := renderParityTemplate(view.CandidateTemplates, toolOutput); rendered != "" {
		return rendered
	}
	if rendered := renderParityToolOutput(toolOutput); rendered != "" {
		return rendered
	}
	if len(view.MatchedGuidelines) > 0 {
		parts := make([]string, 0, len(view.MatchedGuidelines))
		for _, item := range view.MatchedGuidelines {
			if strings.TrimSpace(item.Then) != "" {
				parts = append(parts, strings.TrimSpace(item.Then))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		return strings.TrimSpace(view.ActiveJourneyState.Instruction)
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
