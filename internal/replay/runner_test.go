package replay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/policy"
	replaydomain "github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestReplayRunnerCompletesReplayRun(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "I want to return this order"}},
	})
	_ = repo.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		PolicyBundleID: "bundle_1",
		Status:         execution.StatusSucceeded,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil)
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_1",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "returns",
			When: "return order",
			Then: "Help with returns",
		}},
	})

	run := replaydomain.Run{
		ID:                "eval_1",
		Type:              replaydomain.TypeReplay,
		SourceExecutionID: "exec_1",
		ActiveBundleID:    "bundle_1",
		Status:            replaydomain.StatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.CreateEvalRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	r := New(repo, writes)
	if err := r.process(ctx, run); err != nil {
		t.Fatalf("process() error = %v", err)
	}

	got, err := repo.GetEvalRun(ctx, "eval_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != replaydomain.StatusSucceeded {
		t.Fatalf("status = %s, want %s", got.Status, replaydomain.StatusSucceeded)
	}
	if got.ResultJSON == "" || got.ResultJSON == "{}" {
		t.Fatalf("result_json = %q, want populated replay output", got.ResultJSON)
	}
}

func TestReplayRunnerUpdatesProposalSummary(t *testing.T) {
	repo := memory.New()
	writes := asyncwrite.New(repo, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes.Start(ctx, 1)
	defer writes.Stop()

	now := time.Now().UTC()
	_ = repo.CreateSession(ctx, session.Session{ID: "sess", Channel: "web", CreatedAt: now})
	_ = repo.AppendEvent(ctx, session.Event{
		ID:        "evt_1",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		CreatedAt: now,
		Content:   []session.ContentPart{{Type: "text", Text: "return order"}},
	})
	_ = repo.CreateExecution(ctx, execution.TurnExecution{
		ID:             "exec_1",
		SessionID:      "sess",
		TriggerEventID: "evt_1",
		PolicyBundleID: "bundle_active",
		Status:         execution.StatusSucceeded,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil)
	_ = repo.SaveBundle(ctx, policy.Bundle{ID: "bundle_active", Version: "v1"})
	_ = repo.SaveBundle(ctx, policy.Bundle{
		ID:      "bundle_candidate",
		Version: "v2",
		Guidelines: []policy.Guideline{{
			ID:   "returns",
			When: "return order",
			Then: "Help the customer start a return",
		}},
	})
	_ = repo.SaveProposal(ctx, rollout.Proposal{
		ID:                "proposal_1",
		SourceBundleID:    "bundle_active",
		CandidateBundleID: "bundle_candidate",
		State:             rollout.StateShadow,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	run := replaydomain.Run{
		ID:                "eval_shadow",
		Type:              replaydomain.TypeShadow,
		ProposalID:        "proposal_1",
		SourceExecutionID: "exec_1",
		ActiveBundleID:    "bundle_active",
		ShadowBundleID:    "bundle_candidate",
		Status:            replaydomain.StatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	_ = repo.CreateEvalRun(ctx, run)

	r := New(repo, writes)
	if err := r.process(ctx, run); err != nil {
		t.Fatalf("process() error = %v", err)
	}

	proposal, err := repo.GetProposal(ctx, "proposal_1")
	if err != nil {
		t.Fatal(err)
	}
	if proposal.EvalSummaryJSON == "" || proposal.ReplayScore == 0 {
		t.Fatalf("proposal = %#v, want populated eval summary and replay score", proposal)
	}
	var summary struct {
		Quality map[string]any `json:"quality"`
	}
	if err := json.Unmarshal([]byte(proposal.EvalSummaryJSON), &summary); err != nil {
		t.Fatalf("decode eval summary: %v", err)
	}
	if len(summary.Quality) == 0 {
		t.Fatalf("eval summary = %s, want quality scorecards", proposal.EvalSummaryJSON)
	}
	if proposal.SafetyScore == 0 {
		t.Fatalf("proposal safety score = %v, want quality-derived score", proposal.SafetyScore)
	}
}

func TestSelectSnapshotBundlesUsesSnapshotOnly(t *testing.T) {
	now := time.Now().UTC()
	snapshots := []policy.Snapshot{{
		ID:        "snap_1",
		BundleID:  "bundle_1",
		Version:   "v1",
		Bundle:    policy.Bundle{ID: "bundle_1", Version: "v1", Soul: policy.Soul{Identity: "Snapshot Only"}},
		CreatedAt: now,
	}}
	bundles := selectSnapshotBundles(snapshots, "bundle_1", "")
	if len(bundles) != 1 || bundles[0].Soul.Identity != "Snapshot Only" {
		t.Fatalf("bundles = %#v, want snapshot-backed bundle", bundles)
	}
}
