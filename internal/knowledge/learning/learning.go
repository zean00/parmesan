package learning

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store"
)

type Learner struct {
	repo store.Repository
}

func New(repo store.Repository) *Learner {
	return &Learner{repo: repo}
}

func (l *Learner) LearnFromSession(ctx context.Context, sess session.Session, exec execution.TurnExecution, events []session.Event, signals []media.DerivedSignal) error {
	if l == nil || l.repo == nil {
		return nil
	}
	if err := l.learnCustomerFacts(ctx, sess, exec, events); err != nil {
		return err
	}
	if err := l.proposeSharedKnowledge(ctx, sess, exec, events, signals); err != nil {
		return err
	}
	return nil
}

func (l *Learner) CompileFeedback(ctx context.Context, record feedback.Record, sess session.Session, events []session.Event, signals []media.DerivedSignal) (feedback.Outputs, error) {
	var out feedback.Outputs
	if l == nil || l.repo == nil {
		return out, nil
	}
	record.Text = strings.TrimSpace(record.Text)
	if record.Text == "" {
		out.Unclassified = append(out.Unclassified, "empty feedback")
		return out, nil
	}
	for _, finding := range preferenceFindings(record.Text) {
		pref, event, err := l.preferenceRecord(sess, record.ExecutionID, record.TraceID, "operator_feedback", finding)
		if err != nil {
			return out, err
		}
		if pref.ID == "" {
			out.Unclassified = append(out.Unclassified, "preference feedback requires session agent_id and customer_id")
			continue
		}
		if err := l.repo.SaveCustomerPreference(ctx, pref, event); err != nil {
			return out, err
		}
		out.PreferenceIDs = append(out.PreferenceIDs, pref.ID)
		out.PreferenceEventIDs = append(out.PreferenceEventIDs, event.ID)
	}
	category := strings.ToLower(strings.TrimSpace(record.Category + " " + strings.Join(record.Labels, " ") + " " + record.Text))
	switch {
	case isSoulFeedback(category):
		proposalID, err := l.proposePolicyChange(ctx, sess, record, true)
		if err != nil {
			return out, err
		}
		if proposalID != "" {
			out.PolicyProposalIDs = append(out.PolicyProposalIDs, proposalID)
		}
	case isPolicyFeedback(category):
		proposalID, err := l.proposePolicyChange(ctx, sess, record, false)
		if err != nil {
			return out, err
		}
		if proposalID != "" {
			out.PolicyProposalIDs = append(out.PolicyProposalIDs, proposalID)
		}
	case isKnowledgeFeedback(category):
		proposalID, err := l.proposeKnowledgeFromFeedback(ctx, sess, record, signals)
		if err != nil {
			return out, err
		}
		if proposalID != "" {
			out.KnowledgeProposalIDs = append(out.KnowledgeProposalIDs, proposalID)
		}
	default:
		if len(out.PreferenceIDs) == 0 {
			out.Unclassified = append(out.Unclassified, "feedback did not match preference, knowledge, policy, or soul compiler rules")
		}
	}
	return out, nil
}

var (
	rePrefer = regexp.MustCompile(`(?i)\bi prefer ([^.!\n]+)`)
	reCallMe = regexp.MustCompile(`(?i)\bcall me ([^.!\n]+)`)
	reName   = regexp.MustCompile(`(?i)\bmy name is ([^.!\n]+)`)
)

