package policyruntime

import (
	"fmt"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
)

func SelectHistoryEvents(events []session.Event, bundle policy.Bundle, executionID string) ([]session.Event, HistorySelectionStageResult) {
	cfg := contextHistoryDefaults(bundle.ContextHistory)
	stage := HistorySelectionStageResult{Enabled: cfg.enabled, OriginalEvents: len(events)}
	if !cfg.enabled || len(events) == 0 {
		stage.Included = len(events)
		stage.SelectedEvents = len(events)
		stage.Decisions = historySelectionAllIncluded(events, "disabled")
		return append([]session.Event(nil), events...), stage
	}

	latestCustomer := latestCustomerEventIndexForPolicy(events)
	actions := make([]string, len(events))
	reasons := make([][]string, len(events))

	for i, event := range events {
		action := "include"
		reason := []string{"default"}
		switch {
		case shouldAlwaysIncludeHistoryEvent(event, executionID):
			reason = []string{"runtime_event"}
		case event.Source == "customer" && isModeratedHistoryEvent(event):
			action = cfg.moderated
			reason = moderationHistoryReasons(event)
		case event.Source == "customer" && i != latestCustomer && cfg.outOfScope == "exclude_turn" && customerEventOutOfScope(event, bundle.DomainBoundary):
			action = "exclude"
			reason = []string{"domain_boundary_out_of_scope"}
		}
		if i == latestCustomer && cfg.keepLatestCustomerTurn {
			if action == "exclude" {
				action = "include"
			}
			reason = append(reason, "latest_turn")
		}
		actions[i] = action
		reasons[i] = dedupe(reason)
	}

	applyAssistantPairExclusions(events, actions, reasons)
	applyHistoryTurnLimit(events, actions, reasons, cfg.maxTurns, latestCustomer)
	excludeUnrelatedToolContext(events, actions, reasons, executionID)

	selected := make([]session.Event, 0, len(events))
	for i, event := range events {
		stage.Decisions = append(stage.Decisions, HistorySelectionDecision{
			EventID: event.ID,
			Source:  event.Source,
			Kind:    event.Kind,
			Action:  actions[i],
			Reasons: append([]string(nil), reasons[i]...),
		})
		switch actions[i] {
		case "exclude":
			stage.Excluded++
		case "metadata_only":
			stage.MetadataOnly++
			stage.Included++
			selected = append(selected, metadataOnlyHistoryEvent(event))
		default:
			stage.Included++
			selected = append(selected, event)
		}
	}
	stage.SelectedEvents = len(selected)
	return selected, stage
}

type contextHistoryConfig struct {
	enabled                bool
	maxTurns               int
	outOfScope             string
	moderated              string
	keepLatestCustomerTurn bool
}

func contextHistoryDefaults(raw policy.ContextHistoryPolicy) contextHistoryConfig {
	cfg := contextHistoryConfig{
		enabled:                true,
		maxTurns:               12,
		outOfScope:             "exclude_turn",
		moderated:              "metadata_only",
		keepLatestCustomerTurn: true,
	}
	if raw.Enabled != nil {
		cfg.enabled = *raw.Enabled
	}
	if raw.MaxTurns > 0 {
		cfg.maxTurns = raw.MaxTurns
	}
	switch strings.ToLower(strings.TrimSpace(raw.OutOfScope)) {
	case "include", "exclude_turn":
		cfg.outOfScope = strings.ToLower(strings.TrimSpace(raw.OutOfScope))
	}
	switch strings.ToLower(strings.TrimSpace(raw.Moderated)) {
	case "include", "exclude", "metadata_only":
		cfg.moderated = strings.ToLower(strings.TrimSpace(raw.Moderated))
	}
	if raw.KeepLatestCustomerTurn != nil {
		cfg.keepLatestCustomerTurn = *raw.KeepLatestCustomerTurn
	}
	return cfg
}

func historySelectionAllIncluded(events []session.Event, reason string) []HistorySelectionDecision {
	out := make([]HistorySelectionDecision, 0, len(events))
	for _, event := range events {
		out = append(out, HistorySelectionDecision{EventID: event.ID, Source: event.Source, Kind: event.Kind, Action: "include", Reasons: []string{reason}})
	}
	return out
}

func latestCustomerEventIndexForPolicy(events []session.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source == "customer" && (events[i].Kind == "" || events[i].Kind == "message") {
			return i
		}
	}
	return -1
}

func shouldAlwaysIncludeHistoryEvent(event session.Event, executionID string) bool {
	if event.Source == "system" && event.Kind == "response.trigger" {
		return true
	}
	if strings.TrimSpace(executionID) != "" && strings.TrimSpace(event.ExecutionID) == strings.TrimSpace(executionID) {
		return true
	}
	if event.Source != "customer" && event.Source != "ai_agent" && !isToolContextEvent(event) {
		return true
	}
	return false
}

