package asyncwrite

import (
	"context"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestQueueDrainsBufferedWritesOnShutdown(t *testing.T) {
	repo := memory.New()
	q := New(repo, 8)
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx, 1)

	if err := q.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_1", Version: "v1", ImportedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveBundle() error = %v", err)
	}

	cancel()
	q.Stop()

	bundles, err := repo.ListBundles(context.Background())
	if err != nil {
		t.Fatalf("ListBundles() error = %v", err)
	}
	if len(bundles) != 1 || bundles[0].ID != "bundle_1" {
		t.Fatalf("bundles = %#v, want drained queued bundle", bundles)
	}
}
