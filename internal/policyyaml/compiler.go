package policyyaml

import (
	"errors"
	"fmt"
	"strconv"
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
	bundle = applyBundleDefaults(bundle)

	if err := ValidateBundle(bundle); err != nil {
		return policy.Bundle{}, err
	}
	bundle.GuidelineToolAssociations = compileGuidelineToolAssociations(bundle)
	bundle.GuidelineAgentAssociations = compileGuidelineAgentAssociations(bundle)

	return bundle, nil
}

func ValidateBundle(bundle policy.Bundle) error {
	if strings.TrimSpace(bundle.ID) == "" {
		return errors.New("bundle.id is required")
	}
	if strings.TrimSpace(bundle.Version) == "" {
		return errors.New("bundle.version is required")
	}
	if err := validatePerceivedPerformance(bundle.PerceivedPerformance); err != nil {
		return err
	}
	if err := validateSemantics(bundle.Semantics); err != nil {
		return err
	}
	if err := validateWatchCapabilities(bundle.WatchCapabilities); err != nil {
		return err
	}
	if err := validateDelegationWorkflows(bundle.DelegationWorkflows); err != nil {
		return err
	}
	if err := validateResponseCapabilities(bundle.ResponseCapabilities); err != nil {
		return err
	}
	if err := validateResponseStyleProfiles(bundle.ResponseStyleProfiles); err != nil {
		return err
	}
	if err := validateSoul(bundle.Soul, bundle.ResponseStyleProfiles); err != nil {
		return err
	}
	if err := validateDelegationContracts(bundle.DelegationContracts, bundle.WatchCapabilities); err != nil {
		return err
	}
	if err := validateGuidelineAgentBindings(bundle); err != nil {
		return err
	}
	if err := validateQualityProfile(bundle.QualityProfile); err != nil {
		return err
	}
	if err := validateLifecyclePolicy(bundle.LifecyclePolicy); err != nil {
		return err
	}
	if err := validateCapabilityIsolation(bundle.CapabilityIsolation); err != nil {
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
		if err := validateNonEmptyUnique("guideline.agents", item.Agents); err != nil {
			return fmt.Errorf("guideline %q: %w", item.ID, err)
		}
		if err := validateResponseCapabilityReference("guideline "+strconv.Quote(item.ID), item.ResponseCapabilityID, bundle.ResponseCapabilities); err != nil {
			return err
		}
		if err := validateResponseStyleProfileReference("guideline "+strconv.Quote(item.ID), item.StyleProfileID, bundle.ResponseStyleProfiles); err != nil {
			return err
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
			if strings.TrimSpace(state.Agent) != "" {
				if err := validateNonEmptyUnique("journey.state.agent", []string{state.Agent}); err != nil {
					return fmt.Errorf("journey %q state %q: %w", item.ID, state.ID, err)
				}
			}
			if err := validateResponseCapabilityReference("journey "+strconv.Quote(item.ID)+" state "+strconv.Quote(state.ID), state.ResponseCapabilityID, bundle.ResponseCapabilities); err != nil {
				return err
			}
			if err := validateResponseStyleProfileReference("journey "+strconv.Quote(item.ID)+" state "+strconv.Quote(state.ID), state.StyleProfileID, bundle.ResponseStyleProfiles); err != nil {
				return err
			}
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
			if err := validateNonEmptyUnique("journey guideline.agents", guideline.Agents); err != nil {
				return fmt.Errorf("journey %q guideline %q: %w", item.ID, guideline.ID, err)
			}
			if err := validateResponseCapabilityReference("journey guideline "+strconv.Quote(guideline.ID), guideline.ResponseCapabilityID, bundle.ResponseCapabilities); err != nil {
				return err
			}
			if err := validateResponseStyleProfileReference("journey guideline "+strconv.Quote(guideline.ID), guideline.StyleProfileID, bundle.ResponseStyleProfiles); err != nil {
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

func applyBundleDefaults(bundle policy.Bundle) policy.Bundle {
	if len(bundle.Semantics.Signals) == 0 && len(bundle.Semantics.Categories) == 0 && len(bundle.Semantics.Slots) == 0 {
		bundle.Semantics = defaultSemanticsPolicy()
	}
	if len(bundle.WatchCapabilities) == 0 {
		bundle.WatchCapabilities = defaultWatchCapabilities()
	}
	if strings.TrimSpace(bundle.QualityProfile.ID) == "" {
		bundle.QualityProfile = defaultQualityProfile(bundle.QualityProfile)
	}
	if strings.TrimSpace(bundle.LifecyclePolicy.ID) == "" {
		bundle.LifecyclePolicy = defaultLifecyclePolicy(bundle.LifecyclePolicy)
	}
	return bundle
}

func validateCapabilityIsolation(item policy.CapabilityIsolation) error {
	if err := validateNonEmptyUnique("capability_isolation.allowed_provider_ids", item.AllowedProviderIDs); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("capability_isolation.allowed_tool_ids", item.AllowedToolIDs); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("capability_isolation.allowed_agent_ids", item.AllowedAgentIDs); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("capability_isolation.allowed_retriever_ids", item.AllowedRetrieverIDs); err != nil {
		return err
	}
	seenScopes := map[string]struct{}{}
	for _, scope := range item.AllowedKnowledgeScopes {
		kind := strings.TrimSpace(scope.Kind)
		id := strings.TrimSpace(scope.ID)
		if kind == "" || id == "" {
			return errors.New("capability_isolation.allowed_knowledge_scopes requires kind and id")
		}
		key := kind + ":" + id
		if _, ok := seenScopes[key]; ok {
			return fmt.Errorf("capability_isolation.allowed_knowledge_scopes contains duplicate %q", key)
		}
		seenScopes[key] = struct{}{}
	}
	return nil
}

func validateSoul(soul policy.Soul, styles []policy.ResponseStyleProfile) error {
	if err := validateResponseStyleProfileReference("soul", soul.StyleProfileID, styles); err != nil {
		return err
	}
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

func validateResponseStyleProfiles(items []policy.ResponseStyleProfile) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if err := validateID("response_style_profile", item.ID, seen); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" tone.formality", item.Tone.Formality, "", "casual", "neutral", "professional", "formal"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" tone.warmth", item.Tone.Warmth, "", "low", "medium", "high"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" tone.directness", item.Tone.Directness, "", "low", "medium", "high"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" tone.empathy", item.Tone.Empathy, "", "low", "medium", "high"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" verbosity.overall", item.Verbosity.Overall, "", "concise", "balanced", "detailed"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" verbosity.explanation_depth", item.Verbosity.ExplanationDepth, "", "shallow", "medium", "deep"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" structure.paragraph_style", item.Structure.ParagraphStyle, "", "short", "medium", "long"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" structure.opening_style", item.Structure.OpeningStyle, "", "direct_answer_first", "context_first"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" structure.closing_style", item.Structure.ClosingStyle, "", "minimal", "inviting", "action_oriented"); err != nil {
			return err
		}
		if err := validateStyleField("response_style_profile "+strconv.Quote(item.ID)+" wording.hedging_level", item.Wording.HedgingLevel, "", "low", "medium", "high"); err != nil {
			return err
		}
		if item.Structure.MaxMessages < 0 {
			return fmt.Errorf("response_style_profile %q structure.max_messages cannot be negative", item.ID)
		}
		for idx, example := range item.Examples {
			if !hasTemplateMessages(example.Messages) {
				return fmt.Errorf("response_style_profile %q example %d requires messages", item.ID, idx)
			}
		}
	}
	return nil
}

func validateResponseStyleProfileReference(owner string, profileID string, items []policy.ResponseStyleProfile) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil
	}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == profileID {
			return nil
		}
	}
	return fmt.Errorf("%s references unknown style_profile_id %q", owner, profileID)
}

