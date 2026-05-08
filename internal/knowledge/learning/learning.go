package learning

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store"
)

type Learner struct {
	repo   store.Repository
	router *model.Router
}

func New(repo store.Repository) *Learner {
	return &Learner{repo: repo}
}

func NewWithRouter(repo store.Repository, router *model.Router) *Learner {
	return &Learner{repo: repo, router: router}
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
	learningText := strings.TrimSpace(feedbackLearningFocus(record))
	if learningText == "" {
		if record.Score != nil {
			return out, nil
		}
		out.Unclassified = append(out.Unclassified, "empty feedback")
		return out, nil
	}
	for _, finding := range l.normalizeMemoryFindings(ctx, learningText, time.Now().UTC()) {
		item, event, err := l.memoryRecord(ctx, sess, record.ExecutionID, record.TraceID, "operator_feedback", finding)
		if err != nil {
			return out, err
		}
		if item.ID == "" {
			if event.ID != "" {
				if err := l.repo.AppendCustomerMemoryEvent(ctx, event); err != nil {
					return out, err
				}
				if finding.Category == customer.MemoryCategoryPreference {
					out.PreferenceEventIDs = append(out.PreferenceEventIDs, "cpevt_"+strings.TrimPrefix(event.ID, "cmemt_"))
				}
				continue
			}
			out.Unclassified = append(out.Unclassified, "memory feedback requires session agent_id and customer_id")
			continue
		}
		if err := l.repo.SaveCustomerMemoryItem(ctx, item, event); err != nil {
			return out, err
		}
		if item.Category == customer.MemoryCategoryPreference {
			out.PreferenceIDs = append(out.PreferenceIDs, "cpref_"+strings.TrimPrefix(item.ID, "cmem_"))
			out.PreferenceEventIDs = append(out.PreferenceEventIDs, "cpevt_"+strings.TrimPrefix(event.ID, "cmemt_"))
		}
	}
	category := strings.ToLower(strings.TrimSpace(record.Category + " " + strings.Join(record.Labels, " ") + " " + learningText))
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
		if record.IsResponseScoped() && strings.TrimSpace(record.Correction) != "" && len(out.PreferenceIDs) == 0 {
			proposalID, err := l.proposeKnowledgeFromFeedback(ctx, sess, record, signals)
			if err != nil {
				return out, err
			}
			if proposalID != "" {
				out.KnowledgeProposalIDs = append(out.KnowledgeProposalIDs, proposalID)
				break
			}
		}
		if len(out.PreferenceIDs) == 0 {
			out.Unclassified = append(out.Unclassified, "feedback did not match preference, knowledge, policy, or soul compiler rules")
		}
	}
	return out, nil
}

