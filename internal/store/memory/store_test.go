package memory

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestListBundlesReturnsNewestFirst(t *testing.T) {
	store := New()
	_ = store.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_old", Version: "v1", ImportedAt: time.Now().UTC().Add(-time.Minute)})
	_ = store.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_new", Version: "v2", ImportedAt: time.Now().UTC()})

	bundles, err := store.ListBundles(context.Background())
	if err != nil {
		t.Fatalf("ListBundles() error = %v", err)
	}
	if len(bundles) != 2 || bundles[0].ID != "bundle_new" || bundles[1].ID != "bundle_old" {
		t.Fatalf("bundles = %#v, want newest first", bundles)
	}
}

func TestListRunnableExecutionsHonorsWaitingRetryCursor(t *testing.T) {
	store := New()
	now := time.Now().UTC()
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	for _, exec := range []execution.TurnExecution{
		{ID: "pending", Status: execution.StatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "waiting_future", Status: execution.StatusWaiting, LeaseExpiresAt: future, CreatedAt: now, UpdatedAt: now},
		{ID: "waiting_due", Status: execution.StatusWaiting, LeaseExpiresAt: past, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.CreateExecution(context.Background(), exec, nil); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListRunnableExecutions(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.ID] = true
	}
	if !seen["pending"] || !seen["waiting_due"] || seen["waiting_future"] {
		t.Fatalf("runnable ids = %#v, want pending and due waiting only", seen)
	}
}

func TestSaveBundleMaterializesGraphSnapshot(t *testing.T) {
	store := New()
	now := time.Now().UTC()
	bundle := policy.Bundle{
		ID:         "bundle_graph",
		Version:    "v1",
		ImportedAt: now,
		Soul:       policy.Soul{Identity: "Graph Agent"},
		Guidelines: []policy.Guideline{{ID: "greet", When: "customer says hi", Then: "say hello"}},
		WatchCapabilities: []policy.WatchCapability{{
			ID:               "delivery_watch",
			Kind:             "delivery_status",
			ScheduleStrategy: "poll",
		}},
	}
	if err := store.SaveBundle(context.Background(), bundle); err != nil {
		t.Fatalf("SaveBundle() error = %v", err)
	}
	snapshots, err := store.ListPolicySnapshots(context.Background(), policy.SnapshotQuery{BundleID: bundle.ID})
	if err != nil {
		t.Fatalf("ListPolicySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	if snapshots[0].BundleID != bundle.ID || snapshots[0].Bundle.ID != bundle.ID {
		t.Fatalf("snapshot = %#v, want materialized bundle %q", snapshots[0], bundle.ID)
	}
	artifacts, err := store.ListPolicyArtifacts(context.Background(), policy.ArtifactQuery{BundleID: bundle.ID})
	if err != nil {
		t.Fatalf("ListPolicyArtifacts() error = %v", err)
	}
	if len(artifacts) == 0 {
		t.Fatal("expected policy artifacts to be materialized")
	}
	var foundWatch bool
	for _, item := range artifacts {
		if item.Kind == "watch_capability" {
			foundWatch = true
			break
		}
	}
	if !foundWatch {
		t.Fatalf("artifacts = %#v, want watch_capability", artifacts)
	}
}