func validateStyleField(field string, value string, allowed ...string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return fmt.Errorf("%s has unsupported value %q", field, value)
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

func validateSemantics(sem policy.SemanticsPolicy) error {
	seen := map[string]struct{}{}
	for _, item := range sem.Signals {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("semantics.signals.id is required")
		}
		if err := validateID("semantic signal", item.ID, seen); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("semantics.signals.phrases", item.Phrases); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("semantics.signals.tokens", item.Tokens); err != nil {
			return err
		}
	}
	for _, item := range sem.Categories {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("semantics.categories.id is required")
		}
		if err := validateNonEmptyUnique("semantics.categories.signals", item.Signals); err != nil {
			return err
		}
	}
	for _, item := range sem.Slots {
		if strings.TrimSpace(item.Field) == "" || strings.TrimSpace(item.Kind) == "" {
			return errors.New("semantics.slots require field and kind")
		}
	}
	return validateNonEmptyUnique("semantics.relative_dates", sem.RelativeDates)
}

func validateWatchCapabilities(items []policy.WatchCapability) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if err := validateID("watch_capability", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.Kind) == "" {
			return fmt.Errorf("watch_capability %q requires kind", item.ID)
		}
		switch strings.TrimSpace(item.ScheduleStrategy) {
		case "", "poll", "reminder":
		default:
			return fmt.Errorf("watch_capability %q has unsupported schedule_strategy %q", item.ID, item.ScheduleStrategy)
		}
		if item.PollIntervalSeconds < 0 || item.ReminderLeadSeconds < 0 {
			return fmt.Errorf("watch_capability %q cannot use negative intervals", item.ID)
		}
		if err := validateNonEmptyUnique("watch_capability.trigger_signals", item.TriggerSignals); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.tool_match_terms", item.ToolMatchTerms); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.subject_keys", item.SubjectKeys); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.required_fields", item.RequiredFields); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.remind_at_keys", item.RemindAtKeys); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.status_keys", item.StatusKeys); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("watch_capability.stop_values", item.StopValues); err != nil {
			return err
		}
	}
	return nil
}