func (l *Learner) learnCustomerFacts(ctx context.Context, sess session.Session, exec execution.TurnExecution, events []session.Event) error {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return nil
	}
	for _, event := range events {
		if event.Source != "customer" {
			continue
		}
		for _, part := range event.Content {
			if part.Type != "text" || strings.TrimSpace(part.Text) == "" {
				continue
			}
			for _, finding := range preferenceFindings(part.Text) {
				pref, prefEvent, err := l.preferenceRecord(sess, exec.ID, exec.TraceID, "conversation_explicit", finding)
				if err != nil {
					return err
				}
				if prefEvent.Metadata == nil {
					prefEvent.Metadata = map[string]any{}
				}
				prefEvent.Metadata["event_id"] = event.ID
				if err := l.repo.SaveCustomerPreference(ctx, pref, prefEvent); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (l *Learner) preferenceRecord(sess session.Session, executionID, traceID, source string, finding preferenceFinding) (customer.Preference, customer.PreferenceEvent, error) {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return customer.Preference{}, customer.PreferenceEvent{}, nil
	}
	now := time.Now().UTC()
	evidence := []string{"session:" + sess.ID}
	if strings.TrimSpace(traceID) != "" {
		evidence = append(evidence, "trace:"+traceID)
	}
	if strings.TrimSpace(executionID) != "" {
		evidence = append(evidence, "execution:"+executionID)
	}
	prefID := stableID("cpref", sess.AgentID, sess.CustomerID, finding.Key)
	return customer.Preference{
			ID:              prefID,
			AgentID:         strings.TrimSpace(sess.AgentID),
			CustomerID:      strings.TrimSpace(sess.CustomerID),
			Key:             finding.Key,
			Value:           finding.Value,
			Source:          source,
			Confidence:      1,
			Status:          "active",
			EvidenceRefs:    evidence,
			Metadata:        map[string]any{"compiler": "deterministic"},
			LastConfirmedAt: &now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}, customer.PreferenceEvent{
			ID:           stableID("cpevt", prefID, source, finding.Value, now.Format(time.RFC3339Nano)),
			PreferenceID: prefID,
			AgentID:      strings.TrimSpace(sess.AgentID),
			CustomerID:   strings.TrimSpace(sess.CustomerID),
			Key:          finding.Key,
			Value:        finding.Value,
			Action:       "upsert",
			Source:       source,
			Confidence:   1,
			EvidenceRefs: evidence,
			Metadata:     map[string]any{"compiler": "deterministic"},
			CreatedAt:    now,
		}, nil
}

func (l *Learner) proposeSharedKnowledge(ctx context.Context, sess session.Session, exec execution.TurnExecution, events []session.Event, signals []media.DerivedSignal) error {
	var notes []string
	for _, event := range events {
		if event.Kind != "operator.note" {
			continue
		}
		for _, part := range event.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				notes = append(notes, strings.TrimSpace(part.Text))
			}
		}
	}
	if len(notes) == 0 && len(signals) == 0 {
		return nil
	}
	scopeKind, scopeID := sharedScope(sess)
	if scopeID == "" {
		return nil
	}
	now := time.Now().UTC()
	return l.repo.SaveKnowledgeUpdateProposal(ctx, knowledge.UpdateProposal{
		ID:        stableID("kprop", scopeKind, scopeID, exec.ID),
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		Kind:      "conversation_insight",
		State:     "draft",
		Rationale: "Conversation and operator evidence suggested a shared knowledge update.",
		Evidence:  []knowledge.Citation{{URI: "session:" + sess.ID, Anchor: exec.TraceID, Title: "Conversation trace"}},
		Payload: map[string]any{
			"session_id":     sess.ID,
			"execution_id":   exec.ID,
			"trace_id":       exec.TraceID,
			"operator_notes": notes,
			"signals":        signalPayloads(signals),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (l *Learner) proposeKnowledgeFromFeedback(ctx context.Context, sess session.Session, record feedback.Record, signals []media.DerivedSignal) (string, error) {
	scopeKind, scopeID := sharedScope(sess)
	if scopeID == "" {
		return "", nil
	}
	now := time.Now().UTC()
	id := stableID("kprop", scopeKind, scopeID, record.ID, record.Text)
	item := knowledge.UpdateProposal{
		ID:        id,
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		Kind:      "operator_feedback",
		State:     "draft",
		Rationale: "Operator feedback suggested a shared knowledge update.",
		Evidence:  []knowledge.Citation{{URI: "session:" + sess.ID, Anchor: record.TraceID, Title: "Operator feedback"}},
		Payload: map[string]any{
			"session_id":        sess.ID,
			"execution_id":      record.ExecutionID,
			"trace_id":          record.TraceID,
			"feedback_id":       record.ID,
			"operator_id":       record.OperatorID,
			"operator_feedback": record.Text,
			"signals":           signalPayloads(signals),
			"title":             "Operator feedback",
			"body":              record.Text,
			"operation":         "append",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return id, l.repo.SaveKnowledgeUpdateProposal(ctx, item)
}

func (l *Learner) proposePolicyChange(ctx context.Context, sess session.Session, record feedback.Record, soulOnly bool) (string, error) {
	base, ok := l.baseBundle(ctx, sess)
	if !ok {
		return "", nil
	}
	now := time.Now().UTC()
	short := stableChecksum(record.ID + "\x00" + record.Text)[:12]
	candidate := base
	candidate.ID = base.ID + "_feedback_" + short
	candidate.Version = strings.TrimSpace(base.Version + "+feedback." + short)
	candidate.ImportedAt = now
	candidate.SourceYAML = base.SourceYAML
	if soulOnly {
		rule := "Operator feedback: " + record.Text
		if !containsString(candidate.Soul.StyleRules, rule) {
			candidate.Soul.StyleRules = append(candidate.Soul.StyleRules, rule)
		}
	} else {
		candidate.Guidelines = append(candidate.Guidelines, policy.Guideline{
			ID:    "feedback_" + short,
			When:  "operator feedback applies",
			Then:  record.Text,
			Scope: "operator_feedback",
		})
	}
	if err := l.repo.SaveBundle(ctx, candidate); err != nil {
		return "", err
	}
	proposalID := stableID("proposal", base.ID, candidate.ID, record.ID)
	proposal := rollout.Proposal{
		ID:                     proposalID,
		SourceBundleID:         base.ID,
		CandidateBundleID:      candidate.ID,
		State:                  rollout.StateProposed,
		Rationale:              "Operator feedback compiler created a draft " + proposalKindLabel(soulOnly) + " proposal.",
		EvidenceRefs:           []string{"session:" + sess.ID, "feedback:" + record.ID},
		RiskFlags:              []string{"operator_feedback"},
		RequiresManualApproval: true,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if strings.TrimSpace(record.TraceID) != "" {
		proposal.EvidenceRefs = append(proposal.EvidenceRefs, "trace:"+record.TraceID)
	}
	return proposalID, l.repo.SaveProposal(ctx, proposal)
}

func (l *Learner) baseBundle(ctx context.Context, sess session.Session) (policy.Bundle, bool) {
	bundles, err := l.repo.ListBundles(ctx)
	if err != nil || len(bundles) == 0 {
		return policy.Bundle{}, false
	}
	if strings.TrimSpace(sess.AgentID) != "" {
		if profile, err := l.repo.GetAgentProfile(ctx, sess.AgentID); err == nil && strings.TrimSpace(profile.DefaultPolicyBundleID) != "" {
			for _, bundle := range bundles {
				if bundle.ID == profile.DefaultPolicyBundleID {
					return bundle, true
				}
			}
		}
	}
	return bundles[0], true
}

type preferenceFinding struct {
	Key   string
	Value string
}

func preferenceFindings(text string) []preferenceFinding {
	text = strings.TrimSpace(text)
	var out []preferenceFinding
	for _, item := range []struct {
		re  *regexp.Regexp
		key func(string) string
	}{
		{rePrefer, func(value string) string { return "preference." + stableChecksum(strings.ToLower(value))[:12] }},
		{reCallMe, func(string) string { return "preferred_name" }},
		{reName, func(string) string { return "name" }},
	} {
		match := item.re.FindStringSubmatch(text)
		if len(match) != 2 {
			continue
		}
		value := strings.TrimSpace(match[1])
		if value == "" {
			continue
		}
		out = append(out, preferenceFinding{Key: item.key(value), Value: value})
	}
	return out
}

func isKnowledgeFeedback(text string) bool {
	return containsAny(text, "knowledge", "fact", "docs", "documentation", "product", "faq", "manual", "article")
}

func isPolicyFeedback(text string) bool {
	return containsAny(text, "policy", "guideline", "journey", "scenario", "guardrail", "rule", "behavior", "approval", "escalate")
}

func isSoulFeedback(text string) bool {
	return containsAny(text, "soul", "persona", "tone", "voice", "brand", "style", "language", "verbosity", "formal", "handoff")
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func proposalKindLabel(soulOnly bool) string {
	if soulOnly {
		return "SOUL"
	}
	return "policy"
}

func sharedScope(sess session.Session) (string, string) {
	if strings.TrimSpace(sess.AgentID) != "" {
		return "agent", strings.TrimSpace(sess.AgentID)
	}
	return "", ""
}

func signalPayloads(signals []media.DerivedSignal) []map[string]any {
	out := make([]map[string]any, 0, len(signals))
	for _, signal := range signals {
		out = append(out, map[string]any{
			"kind":      signal.Kind,
			"value":     signal.Value,
			"extractor": signal.Extractor,
		})
	}
	return out
}

func stableID(prefix string, parts ...string) string {
	return prefix + "_" + stableChecksum(strings.Join(parts, "\x00"))[:16]
}

func stableChecksum(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}
