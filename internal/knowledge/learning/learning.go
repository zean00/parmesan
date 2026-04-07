package learning

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
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

var (
	rePrefer = regexp.MustCompile(`(?i)\bi prefer ([^.!\n]+)`)
	reCallMe = regexp.MustCompile(`(?i)\bcall me ([^.!\n]+)`)
	reName   = regexp.MustCompile(`(?i)\bmy name is ([^.!\n]+)`)
)

func (l *Learner) learnCustomerFacts(ctx context.Context, sess session.Session, exec execution.TurnExecution, events []session.Event) error {
	scopeID := customerScopeID(sess)
	if scopeID == "" {
		return nil
	}
	var facts []string
	for _, event := range events {
		if event.Source != "customer" {
			continue
		}
		for _, part := range event.Content {
			if part.Type != "text" || strings.TrimSpace(part.Text) == "" {
				continue
			}
			text := strings.TrimSpace(part.Text)
			for _, match := range [][]string{rePrefer.FindStringSubmatch(text), reCallMe.FindStringSubmatch(text), reName.FindStringSubmatch(text)} {
				if len(match) != 2 {
					continue
				}
				fact := strings.TrimSpace(match[1])
				if fact != "" {
					facts = append(facts, factSentence(match[0], fact))
				}
			}
		}
	}
	facts = dedupeStrings(facts)
	if len(facts) == 0 {
		return nil
	}
	now := time.Now().UTC()
	body := strings.Join(facts, "\n")
	pageID := stableID("kpage", "customer_agent", scopeID, exec.ID)
	page := knowledge.Page{
		ID:        pageID,
		ScopeKind: "customer_agent",
		ScopeID:   scopeID,
		Title:     "Learned customer facts",
		Body:      body,
		PageType:  "customer_facts",
		Citations: []knowledge.Citation{{URI: "session:" + sess.ID, Anchor: exec.TraceID, Title: "Session evidence"}},
		Metadata:  map[string]any{"execution_id": exec.ID, "trace_id": exec.TraceID, "learning_mode": "direct"},
		Checksum:  stableChecksum(body),
		CreatedAt: now,
		UpdatedAt: now,
	}
	chunks := []knowledge.Chunk{{
		ID:        stableID("kchunk", pageID, "0"),
		PageID:    pageID,
		ScopeKind: page.ScopeKind,
		ScopeID:   page.ScopeID,
		Text:      page.Body,
		Citations: append([]knowledge.Citation(nil), page.Citations...),
		Metadata:  map[string]any{"page_title": page.Title},
		CreatedAt: now,
	}}
	if err := l.repo.SaveKnowledgePage(ctx, page, chunks); err != nil {
		return err
	}
	pages, err := l.repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: "customer_agent", ScopeID: scopeID, Limit: 1000})
	if err != nil {
		return err
	}
	chunksForScope, err := l.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: "customer_agent", ScopeID: scopeID, Limit: 1000})
	if err != nil {
		return err
	}
	pageIDs := make([]string, 0, len(pages))
	chunkIDs := make([]string, 0, len(chunksForScope))
	for _, item := range pages {
		pageIDs = append(pageIDs, item.ID)
	}
	for _, item := range chunksForScope {
		chunkIDs = append(chunkIDs, item.ID)
	}
	return l.repo.SaveKnowledgeSnapshot(ctx, knowledge.Snapshot{
		ID:        stableID("ksnap", "customer_agent", scopeID, strings.Join(pageIDs, ","), strings.Join(chunkIDs, ",")),
		ScopeKind: "customer_agent",
		ScopeID:   scopeID,
		PageIDs:   pageIDs,
		ChunkIDs:  chunkIDs,
		Metadata:  map[string]any{"source": "conversation_learning", "last_execution_id": exec.ID},
		CreatedAt: now,
	})
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

func customerScopeID(sess session.Session) string {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return ""
	}
	return strings.TrimSpace(sess.AgentID) + ":" + strings.TrimSpace(sess.CustomerID)
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

func factSentence(match string, fact string) string {
	match = strings.ToLower(strings.TrimSpace(match))
	switch {
	case strings.HasPrefix(match, "i prefer"):
		return "Customer preference: " + fact
	case strings.HasPrefix(match, "call me"):
		return "Preferred form of address: " + fact
	default:
		return "Customer name: " + fact
	}
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
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