func validateDelegationContracts(items []policy.DelegationContract, watches []policy.WatchCapability) error {
	seen := map[string]struct{}{}
	watchIDs := map[string]struct{}{}
	for _, item := range watches {
		if strings.TrimSpace(item.ID) != "" {
			watchIDs[strings.TrimSpace(item.ID)] = struct{}{}
		}
	}
	for _, item := range items {
		if err := validateID("delegation_contract", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.ResourceType) == "" {
			return fmt.Errorf("delegation_contract %q requires resource_type", item.ID)
		}
		if err := validateNonEmptyUnique("delegation_contract.agent_ids", item.AgentIDs); err != nil {
			return fmt.Errorf("delegation_contract %q: %w", item.ID, err)
		}
		if err := validateNonEmptyUnique("delegation_contract.required_result_fields", item.RequiredResultKeys); err != nil {
			return fmt.Errorf("delegation_contract %q: %w", item.ID, err)
		}
		if strings.TrimSpace(item.WatchCapabilityID) != "" {
			if _, ok := watchIDs[strings.TrimSpace(item.WatchCapabilityID)]; !ok {
				return fmt.Errorf("delegation_contract %q references unknown watch_capability_id %q", item.ID, item.WatchCapabilityID)
			}
		}
		if err := validateDelegationFieldAliases("delegation_contract.field_aliases", item.ID, item.FieldAliases); err != nil {
			return err
		}
		if err := validateDelegationVerification(item); err != nil {
			return err
		}
	}
	return nil
}

func validateDelegationWorkflows(items []policy.DelegationWorkflow) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if err := validateID("delegation_workflow", item.ID, seen); err != nil {
			return err
		}
		stepSeen := map[string]struct{}{}
		for _, step := range item.Steps {
			if err := validateID("delegation_workflow step", step.ID, stepSeen); err != nil {
				return fmt.Errorf("delegation_workflow %q: %w", item.ID, err)
			}
			if strings.TrimSpace(step.Instruction) == "" {
				return fmt.Errorf("delegation_workflow %q step %q requires instruction", item.ID, step.ID)
			}
			if err := validateNonEmptyUnique("delegation_workflow.step.tool_ids", step.ToolIDs); err != nil {
				return fmt.Errorf("delegation_workflow %q step %q: %w", item.ID, step.ID, err)
			}
			for _, toolID := range step.ToolIDs {
				if !strings.Contains(strings.TrimSpace(toolID), ".") {
					return fmt.Errorf("delegation_workflow %q step %q requires provider-qualified tool_id %q", item.ID, step.ID, toolID)
				}
			}
		}
		if err := validateNonEmptyUnique("delegation_workflow.constraints", item.Constraints); err != nil {
			return fmt.Errorf("delegation_workflow %q: %w", item.ID, err)
		}
		if err := validateNonEmptyUnique("delegation_workflow.success_criteria", item.SuccessCriteria); err != nil {
			return fmt.Errorf("delegation_workflow %q: %w", item.ID, err)
		}
	}
	return nil
}

