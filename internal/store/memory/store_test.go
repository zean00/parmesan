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
