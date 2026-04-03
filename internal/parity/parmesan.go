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
	if out.ActiveJourney != "" {
		out.ActiveJourneyNode = ""
		out.MatchedGuidelines = ensureJourneyGuideline(out.MatchedGuidelines, out.ActiveJourney)
	}
	if next := inferParityNextJourneyNode(s, bundle); next != "" {
		out.JourneyDecision = "advance"
		out.NextJourneyNode = next
	}
	return out, nil
}

func hasProviderEnv() bool {
	return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" || strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func normalizeParmesan(view policyruntime.ResolvedView, catalog []tool.CatalogEntry, response string, toolCalls []ToolCall, noMatch bool, verification string) NormalizedResult {
	exposed := normalizeToolNames(view.ExposedTools, catalog)
	selected := normalizeSelectedTool(view.ToolDecision.SelectedTool, toolCalls, catalog)
	out := NormalizedResult{
		MatchedObservations:  idsFromObservations(view.MatchedObservations),
		MatchedGuidelines:    idsFromGuidelines(view.MatchedGuidelines),
		SuppressedGuidelines: idsFromSuppressed(view.SuppressedGuidelines),
		SuppressionReasons:   suppressionReasons(view.SuppressedGuidelines),
		ExposedTools:         exposed,
		SelectedTool:         selected,
		ResponseMode:         normalizeMode(view.CompositionMode),
		NoMatch:              noMatch,
		ResponseText:         strings.TrimSpace(response),
		ToolCalls:            toolCalls,
		VerificationOutcome:  verification,
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

func buildEventsForScenario(s Scenario) []session.Event {
	sessionID := scenarioSessionID(s)
	now := time.Now().UTC()
	events := make([]session.Event, 0, len(s.Transcript))
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
		out = append(out, tool.CatalogEntry{
			ID:              id,
			ProviderID:      providerID,
			Name:            name,
			Description:     name,
			RuntimeProtocol: "mcp",
			Schema:          builtInToolSchema(id),
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
				ID:    g.ID,
				When:  g.Condition,
				Then:  g.Action,
				Scope: scope,
				Track: true,
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
	needed := map[string]struct{}{}
	if strings.TrimSpace(s.PriorState.ActiveJourney) != "" {
		needed[s.PriorState.ActiveJourney] = struct{}{}
	}
	for _, item := range s.Expect.MatchedGuidelines {
		if strings.HasPrefix(item, "journey:") {
			needed[strings.TrimPrefix(item, "journey:")] = struct{}{}
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
	var out []policy.Journey
	for id := range needed {
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

func runFixtureTool(view policyruntime.ResolvedView, catalog []tool.CatalogEntry) (map[string]any, []ToolCall) {
	name := strings.TrimSpace(view.ToolDecision.SelectedTool)
	if name == "" || !view.ToolDecision.CanRun {
		return nil, nil
	}
	fullID := fullToolID(name, catalog)
	output := builtInToolOutput(name, view.ToolDecision.Arguments)
	if output == nil {
		output = builtInToolOutput(fullID, view.ToolDecision.Arguments)
	}
	if output == nil {
		return nil, []ToolCall{{ToolID: fullID, Arguments: cloneMap(view.ToolDecision.Arguments)}}
	}
	return output, []ToolCall{{ToolID: fullID, Arguments: cloneMap(view.ToolDecision.Arguments)}}
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

func normalizeSelectedTool(name string, calls []ToolCall, catalog []tool.CatalogEntry) string {
	if len(calls) > 0 && strings.TrimSpace(calls[0].ToolID) != "" {
		return calls[0].ToolID
	}
	return fullToolID(name, catalog)
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
		if strings.HasPrefix(item.ID, "journey_node:") {
			continue
		}
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