func validateResponseCapabilities(items []policy.ResponseCapability) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if err := validateID("response_capability", item.ID, seen); err != nil {
			return err
		}
		mode := strings.TrimSpace(item.Mode)
		switch mode {
		case "", "always", "fallback_only":
		default:
			return fmt.Errorf("response_capability %q has unsupported mode %q", item.ID, item.Mode)
		}
		factKeys := map[string]struct{}{}
		for _, fact := range item.Facts {
			key := strings.TrimSpace(fact.Key)
			if key == "" {
				return fmt.Errorf("response_capability %q requires fact key", item.ID)
			}
			if _, ok := factKeys[key]; ok {
				return fmt.Errorf("response_capability %q contains duplicate fact key %q", item.ID, key)
			}
			factKeys[key] = struct{}{}
			if len(fact.Sources) == 0 {
				return fmt.Errorf("response_capability %q fact %q requires at least one source", item.ID, key)
			}
			for _, source := range fact.Sources {
				if !strings.Contains(strings.TrimSpace(source.ToolID), ".") {
					return fmt.Errorf("response_capability %q fact %q requires provider-qualified tool_id %q", item.ID, key, source.ToolID)
				}
				if strings.TrimSpace(source.Path) == "" {
					return fmt.Errorf("response_capability %q fact %q requires source path", item.ID, key)
				}
			}
		}
		if err := validateNonEmptyUnique("response_capability.instructions", item.Instructions); err != nil {
			return fmt.Errorf("response_capability %q: %w", item.ID, err)
		}
		for idx, example := range item.Examples {
			if len(example.Messages) == 0 {
				return fmt.Errorf("response_capability %q example %d requires messages", item.ID, idx)
			}
			for key := range example.Facts {
				if _, ok := factKeys[strings.TrimSpace(key)]; !ok {
					return fmt.Errorf("response_capability %q example %d references unknown fact %q", item.ID, idx, key)
				}
			}
		}
		for idx, message := range item.DeterministicFallback.Messages {
			if strings.TrimSpace(message.Text) == "" {
				return fmt.Errorf("response_capability %q deterministic_fallback message %d requires text", item.ID, idx)
			}
			for _, key := range extractFactTemplateKeys(message.Text) {
				if _, ok := factKeys[key]; !ok {
					return fmt.Errorf("response_capability %q deterministic_fallback message %d references unknown fact %q", item.ID, idx, key)
				}
			}
			if containsUnsupportedTemplateRef(message.Text) {
				return fmt.Errorf("response_capability %q deterministic_fallback message %d may only reference {{facts.<key>}}", item.ID, idx)
			}
			for _, key := range message.WhenPresent {
				if _, ok := factKeys[strings.TrimSpace(key)]; !ok {
					return fmt.Errorf("response_capability %q deterministic_fallback message %d references unknown when_present fact %q", item.ID, idx, key)
				}
			}
		}
	}
	return nil
}

func validateResponseCapabilityReference(owner string, capabilityID string, items []policy.ResponseCapability) error {
	capabilityID = strings.TrimSpace(capabilityID)
	if capabilityID == "" {
		return nil
	}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == capabilityID {
			return nil
		}
	}
	return fmt.Errorf("%s references unknown response_capability_id %q", owner, capabilityID)
}

