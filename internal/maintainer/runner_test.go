package maintainer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/maintainer"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestBootstrapJobCreatesWorkspaceAndSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	router := model.NewRouter(config.ProviderConfig{})
	svc := NewService(repo)
	runner := New(repo, router)
	now := time.Now().UTC()
	profile := agent.Profile{
		ID:                        "agent_bootstrap",
		Name:                      "Support",
		Description:               "Handles support requests.",
		Status:                    "active",
		DefaultKnowledgeScopeKind: "agent",
		DefaultKnowledgeScopeID:   "agent_bootstrap",
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := repo.SaveAgentProfile(ctx, profile); err != nil {
		t.Fatalf("SaveAgentProfile() error = %v", err)
	}
	job, err := svc.QueueBootstrap(ctx, profile, "op_1")
	if err != nil {
		t.Fatalf("QueueBootstrap() error = %v", err)
	}
	if err := runner.processMaintainerJob(ctx, job.ID); err != nil {
		t.Fatalf("processMaintainerJob() error = %v", err)
	}
	workspaces, err := repo.ListMaintainerWorkspaces(ctx, maintainer.WorkspaceQuery{ScopeKind: "agent", ScopeID: "agent_bootstrap", Mode: maintainer.ModeSharedWiki, Limit: 10})
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("ListMaintainerWorkspaces() = %v, %v; want one workspace", workspaces, err)
	}
	snapshots, err := repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: "agent", ScopeID: "agent_bootstrap", Limit: 10})
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("ListKnowledgeSnapshots() = %v, %v; want bootstrap snapshot", snapshots, err)
	}
}

func TestKnowledgeSyncJobRunsThroughMaintainer(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	router := model.NewRouter(config.ProviderConfig{})
	runner := New(repo, router)
	dir := t.TempDir()
	path := filepath.Join(dir, "faq.md")
	if err := os.WriteFile(path, []byte("# Refunds\n\nWe accept returns within 30 days."), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	now := time.Now().UTC()
	source := knowledge.Source{
		ID:        "source_1",
		ScopeKind: "agent",
		ScopeID:   "agent_sync",
		Kind:      "folder",
		URI:       dir,
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.SaveKnowledgeSource(ctx, source); err != nil {
		t.Fatalf("SaveKnowledgeSource() error = %v", err)
	}
	job := knowledge.SyncJob{
		ID:        "ksync_1",
		SourceID:  source.ID,
		Status:    "queued",
		CreatedAt: now,
	}
	if err := repo.SaveKnowledgeSyncJob(ctx, job); err != nil {
		t.Fatalf("SaveKnowledgeSyncJob() error = %v", err)
	}
	if err := runner.processKnowledgeSyncJob(ctx, job.ID); err != nil {
		t.Fatalf("processKnowledgeSyncJob() error = %v", err)
	}
	updated, err := repo.GetKnowledgeSyncJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetKnowledgeSyncJob() error = %v", err)
	}
	if updated.Status != "succeeded" || updated.SnapshotID == "" {
		t.Fatalf("sync job = %#v, want succeeded snapshot", updated)
	}
	workspaces, err := repo.ListMaintainerWorkspaces(ctx, maintainer.WorkspaceQuery{ScopeKind: "agent", ScopeID: "agent_sync", Mode: maintainer.ModeSharedWiki, Limit: 10})
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("ListMaintainerWorkspaces() = %v, %v; want workspace", workspaces, err)
	}
	pages, err := repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: "agent", ScopeID: "agent_sync", Limit: 100})
	if err != nil || len(pages) == 0 {
		t.Fatalf("ListKnowledgePages() = %v, %v; want pages", pages, err)
	}
}

func TestSessionLearningCreatesCustomerMemoryAndPreferences(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	router := model.NewRouter(config.ProviderConfig{})
	svc := NewService(repo)
	runner := New(repo, router)
	now := time.Now().UTC()
	sess := session.Session{
		ID:         "sess_1",
		AgentID:    "agent_1",
		CustomerID: "cust_1",
		Channel:    "web",
		Status:     session.StatusClosed,
		CreatedAt:  now,
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := repo.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: sess.ID,
		Source:    "customer",
		Kind:      "message",
		Offset:    1,
		CreatedAt: now,
		Content: []session.ContentPart{{
			Type: "text",
			Text: "I prefer email updates and please reply in English.",
		}},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	job, err := svc.QueueSessionLearning(ctx, sess, "system")
	if err != nil {
		t.Fatalf("QueueSessionLearning() error = %v", err)
	}
	if err := runner.processMaintainerJob(ctx, job.ID); err != nil {
		t.Fatalf("processMaintainerJob() error = %v", err)
	}
	pages, err := repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: "customer_agent", ScopeID: "agent_1:cust_1", Limit: 100})
	if err != nil || len(pages) == 0 {
		t.Fatalf("ListKnowledgePages() = %v, %v; want customer memory pages", pages, err)
	}
	prefs, err := repo.ListCustomerPreferences(ctx, customer.PreferenceQuery{AgentID: "agent_1", CustomerID: "cust_1", Limit: 100})
	if err != nil || len(prefs) == 0 {
		t.Fatalf("ListCustomerPreferences() = %v, %v; want learned preferences", prefs, err)
	}
}

func TestQueueFeedbackRoutesResponseCorrectionToSharedWiki(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	svc := NewService(repo)
	now := time.Now().UTC()
	sess := session.Session{
		ID:         "sess_feedback_scope",
		AgentID:    "agent_1",
		CustomerID: "cust_1",
		Channel:    "web",
		Status:     session.StatusClosed,
		CreatedAt:  now,
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	record := feedback.Record{
		ID:         "fb_response_scope",
		SessionID:  sess.ID,
		ResponseID: "resp_1",
		Correction: "The return window is 30 days.",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	job, err := svc.QueueFeedback(ctx, sess, record)
	if err != nil {
		t.Fatalf("QueueFeedback() error = %v", err)
	}
	if job.Mode != maintainer.ModeSharedWiki || job.ScopeKind != "agent" || job.ScopeID != "agent_1" {
		t.Fatalf("job = %#v, want shared wiki scope for response correction", job)
	}
}

func TestQueueFeedbackSkipsScoreOnlyResponseFeedback(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	svc := NewService(repo)
	now := time.Now().UTC()
	sess := session.Session{
		ID:         "sess_feedback_score_only",
		AgentID:    "agent_1",
		CustomerID: "cust_1",
		Channel:    "web",
		Status:     session.StatusClosed,
		CreatedAt:  now,
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	score := 1
	job, err := svc.QueueFeedback(ctx, sess, feedback.Record{
		ID:         "fb_score_only",
		SessionID:  sess.ID,
		ResponseID: "resp_1",
		Score:      &score,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("QueueFeedback() error = %v", err)
	}
	if job.ID != "" {
		t.Fatalf("job = %#v, want score-only feedback to skip maintainer queue", job)
	}
	jobs, err := repo.ListMaintainerJobs(ctx, maintainer.JobQuery{SessionID: sess.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMaintainerJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want no maintainer jobs for score-only feedback", jobs)
	}
}