func isModeratedHistoryEvent(event session.Event) bool {
	meta := mapValuePolicy(event.Metadata["moderation"])
	if len(meta) == 0 {
		return false
	}
	return boolValue(meta["censored"]) || boolValue(meta["jailbreak"]) || strings.EqualFold(strings.TrimSpace(fmt.Sprint(meta["decision"])), "censored")
}

func moderationHistoryReasons(event session.Event) []string {
	meta := mapValuePolicy(event.Metadata["moderation"])
	var out []string
	if boolValue(meta["censored"]) || strings.EqualFold(strings.TrimSpace(fmt.Sprint(meta["decision"])), "censored") {
		out = append(out, "moderation_censored")
	}
	if boolValue(meta["jailbreak"]) {
		out = append(out, "moderation_jailbreak")
	}
	for _, category := range stringSliceValue(meta["categories"]) {
		out = append(out, "moderation_category:"+strings.ToLower(strings.TrimSpace(category)))
	}
	if len(out) == 0 {
		out = append(out, "moderation")
	}
	return out
}

func customerEventOutOfScope(event session.Event, boundary policy.DomainBoundary) bool {
	if strings.TrimSpace(boundary.Mode) == "" {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(eventTextForHistory(event)))
	if text == "" {
		return false
	}
	if len(matchedBoundaryTopics(text, boundary.BlockedTopics)) > 0 {
		return true
	}
	if len(boundary.AllowedTopics) == 0 {
		return false
	}
	if len(matchedBoundaryTopics(text, boundary.AllowedTopics)) > 0 || len(matchedBoundaryTopics(text, boundary.AdjacentTopics)) > 0 {
		return false
	}
	return true
}

func eventTextForHistory(event session.Event) string {
	var parts []string
	for _, part := range event.Content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, " ")
}

func isToolContextEvent(event session.Event) bool {
	if strings.HasPrefix(event.Kind, "tool.") {
		return true
	}
	if _, ok := stagedToolCallFromEvent(event); ok {
		return true
	}
	return false
}

func applyAssistantPairExclusions(events []session.Event, actions []string, reasons [][]string) {
	for i, event := range events {
		if actions[i] != "exclude" || event.Source != "customer" {
			continue
		}
		for j := i + 1; j < len(events); j++ {
			if events[j].Source == "customer" {
				break
			}
			if events[j].Source != "ai_agent" || actions[j] == "exclude" {
				continue
			}
			actions[j] = "exclude"
			reasons[j] = append(reasons[j], "paired_with_excluded_customer_turn")
		}
	}
}

func applyHistoryTurnLimit(events []session.Event, actions []string, reasons [][]string, maxTurns int, latestCustomer int) {
	if maxTurns <= 0 {
		return
	}
	count := 0
	for i := len(events) - 1; i >= 0; i-- {
		if actions[i] == "exclude" || (events[i].Source != "customer" && events[i].Source != "ai_agent") {
			continue
		}
		count++
		if count <= maxTurns || i == latestCustomer {
			continue
		}
		actions[i] = "exclude"
		reasons[i] = append(reasons[i], "recency_limit")
	}
}

func excludeUnrelatedToolContext(events []session.Event, actions []string, reasons [][]string, executionID string) {
	includedExecs := map[string]struct{}{}
	currentExecutionID := strings.TrimSpace(executionID)
	if currentExecutionID != "" {
		includedExecs[currentExecutionID] = struct{}{}
	}
	for i, event := range events {
		if actions[i] == "exclude" {
			continue
		}
		eventExecutionID := strings.TrimSpace(event.ExecutionID)
		if eventExecutionID == "" {
			continue
		}
		if event.Source == "customer" || event.Source == "ai_agent" || eventExecutionID == currentExecutionID {
			includedExecs[eventExecutionID] = struct{}{}
		}
	}
	for i, event := range events {
		if actions[i] == "exclude" || !isToolContextEvent(event) {
			continue
		}
		eventExecutionID := strings.TrimSpace(event.ExecutionID)
		if _, ok := includedExecs[eventExecutionID]; ok && eventExecutionID != "" {
			continue
		}
		actions[i] = "exclude"
		reasons[i] = append(reasons[i], "unrelated_tool_context")
	}
}

func metadataOnlyHistoryEvent(event session.Event) session.Event {
	out := event
	out.Content = nil
	return out
}

func mapValuePolicy(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	default:
		return nil
	}
}