func extractFactTemplateKeys(text string) []string {
	var keys []string
	for {
		start := strings.Index(text, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(text[start+2:], "}}")
		if end < 0 {
			break
		}
		token := strings.TrimSpace(text[start+2 : start+2+end])
		if strings.HasPrefix(token, "facts.") {
			keys = append(keys, strings.TrimSpace(strings.TrimPrefix(token, "facts.")))
		}
		text = text[start+2+end+2:]
	}
	return keys
}

func containsUnsupportedTemplateRef(text string) bool {
	for {
		start := strings.Index(text, "{{")
		if start < 0 {
			return false
		}
		end := strings.Index(text[start+2:], "}}")
		if end < 0 {
			return true
		}
		token := strings.TrimSpace(text[start+2 : start+2+end])
		if !strings.HasPrefix(token, "facts.") {
			return true
		}
		text = text[start+2+end+2:]
	}
}

func validateDelegationVerification(item policy.DelegationContract) error {
	verification := item.Verification
	if strings.TrimSpace(verification.PrimaryToolID) == "" && len(verification.FallbackTools) == 0 && len(verification.ExtractPaths) == 0 {
		return nil
	}
	if err := validateDelegationFieldAliases("delegation_contract.verification.extract_paths", item.ID, verification.ExtractPaths); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("delegation_contract.verification.require_match_on", verification.RequireMatchOn); err != nil {
		return fmt.Errorf("delegation_contract %q: %w", item.ID, err)
	}
	for _, tool := range verification.FallbackTools {
		if strings.TrimSpace(tool.ToolID) == "" {
			return fmt.Errorf("delegation_contract %q fallback_tools requires tool_id", item.ID)
		}
	}
	return nil
}

func validateDelegationFieldAliases(field string, contractID string, aliases []policy.DelegationFieldAlias) error {
	seen := map[string]struct{}{}
	for _, item := range aliases {
		target := strings.TrimSpace(item.Target)
		if target == "" {
			return fmt.Errorf("delegation_contract %q %s requires target", contractID, field)
		}
		if _, ok := seen[target]; ok {
			return fmt.Errorf("delegation_contract %q %s contains duplicate target %q", contractID, field, target)
		}
		seen[target] = struct{}{}
		if err := validateNonEmptyUnique(field+".sources", item.Sources); err != nil {
			return fmt.Errorf("delegation_contract %q: %w", contractID, err)
		}
	}
	return nil
}

func validateGuidelineAgentBindings(bundle policy.Bundle) error {
	workflowIDs := map[string]struct{}{}
	for _, item := range bundle.DelegationWorkflows {
		workflowIDs[strings.TrimSpace(item.ID)] = struct{}{}
	}
	validate := func(owner string, agents []string, bindings []policy.GuidelineAgentBinding) error {
		seenAgents := map[string]struct{}{}
		for _, agentID := range agents {
			agentID = strings.TrimSpace(agentID)
			if agentID == "" {
				continue
			}
			seenAgents[agentID] = struct{}{}
		}
		for _, binding := range bindings {
			agentID := strings.TrimSpace(binding.AgentID)
			if agentID == "" {
				return fmt.Errorf("%s agent_binding requires agent_id", owner)
			}
			if _, ok := seenAgents[agentID]; ok {
				return fmt.Errorf("%s agent %q cannot appear in both agents and agent_bindings", owner, agentID)
			}
			seenAgents[agentID] = struct{}{}
			workflowID := strings.TrimSpace(binding.WorkflowID)
			if workflowID == "" {
				return fmt.Errorf("%s agent_binding for %q requires workflow_id", owner, agentID)
			}
			if _, ok := workflowIDs[workflowID]; !ok {
				return fmt.Errorf("%s agent_binding for %q references unknown workflow_id %q", owner, agentID, workflowID)
			}
		}
		return nil
	}
	for _, guideline := range bundle.Guidelines {
		if err := validate("guideline "+strconv.Quote(guideline.ID), guideline.Agents, guideline.AgentBindings); err != nil {
			return err
		}
	}
	for _, journey := range bundle.Journeys {
		for _, guideline := range journey.Guidelines {
			if err := validate("journey guideline "+strconv.Quote(guideline.ID), guideline.Agents, guideline.AgentBindings); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQualityProfile(profile policy.QualityProfile) error {
	if profile.MinimumOverall < 0 || profile.MinimumOverall > 1 {
		return errors.New("quality_profile.minimum_overall must be between 0 and 1")
	}
	if err := validateNonEmptyUnique("quality_profile.allowed_commitments", profile.AllowedCommitments); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("quality_profile.required_evidence", profile.RequiredEvidence); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("quality_profile.required_verification_steps", profile.RequiredVerificationSteps); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("quality_profile.high_risk_indicators", profile.HighRiskIndicators); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("quality_profile.refusal_signals", profile.RefusalSignals); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("quality_profile.escalation_signals", profile.EscalationSignals); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, item := range profile.ClaimProfiles {
		if err := validateID("quality_profile.claim_profile", item.ID, seen); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.match_terms", item.MatchTerms); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.required_evidence", item.RequiredEvidence); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.required_verification", item.RequiredVerification); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.allowed_commitments", item.AllowedCommitments); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.verification_qualifiers", item.VerificationQualifiers); err != nil {
			return err
		}
		if err := validateNonEmptyUnique("quality_profile.claim_profile.contradiction_markers", item.ContradictionMarkers); err != nil {
			return err
		}
	}
	for concept, aliases := range profile.SemanticConcepts {
		if strings.TrimSpace(concept) == "" {
			return errors.New("quality_profile.semantic_concepts requires non-empty concept keys")
		}
		if err := validateNonEmptyUnique("quality_profile.semantic_concepts."+concept, aliases); err != nil {
			return err
		}
	}
	return nil
}

func validateLifecyclePolicy(policyDef policy.LifecyclePolicy) error {
	if policyDef.IdleCandidateAfterMS < 0 || policyDef.AwaitingCloseAfterMS < 0 || policyDef.KeepRecheckAfterMS < 0 {
		return errors.New("lifecycle_policy timeouts cannot be negative")
	}
	if err := validateNonEmptyUnique("lifecycle_policy.resolution_signals", policyDef.ResolutionSignals); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("lifecycle_policy.delivery_update_signals", policyDef.DeliveryUpdateSignals); err != nil {
		return err
	}
	return validateNonEmptyUnique("lifecycle_policy.appointment_reminder_signals", policyDef.AppointmentReminderSignals)
}

func defaultSemanticsPolicy() policy.SemanticsPolicy {
	return policy.SemanticsPolicy{
		Signals: []policy.SemanticSignal{
			{ID: "return_status", Phrases: []string{"return status", "tracking"}, Tokens: []string{"refund", "return", "damaged", "cancel", "order"}},
			{ID: "order_status", Phrases: []string{"order status", "status is"}},
			{ID: "scheduling", Tokens: []string{"schedule", "appointment", "booking", "book", "reschedule"}},
			{ID: "delivery", Phrases: []string{"delivery", "shipping"}, Tokens: []string{"delivery", "shipping", "tracking"}},
			{ID: "confirmation", Tokens: []string{"confirm", "confirmation", "notify", "email"}},
		},
		Categories: []policy.SemanticCategory{
			{ID: "scheduling", Signals: []string{"scheduling"}},
			{ID: "confirmation", Signals: []string{"confirmation"}},
		},
		Slots: []policy.SemanticSlot{
			{Field: "destination", Kind: "destination", Markers: []string{"to"}, StopTokens: []string{"today", "tomorrow", "next", "return", "for"}},
			{Field: "product_name", Kind: "product_like", Markers: []string{"for a", "for an", "for the", "for"}, StopTokens: []string{"today", "tomorrow", "next", "with", "from", "to"}},
		},
		RelativeDates: []string{"today", "tomorrow", "next week", "next month", "return in"},
	}
}

func defaultWatchCapabilities() []policy.WatchCapability {
	return []policy.WatchCapability{
		{
			ID:                     "delivery_status_watch",
			Kind:                   "delivery_status",
			ScheduleStrategy:       "poll",
			TriggerSignals:         []string{"delivery", "tracking", "order_status", "return_status"},
			ToolMatchTerms:         []string{"order", "delivery", "shipping", "tracking"},
			SubjectKeys:            []string{"order_id", "tracking_id", "shipment_id", "package_id", "id"},
			StatusKeys:             []string{"delivery_status", "status", "state", "tracking_status"},
			PollIntervalSeconds:    int((15 * time.Minute) / time.Second),
			StopCondition:          "delivered",
			StopValues:             []string{"delivered"},
			AllowLifecycleFallback: true,
			DeliveryTemplate:       "I have an update on your delivery status: {{status}}.",
		},
		{
			ID:                     "appointment_reminder_watch",
			Kind:                   "appointment_reminder",
			ScheduleStrategy:       "reminder",
			TriggerSignals:         []string{"scheduling", "appointment_reminder"},
			ToolMatchTerms:         []string{"appointment", "schedule", "calendar", "booking"},
			SubjectKeys:            []string{"appointment_id", "booking_id", "id"},
			RequiredFields:         []string{"appointment_at"},
			RemindAtKeys:           []string{"appointment_at", "scheduled_for", "starts_at", "remind_at", "time", "date"},
			ReminderLeadSeconds:    3600,
			AllowLifecycleFallback: true,
			DeliveryTemplate:       "This is your reminder about the appointment scheduled for {{appointment_at}}.",
		},
	}
}

func defaultQualityProfile(existing policy.QualityProfile) policy.QualityProfile {
	if strings.TrimSpace(existing.ID) == "" {
		existing.ID = "default_quality_profile"
	}
	if strings.TrimSpace(existing.RiskTier) == "" {
		existing.RiskTier = "medium"
	}
	if len(existing.AllowedCommitments) == 0 {
		existing.AllowedCommitments = []string{"cautious policy-backed guidance"}
	}
	if len(existing.RequiredEvidence) == 0 {
		existing.RequiredEvidence = []string{"matched_guideline"}
	}
	if len(existing.HighRiskIndicators) == 0 {
		existing.HighRiskIndicators = []string{
			"within 30 days",
			"instant replacement",
			"guarantee",
			"guaranteed",
			"refund",
			"replacement",
			"approved",
			"eligible",
			"qualify",
			"qualifies",
		}
	}
	if len(existing.ClaimProfiles) == 0 {
		existing.ClaimProfiles = []policy.QualityClaimProfile{
			{ID: "refund_commitment", MatchTerms: []string{"refund", "reimbursement", "credit"}, Risk: "high", RequiredEvidence: []string{"policy_or_knowledge", "approval"}, RequiredVerification: []string{"review", "verification"}, AllowedCommitments: []string{"verification_first"}, VerificationQualifiers: []string{"after verification", "after review", "pending review", "requires review"}, ContradictionMarkers: []string{"not eligible", "requires review", "after review", "before review"}},
			{ID: "replacement_commitment", MatchTerms: []string{"replacement", "replace", "exchange", "swap"}, Risk: "high", RequiredEvidence: []string{"policy_or_knowledge", "approval"}, RequiredVerification: []string{"review", "verification"}, AllowedCommitments: []string{"verification_first"}, VerificationQualifiers: []string{"after verification", "after review", "pending review", "requires review"}, ContradictionMarkers: []string{"not eligible", "requires review", "after review", "before review"}},
			{ID: "approval_commitment", MatchTerms: []string{"approved", "approval", "authorized", "authorization"}, Risk: "high", RequiredEvidence: []string{"approval", "matched_guideline"}, RequiredVerification: []string{"approval", "review"}, AllowedCommitments: []string{"approval_required"}, VerificationQualifiers: []string{"after approval", "pending approval", "requires approval"}, ContradictionMarkers: []string{"requires approval", "pending approval", "not approved"}},
			{ID: "escalation_commitment", MatchTerms: []string{"escalat", "handoff", "human operator", "operator review"}, Risk: "medium", RequiredEvidence: []string{"escalation", "matched_guideline"}, RequiredVerification: []string{"handoff"}, AllowedCommitments: []string{"safe_escalation"}, VerificationQualifiers: []string{"for review", "to a human", "to an operator"}, ContradictionMarkers: []string{"cannot escalate", "no escalation"}},
			{ID: "eligibility", MatchTerms: []string{"eligible", "eligibility", "qualify", "qualifies"}, Risk: "high", RequiredEvidence: []string{"eligibility", "policy_or_knowledge"}, RequiredVerification: []string{"review", "verification"}, AllowedCommitments: []string{"verification_first"}, VerificationQualifiers: []string{"after verification", "after review", "pending review"}, ContradictionMarkers: []string{"not eligible", "requires review", "after review"}},
			{ID: "timeline", MatchTerms: []string{"within ", " day", " days", "hour", "hours", "timeline", "window"}, Risk: "medium", RequiredEvidence: []string{"timeline", "policy_or_knowledge"}, RequiredVerification: []string{"review"}, AllowedCommitments: []string{"cautious policy-backed guidance"}, VerificationQualifiers: []string{"after verification", "after review", "once approved"}, ContradictionMarkers: []string{"after verification", "after review", "timeline may vary"}},
			{ID: "preference", MatchTerms: []string{"call me", "prefer", "preferred", "instead"}, Risk: "medium", RequiredEvidence: []string{"customer_preference"}, AllowedCommitments: []string{"preference_following"}},
		}
	}
	if existing.SemanticConcepts == nil {
		existing.SemanticConcepts = map[string][]string{
			"refund":       {"refund", "refunds", "refunded", "reimbursement", "reimburse", "reimbursed", "credit", "credited"},
			"replacement":  {"replacement", "replacements", "replace", "replaced", "exchange", "exchanges", "swap", "swapped"},
			"eligibility":  {"eligible", "eligibility", "qualify", "qualifies", "qualified", "qualifying"},
			"approval":     {"approval", "approve", "approved", "authorization", "authorize", "authorized"},
			"verification": {"review", "reviewed", "verification", "verify", "verified", "confirm", "confirmed", "validation", "validate", "validated"},
			"immediate":    {"instant", "immediate", "immediately", "right", "away"},
			"promise":      {"guarantee", "guaranteed", "promise", "promised", "commit", "committed"},
			"timeline":     {"timeline", "deadline", "window", "days", "day", "hours", "hour"},
			"escalation":   {"escalate", "escalation", "handoff", "operator", "human"},
		}
	}
	if len(existing.RefusalSignals) == 0 {
		existing.RefusalSignals = []string{"cannot", "can't", "not", "unable", "safe", "instead", "but i can", "can help"}
	}
	if len(existing.EscalationSignals) == 0 {
		existing.EscalationSignals = []string{"human", "operator", "escalat", "handoff", "review"}
	}
	if existing.BlueprintRules == nil {
		existing.BlueprintRules = map[string][]string{
			"refund_replacement": {
				"Start by stating what still must be verified before any refund or replacement decision.",
				"Do not imply approval before the required review or verification step is complete.",
			},
			"approval": {
				"State the review or approval requirement before suggesting the outcome is complete.",
			},
			"escalation": {
				"State the handoff need clearly and give the next step.",
			},
		}
	}
	if existing.MinimumOverall == 0 {
		existing.MinimumOverall = 0.7
	}
	return existing
}

func defaultLifecyclePolicy(existing policy.LifecyclePolicy) policy.LifecyclePolicy {
	if strings.TrimSpace(existing.ID) == "" {
		existing.ID = "default_lifecycle_policy"
	}
	if existing.IdleCandidateAfterMS <= 0 {
		existing.IdleCandidateAfterMS = int((30 * time.Minute) / time.Millisecond)
	}
	if existing.AwaitingCloseAfterMS <= 0 {
		existing.AwaitingCloseAfterMS = int((12 * time.Hour) / time.Millisecond)
	}
	if existing.KeepRecheckAfterMS <= 0 {
		existing.KeepRecheckAfterMS = int((30 * time.Minute) / time.Millisecond)
	}
	if strings.TrimSpace(existing.FollowupMessage) == "" {
		existing.FollowupMessage = "Do you need any more help with this?"
	}
	if len(existing.ResolutionSignals) == 0 {
		existing.ResolutionSignals = []string{"thanks", "thank you", "that helps", "all good", "solved", "ok got it"}
	}
	if len(existing.DeliveryUpdateSignals) == 0 {
		existing.DeliveryUpdateSignals = []string{"update me", "keep me updated", "notify me", "let me know", "delivery", "shipping", "order status", "package"}
	}
	if len(existing.AppointmentReminderSignals) == 0 {
		existing.AppointmentReminderSignals = []string{"remind me", "appointment reminder", "reminder for my appointment", "notify me about my appointment"}
	}
	return existing
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

func compileGuidelineAgentAssociations(bundle policy.Bundle) []policy.GuidelineAgentAssociation {
	seen := map[string]struct{}{}
	var out []policy.GuidelineAgentAssociation
	add := func(guidelineID, agentID, workflowID string) {
		guidelineID = strings.TrimSpace(guidelineID)
		agentID = strings.TrimSpace(agentID)
		workflowID = strings.TrimSpace(workflowID)
		if guidelineID == "" || agentID == "" {
			return
		}
		key := guidelineID + "::" + agentID + "::" + workflowID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, policy.GuidelineAgentAssociation{
			GuidelineID: guidelineID,
			AgentID:     agentID,
			WorkflowID:  workflowID,
		})
	}

	for _, guideline := range bundle.Guidelines {
		for _, agentID := range guideline.Agents {
			add(guideline.ID, agentID, "")
		}
		for _, binding := range guideline.AgentBindings {
			add(guideline.ID, binding.AgentID, binding.WorkflowID)
		}
	}
	for _, flow := range bundle.Journeys {
		for _, guideline := range flow.Guidelines {
			for _, agentID := range guideline.Agents {
				add(guideline.ID, agentID, "")
			}
			for _, binding := range guideline.AgentBindings {
				add(guideline.ID, binding.AgentID, binding.WorkflowID)
			}
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
