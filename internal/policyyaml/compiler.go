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
	bundle = applyBundleDefaults(bundle)

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
	if err := validateSemantics(bundle.Semantics); err != nil {
		return err
	}
	if err := validateWatchCapabilities(bundle.WatchCapabilities); err != nil {
		return err
	}
	if err := validateQualityProfile(bundle.QualityProfile); err != nil {
		return err
	}
	if err := validateLifecyclePolicy(bundle.LifecyclePolicy); err != nil {
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
