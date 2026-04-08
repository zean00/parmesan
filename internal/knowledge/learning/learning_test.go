package learning

import (
	"context"
	"testing"

	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestCompileFeedbackCreatesDistinctKnowledgePageTitles(t *testing.T) {
	repo := memory.New()
	learner := New(repo)
	sess := session.Session{
		ID:       "sess_1",
		AgentID:  "agent_1",
		Channel:  "acp",
		Metadata: map[string]any{},
	}

	first, err := learner.CompileFeedback(context.Background(), feedback.Record{
		ID:       "fb_1",
		Category: "knowledge",
		Text:     "Knowledge: damaged electronics purchased within 30 days qualify for an instant replacement before refund review.",
	}, sess, nil, nil)
	if err != nil {
		t.Fatalf("CompileFeedback(first) error = %v", err)
	}
	second, err := learner.CompileFeedback(context.Background(), feedback.Record{
		ID:       "fb_2",
		Category: "knowledge",
		Text:     "Knowledge: shipping delays above five business days should include a proactive apology and updated ETA.",
	}, sess, nil, nil)
	if err != nil {
		t.Fatalf("CompileFeedback(second) error = %v", err)
	}
	if len(first.KnowledgeProposalIDs) != 1 || len(second.KnowledgeProposalIDs) != 1 {
		t.Fatalf("knowledge proposal outputs = %#v %#v, want one each", first, second)
	}

	item1, err := repo.GetKnowledgeUpdateProposal(context.Background(), first.KnowledgeProposalIDs[0])
	if err != nil {
		t.Fatalf("GetKnowledgeUpdateProposal(first) error = %v", err)
	}
	item2, err := repo.GetKnowledgeUpdateProposal(context.Background(), second.KnowledgeProposalIDs[0])
	if err != nil {
		t.Fatalf("GetKnowledgeUpdateProposal(second) error = %v", err)
	}

	title1 := proposalPageTitle(item1.Payload)
	title2 := proposalPageTitle(item2.Payload)
	if title1 == "" || title2 == "" {
		t.Fatalf("proposal titles = %q, %q, want non-empty", title1, title2)
	}
	if title1 == "Operator feedback" || title2 == "Operator feedback" {
		t.Fatalf("proposal titles = %q, %q, want topic-specific titles", title1, title2)
	}
	if title1 == title2 {
		t.Fatalf("proposal titles = %q, %q, want distinct titles for unrelated feedback", title1, title2)
	}
}

func proposalPageTitle(payload map[string]any) string {
	page, _ := payload["page"].(map[string]any)
	title, _ := page["title"].(string)
	return title
}
