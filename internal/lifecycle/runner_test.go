package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestLifecycleRunnerAsksFollowupThenCloses(t *testing.T) {
	t.Setenv("SESSION_IDLE_CANDIDATE_AFTER", "1s")
	t.Setenv("SESSION_AWAITING_CLOSE_AFTER", "1s")
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC().Add(-2 * time.Second)
	if err := repo.CreateSession(ctx, session.Session{
		ID:             "sess_lifecycle",
		Channel:        "web",
		Status:         session.StatusActive,
		CreatedAt:      now,
		LastActivityAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := sessionsvc.New(repo, writes)
	if _, err := svc.CreateMessageEvent(ctx, "sess_lifecycle", "customer", "where is my order", "", "trace_1", nil, false); err != nil {
		t.Fatal(err)
	}
	sess, _ := repo.GetSession(ctx, "sess_lifecycle")
	sess.LastActivityAt = now
	if err := repo.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	r := New(repo, writes, nil)
	r.processIdleSessions(ctx, time.Now().UTC())

	events, err := repo.ListEvents(ctx, "sess_lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	foundFollowup := false
	for _, event := range events {
		if event.Source == "ai_agent" && strings.Contains(strings.ToLower(sessionEventText(event)), "need any more help") {
			foundFollowup = true
		}
	}
	if !foundFollowup {
		t.Fatalf("events = %#v, want lifecycle follow-up message", events)
	}
	sess, _ = repo.GetSession(ctx, "sess_lifecycle")
	if sess.Status != session.StatusAwaitingCustomer || sess.FollowupCount != 1 {
		t.Fatalf("session = %#v, want awaiting_customer with followup_count=1", sess)
	}

	sess.LastActivityAt = time.Now().UTC().Add(-2 * time.Second)
	if err := repo.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	r.processIdleSessions(ctx, time.Now().UTC())
	sess, _ = repo.GetSession(ctx, "sess_lifecycle")
	if sess.Status != session.StatusClosed {
		t.Fatalf("session status = %s, want closed", sess.Status)
	}
}

func TestMarkKeepResetsCooldownAndCompilesDeferredFeedback(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 8)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	if err := repo.CreateSession(ctx, session.Session{
		ID:             "sess_keep",
		Channel:        "web",
		Status:         session.StatusActive,
		CreatedAt:      now,
		LastActivityAt: now,
		AgentID:        "agent_1",
		CustomerID:     "cust_1",
	}); err != nil {
		t.Fatal(err)
	}
	record := feedback.Record{
		ID:        "fb_keep",
		SessionID: "sess_keep",
		Text:      "Call me Rina.",
		Metadata: map[string]any{
			"learning_deferred": true,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.SaveFeedbackRecord(ctx, record); err != nil {
		t.Fatal(err)
	}

	r := New(repo, writes, nil)
	sess, err := repo.GetSession(ctx, "sess_keep")
	if err != nil {
		t.Fatal(err)
	}
	markAt := time.Now().UTC()
	if err := r.markKeep(ctx, &sess, "watch_pending", markAt); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetSession(ctx, "sess_keep")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != session.StatusSessionKeep {
		t.Fatalf("status = %s, want session_keep", updated.Status)
	}
	if updated.LastActivityAt.Before(markAt.Add(-time.Second)) {
		t.Fatalf("last_activity_at = %v, want reset near %v", updated.LastActivityAt, markAt)
	}
	if isLifecycleEligible(updated, markAt.Add(time.Minute)) {
		t.Fatalf("session %#v should not be immediately eligible after keep", updated)
	}
	compiled, err := repo.GetFeedbackRecord(ctx, "fb_keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Outputs.PreferenceIDs) == 0 {
		t.Fatalf("feedback outputs = %#v, want deferred feedback compiled on keep", compiled.Outputs)
	}
}

func TestLifecycleRunnerUsesBundleLifecyclePolicy(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC().Add(-2 * time.Second)
	if err := repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_support",
		Version: "v1",
		LifecyclePolicy: policy.LifecyclePolicy{
			ID:                   "custom_lifecycle",
			IdleCandidateAfterMS: 1,
			FollowupMessage:      "Is there anything else you still need from support?",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgentProfile(ctx, agent.Profile{
		ID:                    "agent_support",
		Name:                  "Support Agent",
		Status:                "active",
		DefaultPolicyBundleID: "bundle_support",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session.Session{
		ID:             "sess_lifecycle_bundle",
		Channel:        "web",
		AgentID:        "agent_support",
		Status:         session.StatusActive,
		CreatedAt:      now,
		LastActivityAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := sessionsvc.New(repo, writes)
	if _, err := svc.CreateMessageEvent(ctx, "sess_lifecycle_bundle", "customer", "where is my order", "", "trace_bundle", nil, false); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(ctx, "sess_lifecycle_bundle")
	if err != nil {
		t.Fatal(err)
	}
	sess.LastActivityAt = now
	if err := repo.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	r := New(repo, writes, nil)
	r.processIdleSessions(ctx, time.Now().UTC())

	events, err := repo.ListEvents(ctx, "sess_lifecycle_bundle")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Source == "ai_agent" && strings.Contains(strings.ToLower(sessionEventText(event)), "anything else you still need from support") {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %#v, want lifecycle follow-up from bundle policy", events)
	}
}
