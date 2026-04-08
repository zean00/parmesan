package policyyaml

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func ParseBundle(raw []byte) (policy.Bundle, error) {
	var bundle policy.Bundle
	if err := yaml.Unmarshal(raw, &bundle); err != nil {
		return policy.Bundle{}, fmt.Errorf("unmarshal yaml: %w", err)
	}

	bundle.SourceYAML = string(raw)
	bundle.ImportedAt = time.Now().UTC()
	bundle.Journeys = normalizeJourneys(bundle.Journeys)

	if err := ValidateBundle(bundle); err != nil {
		return policy.Bundle{}, err
	}
	bundle.GuidelineToolAssociations = compileGuidelineToolAssociations(bundle)

	return bundle, nil
}

func ValidateBundle(bundle policy.Bundle) error {
	if strings.TrimSpace(bundle.ID) == "" {
		return errors.New("bundle.id is required")
	}
	if strings.TrimSpace(bundle.Version) == "" {
		return errors.New("bundle.version is required")
	}
	if err := validateSoul(bundle.Soul); err != nil {
		return err
	}
	if err := validatePerceivedPerformance(bundle.PerceivedPerformance); err != nil {
		return err
	}
	if err := validateDomainBoundary(bundle.DomainBoundary); err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, item := range bundle.Observations {
		if err := validateID("observation", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.When) == "" {
			return fmt.Errorf("observation %q requires when", item.ID)
		}
		if err := validateMCPRef(item.MCP); err != nil {
			return fmt.Errorf("observation %q: %w", item.ID, err)
		}
	}
	for _, item := range bundle.Guidelines {
		if err := validateID("guideline", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.When) == "" || strings.TrimSpace(item.Then) == "" {
			return fmt.Errorf("guideline %q requires when and then", item.ID)
		}
		if err := validateMCPRef(item.MCP); err != nil {
			return fmt.Errorf("guideline %q: %w", item.ID, err)
		}
	}
	for _, item := range bundle.Journeys {
		if err := validateID("journey", item.ID, seen); err != nil {
			return err
		}
		if len(item.When) == 0 {
			return fmt.Errorf("journey %q requires when", item.ID)
		}
		if len(item.States) == 0 {
			return fmt.Errorf("journey %q requires states", item.ID)
		}
		if strings.TrimSpace(item.RootID) == "" {
			return fmt.Errorf("journey %q requires root_id after normalization", item.ID)
		}
		stateIDs := map[string]struct{}{}
		for _, state := range item.States {
			if strings.TrimSpace(state.ID) == "" || strings.TrimSpace(state.Type) == "" {
				return fmt.Errorf("journey %q has invalid state", item.ID)
			}
			stateIDs[strings.TrimSpace(state.ID)] = struct{}{}
			if err := validateMCPRef(state.MCP); err != nil {
				return fmt.Errorf("journey %q state %q: %w", item.ID, state.ID, err)
			}
		}
		for _, edge := range item.Edges {
			if strings.TrimSpace(edge.ID) == "" || strings.TrimSpace(edge.Source) == "" || strings.TrimSpace(edge.Target) == "" {
				return fmt.Errorf("journey %q has invalid edge", item.ID)
			}
			if edge.Source != item.RootID {
				if _, ok := stateIDs[edge.Source]; !ok {
					return fmt.Errorf("journey %q edge %q references unknown source %q", item.ID, edge.ID, edge.Source)
				}
			}
			if _, ok := stateIDs[edge.Target]; !ok {
				return fmt.Errorf("journey %q edge %q references unknown target %q", item.ID, edge.ID, edge.Target)
			}
		}
		for _, guideline := range item.Guidelines {
			if err := validateID("journey guideline", guideline.ID, seen); err != nil {
				return err
			}
		}
		for _, template := range item.Templates {
			if err := validateID("journey template", template.ID, seen); err != nil {
				return err
			}
		}
	}
	for _, item := range bundle.Templates {
		if err := validateID("template", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.Text) == "" && !hasTemplateMessages(item.Messages) {
			return fmt.Errorf("template %q requires text or messages", item.ID)
		}
	}
	for _, item := range bundle.Glossary {
		if strings.TrimSpace(item.Term) == "" {
			return fmt.Errorf("glossary term requires term")
		}
	}
	for _, item := range bundle.ToolPolicies {
		if err := validateID("tool_policy", item.ID, seen); err != nil {
			return err
		}
		if len(item.ToolIDs) == 0 {
			return fmt.Errorf("tool_policy %q requires tool_ids", item.ID)
		}
	}
	for _, item := range bundle.Retrievers {
		if err := validateID("retriever", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.Kind) == "" {
			return fmt.Errorf("retriever %q requires kind", item.ID)
		}
		if strings.TrimSpace(item.Kind) != "knowledge" {
			return fmt.Errorf("retriever %q has unsupported kind %q", item.ID, item.Kind)
		}
		scope := strings.TrimSpace(item.Scope)
		if scope == "" {
			return fmt.Errorf("retriever %q requires scope", item.ID)
		}
		switch scope {
		case "agent":
		case "guideline", "journey", "journey_state":
			if strings.TrimSpace(item.TargetID) == "" {
				return fmt.Errorf("retriever %q requires target_id for scope %q", item.ID, scope)
			}
		default:
			return fmt.Errorf("retriever %q has unsupported scope %q", item.ID, scope)
		}
		if item.MaxResults < 0 {
			return fmt.Errorf("retriever %q max_results cannot be negative", item.ID)
		}
		if item.Mode != "" && item.Mode != "eager" && item.Mode != "scoped" && item.Mode != "deferred" {
			return fmt.Errorf("retriever %q has unsupported mode %q", item.ID, item.Mode)
		}
	}

	return nil
}