func (l *Learner) CompileDeferredFeedbackRecords(ctx context.Context, sess session.Session) error {
	if l == nil || l.repo == nil {
		return nil
	}
	if sess.Status != session.StatusClosed && sess.Status != session.StatusSessionKeep {
		return nil
	}
	records, err := l.repo.ListFeedbackRecords(ctx, feedback.Query{SessionID: sess.ID, Limit: 1000})
	if err != nil {
		return err
	}
	var pending []feedback.Record
	for _, record := range records {
		if !boolMetadata(record.Metadata, "learning_deferred") {
			continue
		}
		pending = append(pending, record)
	}
	if len(pending) == 0 {
		return nil
	}
	events, err := l.repo.ListEvents(ctx, sess.ID)
	if err != nil {
		return err
	}
	signals, err := l.repo.ListDerivedSignals(ctx, sess.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, record := range pending {
		outputs, err := l.CompileFeedback(ctx, record, sess, events, signals)
		if err != nil {
			return err
		}
		record.Outputs = outputs
		if record.Metadata == nil {
			record.Metadata = map[string]any{}
		}
		delete(record.Metadata, "learning_deferred")
		delete(record.Metadata, "learning_deferred_reason")
		record.Metadata["learning_compiled_after_status"] = string(sess.Status)
		record.Metadata["learning_compiled_at"] = now.Format(time.RFC3339Nano)
		record.UpdatedAt = now
		if err := l.repo.SaveFeedbackRecord(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

var (
	rePrefer         = regexp.MustCompile(`(?i)\bi prefer ([^.!\n]+)`)
	reCallMe         = regexp.MustCompile(`(?i)\bcall me ([^.!\n]+)`)
	reName           = regexp.MustCompile(`(?i)\bmy name is ([^.!\n]+)`)
	reContactChannel = regexp.MustCompile(`(?i)\b(?:(?:contact me|reach me|send(?: me)? updates?) (?:via|by|through)\s+(email|sms|phone|chat)|(?:(email|sms|phone|chat))\s+me\s+updates?)\b`)
	reLanguage       = regexp.MustCompile(`(?i)\b(?:please )?(?:reply|respond|speak|write) in\s+([a-zA-Z]+)\b`)
	reConcise        = regexp.MustCompile(`(?i)\b(?:be|keep it|please be)\s+(concise|brief|short)\b`)
	reFormality      = regexp.MustCompile(`(?i)\b(?:be|please be)\s+(formal|casual)\b`)
	reLocation       = regexp.MustCompile(`(?i)\b(?:i am in|i'm in|i live in|my location is)\s+([^.!\n]+)`)
	reTimezone       = regexp.MustCompile(`(?i)\b(?:my timezone is|my time zone is|i am in timezone|i'm in timezone)\s+([A-Za-z0-9_/\-+ ]+)`)
	reSensitiveFact  = regexp.MustCompile(`(?i)\b(?:my password is|my ssn is|my social security number is|my credit card is)\s+([^.!\n]+)`)
	reInferredPrefer = regexp.MustCompile(`(?i)\b(?:seems like|it looks like|maybe|probably)\s+(?:the customer\s+)?(?:prefers|likes|wants)\s+([^.!\n]+)`)
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
			for _, finding := range l.normalizeMemoryFindings(ctx, part.Text, event.CreatedAt) {
				item, memEvent, err := l.memoryRecord(ctx, sess, exec.ID, exec.TraceID, "conversation_explicit", finding)
				if err != nil {
					return err
				}
				if memEvent.Metadata == nil {
					memEvent.Metadata = map[string]any{}
				}
				memEvent.Metadata["event_id"] = event.ID
				if item.ID == "" {
					if memEvent.ID != "" {
						if err := l.repo.AppendCustomerMemoryEvent(ctx, memEvent); err != nil {
							return err
						}
					}
					continue
				}
				if err := l.repo.SaveCustomerMemoryItem(ctx, item, memEvent); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (l *Learner) memoryRecord(ctx context.Context, sess session.Session, executionID, traceID, source string, finding memoryFinding) (customer.MemoryItem, customer.MemoryEvent, error) {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return customer.MemoryItem{}, customer.MemoryEvent{}, nil
	}
	now := time.Now().UTC()
	evidence := []string{"session:" + sess.ID}
	if strings.TrimSpace(traceID) != "" {
		evidence = append(evidence, "trace:"+traceID)
	}
	if strings.TrimSpace(executionID) != "" {
		evidence = append(evidence, "execution:"+executionID)
	}
	memID := stableID("cmem", sess.AgentID, sess.CustomerID, finding.Category, finding.Key)
	status := customer.MemoryStatusActive
	action := "upsert"
	confidence := 1.0
	if finding.Inferred {
		status = customer.MemoryStatusPending
		action = "pending"
		confidence = 0.65
	}
	if finding.Sensitivity == customer.MemorySensitivitySensitive {
		status = customer.MemoryStatusBlocked
		action = "blocked_sensitive"
		confidence = 0
	}
	metadata := map[string]any{"compiler": "deterministic", "normalizer": "rules_v1"}
	if finding.ReviewReason != "" {
		metadata["review_reason"] = finding.ReviewReason
	}
	if finding.ConfirmationPrompt != "" {
		metadata["confirmation_prompt"] = finding.ConfirmationPrompt
	}
	if finding.RawKey != "" && finding.RawKey != finding.Key {
		metadata["raw_key"] = finding.RawKey
	}
	var confirmedAt *time.Time
	if status == customer.MemoryStatusActive {
		confirmedAt = &now
	}
	if existing, err := l.repo.GetCustomerMemoryItem(ctx, strings.TrimSpace(sess.AgentID), strings.TrimSpace(sess.CustomerID), finding.Category, finding.Key); err == nil {
		if !finding.Inferred && existing.Status == customer.MemoryStatusActive && existing.Value == finding.Value {
			existing.LastSeenAt = now
			existing.UpdatedAt = now
			return existing, customer.MemoryEvent{
				ID:           stableID("cmemt", memID, source, "confirmed", finding.Value, now.Format(time.RFC3339Nano)),
				MemoryID:     memID,
				AgentID:      strings.TrimSpace(sess.AgentID),
				CustomerID:   strings.TrimSpace(sess.CustomerID),
				Category:     finding.Category,
				Key:          finding.Key,
				Value:        finding.Value,
				Action:       "confirmed",
				Source:       source,
				Confidence:   1,
				EvidenceRefs: evidence,
				Metadata:     metadata,
				CreatedAt:    now,
			}, nil
		}
		if existing.Status == customer.MemoryStatusActive && existing.Value != finding.Value {
			if !finding.Inferred {
				return customer.MemoryItem{
						ID:              memID,
						AgentID:         strings.TrimSpace(sess.AgentID),
						CustomerID:      strings.TrimSpace(sess.CustomerID),
						Category:        finding.Category,
						Key:             finding.Key,
						Value:           finding.Value,
						Source:          source,
						Confidence:      1,
						Status:          customer.MemoryStatusActive,
						Sensitivity:     firstNonEmptyLearning(finding.Sensitivity, customer.MemorySensitivityLow),
						PromptSafe:      finding.PromptSafe,
						EvidenceRefs:    evidence,
						Metadata:        metadata,
						ValidUntil:      finding.ValidUntil,
						ObservedAt:      now,
						LastSeenAt:      now,
						LastConfirmedAt: &now,
						CreatedAt:       existing.CreatedAt,
						UpdatedAt:       now,
					}, customer.MemoryEvent{
						ID:           stableID("cmemt", memID, source, "supersede", finding.Value, now.Format(time.RFC3339Nano)),
						MemoryID:     memID,
						AgentID:      strings.TrimSpace(sess.AgentID),
						CustomerID:   strings.TrimSpace(sess.CustomerID),
						Category:     finding.Category,
						Key:          finding.Key,
						Value:        finding.Value,
						Action:       "supersede",
						Source:       source,
						Confidence:   1,
						EvidenceRefs: evidence,
						Metadata:     map[string]any{"compiler": "deterministic", "previous_value": existing.Value},
						CreatedAt:    now,
					}, nil
			}
			return customer.MemoryItem{}, customer.MemoryEvent{
				ID:           stableID("cmemt", memID, source, "conflict", finding.Value, now.Format(time.RFC3339Nano)),
				MemoryID:     memID,
				AgentID:      strings.TrimSpace(sess.AgentID),
				CustomerID:   strings.TrimSpace(sess.CustomerID),
				Category:     finding.Category,
				Key:          finding.Key,
				Value:        finding.Value,
				Action:       "conflict_pending",
				Source:       source,
				Confidence:   confidence,
				EvidenceRefs: evidence,
				Metadata:     mergeMaps(metadata, map[string]any{"current_value": existing.Value, "proposed_value": finding.Value}),
				CreatedAt:    now,
			}, nil
		}
	}
	if finding.Category == customer.MemoryCategoryPreference {
		if existing, err := l.repo.GetCustomerPreference(ctx, strings.TrimSpace(sess.AgentID), strings.TrimSpace(sess.CustomerID), finding.Key); err == nil {
			if !finding.Inferred && existing.Status == customer.PreferenceStatusActive && existing.Value == finding.Value {
				return customer.MemoryItem{}, customer.MemoryEvent{
					ID:           stableID("cmemt", memID, source, "confirmed", finding.Value, now.Format(time.RFC3339Nano)),
					MemoryID:     memID,
					AgentID:      strings.TrimSpace(sess.AgentID),
					CustomerID:   strings.TrimSpace(sess.CustomerID),
					Category:     finding.Category,
					Key:          finding.Key,
					Value:        finding.Value,
					Action:       "confirmed",
					Source:       source,
					Confidence:   1,
					EvidenceRefs: evidence,
					Metadata:     metadata,
					CreatedAt:    now,
				}, nil
			}
			if !finding.Inferred && existing.Status == customer.PreferenceStatusActive && existing.Value != finding.Value {
				return customer.MemoryItem{
						ID:              memID,
						AgentID:         strings.TrimSpace(sess.AgentID),
						CustomerID:      strings.TrimSpace(sess.CustomerID),
						Category:        finding.Category,
						Key:             finding.Key,
						Value:           finding.Value,
						Source:          source,
						Confidence:      1,
						Status:          customer.MemoryStatusActive,
						Sensitivity:     firstNonEmptyLearning(finding.Sensitivity, customer.MemorySensitivityLow),
						PromptSafe:      finding.PromptSafe,
						EvidenceRefs:    evidence,
						Metadata:        metadata,
						ValidUntil:      finding.ValidUntil,
						ObservedAt:      now,
						LastSeenAt:      now,
						LastConfirmedAt: &now,
						CreatedAt:       existing.CreatedAt,
						UpdatedAt:       now,
					}, customer.MemoryEvent{
						ID:           stableID("cmemt", memID, source, "supersede", finding.Value, now.Format(time.RFC3339Nano)),
						MemoryID:     memID,
						AgentID:      strings.TrimSpace(sess.AgentID),
						CustomerID:   strings.TrimSpace(sess.CustomerID),
						Category:     finding.Category,
						Key:          finding.Key,
						Value:        finding.Value,
						Action:       "supersede",
						Source:       source,
						Confidence:   1,
						EvidenceRefs: evidence,
						Metadata:     map[string]any{"compiler": "deterministic", "normalizer": "rules_v1", "previous_value": existing.Value},
						CreatedAt:    now,
					}, nil
			}
		}
	}
	return customer.MemoryItem{
			ID:              memID,
			AgentID:         strings.TrimSpace(sess.AgentID),
			CustomerID:      strings.TrimSpace(sess.CustomerID),
			Category:        finding.Category,
			Key:             finding.Key,
			Value:           finding.Value,
			Source:          source,
			Confidence:      confidence,
			Status:          status,
			Sensitivity:     firstNonEmptyLearning(finding.Sensitivity, customer.MemorySensitivityLow),
			PromptSafe:      finding.PromptSafe && status == customer.MemoryStatusActive,
			EvidenceRefs:    evidence,
			Metadata:        metadata,
			ValidUntil:      finding.ValidUntil,
			ObservedAt:      now,
			LastSeenAt:      now,
			LastConfirmedAt: confirmedAt,
			ExpiresAt:       finding.ValidUntil,
			CreatedAt:       now,
			UpdatedAt:       now,
		}, customer.MemoryEvent{
			ID:           stableID("cmemt", memID, source, action, finding.Value, now.Format(time.RFC3339Nano)),
			MemoryID:     memID,
			AgentID:      strings.TrimSpace(sess.AgentID),
			CustomerID:   strings.TrimSpace(sess.CustomerID),
			Category:     finding.Category,
			Key:          finding.Key,
			Value:        finding.Value,
			Action:       action,
			Source:       source,
			Confidence:   confidence,
			EvidenceRefs: evidence,
			Metadata:     metadata,
			CreatedAt:    now,
		}, nil
}

func (l *Learner) preferenceRecord(ctx context.Context, sess session.Session, executionID, traceID, source string, finding preferenceFinding) (customer.Preference, customer.PreferenceEvent, error) {
	item, event, err := l.memoryRecord(ctx, sess, executionID, traceID, source, memoryFinding{
		Category:           customer.MemoryCategoryPreference,
		Key:                finding.Key,
		Value:              finding.Value,
		PromptSafe:         true,
		Sensitivity:        customer.MemorySensitivityLow,
		Inferred:           finding.Inferred,
		ReviewReason:       finding.ReviewReason,
		ConfirmationPrompt: finding.ConfirmationPrompt,
	})
	if err != nil || item.ID == "" {
		return customer.Preference{}, customer.PreferenceEvent{}, err
	}
	return customer.Preference{
			ID:              "cpref_" + strings.TrimPrefix(item.ID, "cmem_"),
			AgentID:         item.AgentID,
			CustomerID:      item.CustomerID,
			Key:             item.Key,
			Value:           item.Value,
			Source:          item.Source,
			Confidence:      item.Confidence,
			Status:          item.Status,
			EvidenceRefs:    item.EvidenceRefs,
			Metadata:        item.Metadata,
			LastConfirmedAt: item.LastConfirmedAt,
			ExpiresAt:       item.ExpiresAt,
			CreatedAt:       item.CreatedAt,
			UpdatedAt:       item.UpdatedAt,
		}, customer.PreferenceEvent{
			ID:           "cpevt_" + strings.TrimPrefix(event.ID, "cmemt_"),
			PreferenceID: "cpref_" + strings.TrimPrefix(item.ID, "cmem_"),
			AgentID:      event.AgentID,
			CustomerID:   event.CustomerID,
			Key:          event.Key,
			Value:        event.Value,
			Action:       event.Action,
			Source:       event.Source,
			Confidence:   event.Confidence,
			EvidenceRefs: event.EvidenceRefs,
			Metadata:     event.Metadata,
			CreatedAt:    event.CreatedAt,
		}, nil
}

func boolMetadata(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		value = strings.TrimSpace(strings.ToLower(value))
		return value == "true" || value == "1" || value == "yes"
	default:
		return false
	}
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
	learningText := strings.TrimSpace(feedbackLearningFocus(record))
	id := stableID("kprop", scopeKind, scopeID, record.ID, learningText)
	pageTitle := feedbackKnowledgeTitle(learningText)
	pagePayload := map[string]any{
		"title":     pageTitle,
		"body":      learningText,
		"operation": "append",
		"citations": []map[string]any{{
			"uri":    "session:" + sess.ID,
			"anchor": record.TraceID,
			"title":  pageTitle,
		}},
	}
	item := knowledge.UpdateProposal{
		ID:        id,
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		Kind:      "operator_feedback",
		State:     "draft",
		Rationale: "Operator feedback suggested a shared knowledge update.",
		Evidence:  []knowledge.Citation{{URI: "session:" + sess.ID, Anchor: record.TraceID, Title: pageTitle}},
		Payload: map[string]any{
			"session_id":        sess.ID,
			"execution_id":      record.ExecutionID,
			"trace_id":          record.TraceID,
			"feedback_id":       record.ID,
			"operator_id":       record.OperatorID,
			"operator_feedback": learningText,
			"response_id":       record.ResponseID,
			"comment":           record.Comment,
			"correction":        record.Correction,
			"signals":           signalPayloads(signals),
			"title":             pagePayload["title"],
			"body":              pagePayload["body"],
			"operation":         pagePayload["operation"],
			"page":              pagePayload,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if strings.TrimSpace(record.ResponseID) != "" {
		item.Evidence = append(item.Evidence, knowledge.Citation{URI: "response:" + record.ResponseID, Anchor: record.TraceID, Title: pageTitle})
	}
	return id, l.repo.SaveKnowledgeUpdateProposal(ctx, item)
}

func feedbackKnowledgeTitle(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "Knowledge:")
	text = strings.TrimPrefix(text, "knowledge:")
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, ".!?\n"); idx >= 0 {
		text = text[:idx]
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "Operator feedback " + stableChecksum("empty")[:8]
	}
	if len(text) > 48 {
		text = strings.TrimSpace(text[:48])
	}
	return text + " [" + stableChecksum(text)[:8] + "]"
}

func feedbackLearningFocus(record feedback.Record) string {
	if text := strings.TrimSpace(record.Correction); text != "" {
		if comment := strings.TrimSpace(record.Comment); comment != "" {
			return text + "\n\nComment: " + comment
		}
		return text
	}
	if text := strings.TrimSpace(record.Comment); text != "" {
		if body := strings.TrimSpace(record.Text); body != "" {
			return body + "\n\nComment: " + text
		}
		return text
	}
	return strings.TrimSpace(record.LearningText())
}

func (l *Learner) proposePolicyChange(ctx context.Context, sess session.Session, record feedback.Record, soulOnly bool) (string, error) {
	base, ok := l.baseBundle(ctx, sess)
	if !ok {
		return "", nil
	}
	now := time.Now().UTC()
	learningText := strings.TrimSpace(feedbackLearningFocus(record))
	short := stableChecksum(record.ID + "\x00" + learningText)[:12]
	candidate := base
	candidate.ID = base.ID + "_feedback_" + short
	candidate.Version = strings.TrimSpace(base.Version + "+feedback." + short)
	candidate.ImportedAt = now
	candidate.SourceYAML = base.SourceYAML
	if soulOnly {
		applySoulFeedback(&candidate.Soul, learningText)
	} else {
		candidate.Guidelines = append(candidate.Guidelines, policy.Guideline{
			ID:          "feedback_" + short,
			When:        "feedback:" + record.ID,
			Then:        learningText,
			Scope:       "operator_feedback",
			Criticality: "high",
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
		Origin:                 "feedback",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if strings.TrimSpace(record.TraceID) != "" {
		proposal.EvidenceRefs = append(proposal.EvidenceRefs, "trace:"+record.TraceID)
	}
	if strings.TrimSpace(record.ResponseID) != "" {
		proposal.EvidenceRefs = append(proposal.EvidenceRefs, "response:"+record.ResponseID)
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
	Key                string
	Value              string
	Inferred           bool
	ReviewReason       string
	ConfirmationPrompt string
}

type memoryFinding struct {
	Category           string
	Key                string
	RawKey             string
	Value              string
	PromptSafe         bool
	Sensitivity        string
	ValidUntil         *time.Time
	Inferred           bool
	ReviewReason       string
	ConfirmationPrompt string
}

func preferenceFindings(text string) []preferenceFinding {
	mem := memoryFindings(text, time.Now().UTC())
	out := make([]preferenceFinding, 0, len(mem))
	for _, finding := range mem {
		if finding.Category != customer.MemoryCategoryPreference {
			continue
		}
		out = append(out, preferenceFinding{
			Key:                finding.Key,
			Value:              finding.Value,
			Inferred:           finding.Inferred,
			ReviewReason:       finding.ReviewReason,
			ConfirmationPrompt: finding.ConfirmationPrompt,
		})
	}
	return out
}

func memoryFindings(text string, observedAt time.Time) []memoryFinding {
	text = strings.TrimSpace(text)
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	var out []memoryFinding
	for _, item := range []struct {
		re       *regexp.Regexp
		key      func(string) string
		value    func(string) string
		category string
		inferred bool
		reason   string
		prompt   func(string) string
	}{
		{rePrefer, func(value string) string { return "preference." + stableChecksum(strings.ToLower(value))[:12] }, func(value string) string { return value }, customer.MemoryCategoryPreference, false, "", nil},
		{reCallMe, func(string) string { return "preferred_name" }, func(value string) string { return value }, customer.MemoryCategoryPreference, false, "", nil},
		{reName, func(string) string { return "preferred_name" }, func(value string) string { return value }, customer.MemoryCategoryPreference, false, "", nil},
		{reContactChannel, func(string) string { return "contact_channel" }, func(value string) string { return strings.ToLower(value) }, customer.MemoryCategoryPreference, false, "", nil},
		{reLanguage, func(string) string { return "preferred_language" }, func(value string) string { return strings.ToLower(value) }, customer.MemoryCategoryPreference, false, "", nil},
		{reConcise, func(string) string { return "response_style" }, func(value string) string { return strings.ToLower(value) }, customer.MemoryCategoryPreference, false, "", nil},
		{reFormality, func(string) string { return "formality" }, func(value string) string { return strings.ToLower(value) }, customer.MemoryCategoryPreference, false, "", nil},
		{reLocation, func(string) string { return "location" }, func(value string) string { return value }, customer.MemoryCategoryFact, false, "", nil},
		{reTimezone, func(string) string { return "time_zone" }, func(value string) string { return value }, customer.MemoryCategoryFact, false, "", nil},
		{reSensitiveFact, func(string) string { return "sensitive_personal_fact" }, func(value string) string { return value }, customer.MemoryCategoryFact, false, "", nil},
		{reInferredPrefer, func(string) string { return "inferred_preference" }, func(value string) string { return value }, customer.MemoryCategoryPreference, true, "inferred from ambiguous language", func(value string) string { return "Confirm whether the customer prefers " + value + "." }},
	} {
		match := item.re.FindStringSubmatch(text)
		if len(match) < 2 {
			continue
		}
		value := ""
		for _, candidate := range match[1:] {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				value = candidate
				break
			}
		}
		if value == "" {
			continue
		}
		if item.value != nil {
			value = item.value(value)
		}
		var prompt string
		if item.prompt != nil {
			prompt = item.prompt(value)
		}
		key := item.key(value)
		category := item.category
		validUntil := temporalValidUntil(text, observedAt)
		if validUntil != nil && category == customer.MemoryCategoryPreference {
			category = customer.MemoryCategoryTemporaryState
		}
		sensitivity := customer.MemorySensitivityLow
		statusPromptSafe := true
		if isSensitiveMemory(key, value) {
			sensitivity = customer.MemorySensitivitySensitive
			statusPromptSafe = false
		}
		out = append(out, memoryFinding{
			Category:           category,
			Key:                key,
			Value:              value,
			PromptSafe:         statusPromptSafe,
			Sensitivity:        sensitivity,
			ValidUntil:         validUntil,
			Inferred:           item.inferred,
			ReviewReason:       item.reason,
			ConfirmationPrompt: prompt,
		})
		if item.re == reName {
			out = append(out, memoryFinding{
				Category:    category,
				Key:         "name",
				RawKey:      key,
				Value:       value,
				PromptSafe:  statusPromptSafe,
				Sensitivity: sensitivity,
				ValidUntil:  validUntil,
			})
		}
	}
	return out
}

func (l *Learner) normalizeMemoryFindings(ctx context.Context, text string, observedAt time.Time) []memoryFinding {
	findings := memoryFindings(text, observedAt)
	if len(findings) > 0 {
		return findings
	}
	return l.llmMemoryFindings(ctx, text, observedAt)
}

func (l *Learner) llmMemoryFindings(ctx context.Context, text string, observedAt time.Time) []memoryFinding {
	if l == nil || l.router == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	prompt := "Extract durable customer memory from the text. Return JSON only with schema " +
		`{"items":[{"category":"preference|fact|temporary_state|summary","key":"canonical_snake_case","value":"string","confidence":0.0,"prompt_safe":true,"sensitivity":"low|sensitive","valid_days":0,"inferred":true,"review_reason":"string"}]}. ` +
		"Use pending/inferred for ambiguous memory. Mark passwords, payment data, medical, political, religious, government-id, and similar sensitive facts as sensitivity=sensitive and prompt_safe=false. Text: " + text
	resp, err := l.router.Generate(ctx, model.CapabilityStructured, model.Request{Prompt: prompt})
	if err != nil {
		return nil
	}
	raw := extractLearningJSONObject(strings.TrimSpace(resp.Text))
	if raw == "" {
		return nil
	}
	var parsed struct {
		Items []struct {
			Category     string  `json:"category"`
			Key          string  `json:"key"`
			Value        string  `json:"value"`
			Confidence   float64 `json:"confidence"`
			PromptSafe   bool    `json:"prompt_safe"`
			Sensitivity  string  `json:"sensitivity"`
			ValidDays    int     `json:"valid_days"`
			Inferred     bool    `json:"inferred"`
			ReviewReason string  `json:"review_reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	var out []memoryFinding
	for _, item := range parsed.Items {
		category := normalizeMemoryCategory(item.Category)
		key := strings.TrimSpace(item.Key)
		value := strings.TrimSpace(item.Value)
		if category == "" || key == "" || value == "" {
			continue
		}
		sensitivity := firstNonEmptyLearning(strings.TrimSpace(item.Sensitivity), customer.MemorySensitivityLow)
		promptSafe := item.PromptSafe && sensitivity != customer.MemorySensitivitySensitive
		var validUntil *time.Time
		if item.ValidDays > 0 {
			until := observedAt.Add(time.Duration(item.ValidDays) * 24 * time.Hour)
			validUntil = &until
		}
		out = append(out, memoryFinding{
			Category:     category,
			Key:          key,
			Value:        value,
			PromptSafe:   promptSafe,
			Sensitivity:  sensitivity,
			ValidUntil:   validUntil,
			Inferred:     item.Inferred || category != customer.MemoryCategoryPreference,
			ReviewReason: firstNonEmptyLearning(strings.TrimSpace(item.ReviewReason), "llm_normalized_memory"),
		})
	}
	return out
}

func normalizeMemoryCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case customer.MemoryCategoryPreference, customer.MemoryCategoryFact, customer.MemoryCategoryTemporaryState, customer.MemoryCategorySummary:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func extractLearningJSONObject(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "provider stub: "))
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return ""
	}
	return raw[start : end+1]
}

func temporalValidUntil(text string, observedAt time.Time) *time.Time {
	lower := strings.ToLower(text)
	var until time.Time
	switch {
	case strings.Contains(lower, "this week"):
		until = observedAt.Add(7 * 24 * time.Hour)
	case strings.Contains(lower, "today"):
		until = observedAt.Add(24 * time.Hour)
	case strings.Contains(lower, "tomorrow"):
		until = observedAt.Add(48 * time.Hour)
	case strings.Contains(lower, "for this order"), strings.Contains(lower, "for this ticket"):
		until = observedAt.Add(30 * 24 * time.Hour)
	}
	if until.IsZero() {
		return nil
	}
	return &until
}

func isSensitiveMemory(key string, value string) bool {
	lower := strings.ToLower(key + " " + value)
	return containsAny(lower, "sensitive_personal_fact", "password", "ssn", "social security", "credit card", "card number", "medical", "diagnosis", "medication", "religion", "political", "passport")
}

func isKnowledgeFeedback(text string) bool {
	return containsAny(text, "knowledge", "fact", "docs", "documentation", "product", "faq", "manual", "article", "unsupported_claim", "retrieval_miss")
}

func isPolicyFeedback(text string) bool {
	return containsAny(text, "policy", "guideline", "journey", "scenario", "guardrail", "rule", "behavior", "approval", "escalate", "answered_out_of_scope", "out_of_scope", "hallucinated_policy", "premature_commitment")
}

func isSoulFeedback(text string) bool {
	return containsAny(text, "soul", "persona", "tone", "voice", "brand", "style", "language", "verbosity", "formal", "handoff", "tone_mismatch", "bad_language")
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

func applySoulFeedback(soul *policy.Soul, text string) {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "concise") || strings.Contains(lower, "brief") || strings.Contains(lower, "short") {
		soul.Verbosity = "concise"
	}
	if strings.Contains(lower, "formal") {
		soul.Formality = "formal"
	}
	if strings.Contains(lower, "casual") {
		soul.Formality = "casual"
	}
	if strings.Contains(lower, "warm") {
		soul.Tone = "warm"
	}
	if strings.Contains(lower, "friendly") {
		soul.Tone = "friendly"
	}
	if strings.Contains(lower, "indonesia") || strings.Contains(lower, "bahasa") {
		soul.DefaultLanguage = "id"
	}
	if strings.Contains(lower, "english") {
		soul.DefaultLanguage = "en"
	}
	rule := "Operator feedback: " + text
	if !containsString(soul.StyleRules, rule) {
		soul.StyleRules = append(soul.StyleRules, rule)
	}
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

func mergeMaps(base map[string]any, extra map[string]any) map[string]any {
	if base == nil && extra == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func firstNonEmptyLearning(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