func validateSoul(soul policy.Soul) error {
	if strings.TrimSpace(soul.DefaultLanguage) != "" {
		if err := validateLanguageCode("soul.default_language", soul.DefaultLanguage); err != nil {
			return err
		}
	}
	seenLanguages := map[string]struct{}{}
	for _, language := range soul.SupportedLanguages {
		language = strings.TrimSpace(language)
		if err := validateLanguageCode("soul.supported_languages", language); err != nil {
			return err
		}
		if _, ok := seenLanguages[language]; ok {
			return fmt.Errorf("soul.supported_languages contains duplicate %q", language)
		}
		seenLanguages[language] = struct{}{}
	}
	if err := validateNonEmptyUnique("soul.style_rules", soul.StyleRules); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("soul.avoid_rules", soul.AvoidRules); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("soul.formatting_rules", soul.FormattingRules); err != nil {
		return err
	}
	return nil
}

func validateDomainBoundary(boundary policy.DomainBoundary) error {
	mode := strings.TrimSpace(boundary.Mode)
	if mode == "" {
		return nil
	}
	switch mode {
	case "hard_refuse", "soft_redirect", "broad_concierge":
	default:
		return fmt.Errorf("domain_boundary.mode has unsupported value %q", boundary.Mode)
	}
	if err := validateNonEmptyUnique("domain_boundary.allowed_topics", boundary.AllowedTopics); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("domain_boundary.adjacent_topics", boundary.AdjacentTopics); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("domain_boundary.blocked_topics", boundary.BlockedTopics); err != nil {
		return err
	}
	action := strings.TrimSpace(boundary.AdjacentAction)
	if action != "" {
		switch action {
		case "allow", "redirect", "refuse":
		default:
			return fmt.Errorf("domain_boundary.adjacent_action has unsupported value %q", boundary.AdjacentAction)
		}
	}
	uncertainty := strings.TrimSpace(boundary.UncertaintyAction)
	if uncertainty != "" {
		switch uncertainty {
		case "redirect", "refuse", "escalate":
		default:
			return fmt.Errorf("domain_boundary.uncertainty_action has unsupported value %q", boundary.UncertaintyAction)
		}
	}
	if (mode == "hard_refuse" || mode == "soft_redirect" || len(boundary.BlockedTopics) > 0 || action == "redirect" || action == "refuse" || uncertainty == "redirect" || uncertainty == "refuse") &&
		strings.TrimSpace(boundary.OutOfScopeReply) == "" {
		return errors.New("domain_boundary.out_of_scope_reply is required when domain boundary can refuse or redirect")
	}
	return nil
}

func validatePerceivedPerformance(perf policy.PerceivedPerformancePolicy) error {
	mode := strings.TrimSpace(perf.Mode)
	if mode != "" {
		switch mode {
		case "off", "smart", "aggressive":
		default:
			return fmt.Errorf("perceived_performance.mode has unsupported value %q", perf.Mode)
		}
	}
	if perf.PreambleDelayMS < 0 {
		return errors.New("perceived_performance.preamble_delay_ms cannot be negative")
	}
	if perf.ProcessingUpdateDelayMS < 0 {
		return errors.New("perceived_performance.processing_update_delay_ms cannot be negative")
	}
	if err := validateNonEmptyUnique("perceived_performance.preambles", perf.Preambles); err != nil {
		return err
	}
	for _, tier := range perf.AllowedRiskTiers {
		switch strings.ToLower(strings.TrimSpace(tier)) {
		case "low", "medium", "high":
		default:
			return fmt.Errorf("perceived_performance.allowed_risk_tiers has unsupported value %q", tier)
		}
	}
	return validateNonEmptyUnique("perceived_performance.allowed_risk_tiers", perf.AllowedRiskTiers)
}

func validateLanguageCode(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s cannot contain empty language", field)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' {
			continue
		}
		return fmt.Errorf("%s has invalid language code %q", field, value)
	}
	return nil
}

func validateNonEmptyUnique(field string, values []string) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s cannot contain empty rule", field)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s contains duplicate rule %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateMCPRef(ref *policy.MCPRef) error {
	if ref == nil {
		return nil
	}
	if strings.TrimSpace(ref.Server) == "" {
		return errors.New("mcp.server is required when mcp is set")
	}
	if strings.TrimSpace(ref.Tool) != "" && len(ref.Tools) > 0 {
		return errors.New("mcp.tool and mcp.tools cannot both be set")
	}
	return nil
}

func validateID(kind, id string, seen map[string]struct{}) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%s id is required", kind)
	}
	if _, ok := seen[id]; ok {
		return fmt.Errorf("duplicate artifact id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func compileGuidelineToolAssociations(bundle policy.Bundle) []policy.GuidelineToolAssociation {
	seen := map[string]struct{}{}
	var out []policy.GuidelineToolAssociation
	add := func(guidelineID string, toolID string) {
		guidelineID = strings.TrimSpace(guidelineID)
		toolID = strings.TrimSpace(toolID)
		if guidelineID == "" || toolID == "" {
			return
		}
		key := guidelineID + "::" + toolID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, policy.GuidelineToolAssociation{
			GuidelineID: guidelineID,
			ToolID:      toolID,
		})
	}

	addRefs := func(guidelineID string, tools []string, ref *policy.MCPRef) {
		for _, toolID := range tools {
			add(guidelineID, toolID)
		}
		if ref == nil {
			return
		}
		if strings.TrimSpace(ref.Tool) != "" {
			add(guidelineID, ref.Server+"."+ref.Tool)
		}
		for _, toolID := range ref.Tools {
			add(guidelineID, ref.Server+"."+toolID)
		}
		if strings.TrimSpace(ref.Server) != "" && strings.TrimSpace(ref.Tool) == "" && len(ref.Tools) == 0 {
			add(guidelineID, ref.Server+".*")
		}
	}

	for _, guideline := range bundle.Guidelines {
		addRefs(guideline.ID, guideline.Tools, guideline.MCP)
	}
	for _, flow := range bundle.Journeys {
		for _, guideline := range flow.Guidelines {
			addRefs(guideline.ID, guideline.Tools, guideline.MCP)
		}
		for _, state := range flow.States {
			projectedID := "journey_node:" + flow.ID + ":" + state.ID
			addRefs(projectedID, []string{state.Tool}, state.MCP)
		}
	}
	return out
}

func normalizeJourneys(items []policy.Journey) []policy.Journey {
	out := make([]policy.Journey, 0, len(items))
	for _, item := range items {
		if len(item.States) > 0 && strings.TrimSpace(item.RootID) == "" {
			item.RootID = strings.TrimSpace(item.States[0].ID)
		}
		if len(item.Edges) == 0 {
			item.Edges = compileJourneyEdges(item)
		}
		out = append(out, item)
	}
	return out
}

func compileJourneyEdges(item policy.Journey) []policy.JourneyEdge {
	var out []policy.JourneyEdge
	seen := map[string]struct{}{}
	rootID := strings.TrimSpace(item.RootID)
	for i, state := range item.States {
		if i == 0 && rootID != "" && state.ID != rootID {
			edgeID := fmt.Sprintf("%s:root->%s", item.ID, state.ID)
			if _, ok := seen[edgeID]; !ok {
				seen[edgeID] = struct{}{}
				out = append(out, policy.JourneyEdge{ID: edgeID, Source: rootID, Target: state.ID})
			}
		}
		for _, next := range state.Next {
			next = strings.TrimSpace(next)
			if next == "" {
				continue
			}
			edgeID := fmt.Sprintf("%s:%s->%s", item.ID, state.ID, next)
			if _, ok := seen[edgeID]; ok {
				continue
			}
			seen[edgeID] = struct{}{}
			out = append(out, policy.JourneyEdge{
				ID:        edgeID,
				Source:    state.ID,
				Target:    next,
				Condition: strings.Join(itemStateWhen(item, next), " "),
			})
		}
	}
	if len(out) == 0 && len(item.States) > 0 && rootID != "" {
		edgeID := fmt.Sprintf("%s:%s->%s", item.ID, rootID, item.States[0].ID)
		out = append(out, policy.JourneyEdge{ID: edgeID, Source: rootID, Target: item.States[0].ID})
	}
	return out
}

func itemStateWhen(item policy.Journey, stateID string) []string {
	for _, state := range item.States {
		if state.ID == stateID {
			return append([]string(nil), state.When...)
		}
	}
	return nil
}

func hasTemplateMessages(messages []string) bool {
	for _, message := range messages {
		if strings.TrimSpace(message) != "" {
			return true
		}
	}
	return false
}
