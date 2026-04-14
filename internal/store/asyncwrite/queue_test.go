package asyncwrite

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/store"
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

type failingBundleRepo struct {
	store.Repository
}

func (r failingBundleRepo) SaveBundle(_ context.Context, _ policy.Bundle) error {
	return context.DeadlineExceeded
}

type flakeyBundleRepo struct {
	store.Repository
	failures atomic.Int32
}

func (r *flakeyBundleRepo) SaveBundle(ctx context.Context, bundle policy.Bundle) error {
	if r.failures.CompareAndSwap(0, 1) {
		return context.DeadlineExceeded
	}
	return r.Repository.SaveBundle(ctx, bundle)
}

func TestQueueStatsCaptureBackgroundFailures(t *testing.T) {
	repo := failingBundleRepo{Repository: memory.New()}
	q := New(repo, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx, 1)
	if err := q.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_1", Version: "v1", ImportedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveBundle() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	stats := q.Stats()
	q.Stop()
	if failed, _ := stats["failed"].(int64); failed != 1 {
		t.Fatalf("failed = %#v, want 1 stats=%#v", stats["failed"], stats)
	}
	if healthy, _ := stats["healthy"].(bool); healthy {
		t.Fatalf("healthy = %#v, want false", stats["healthy"])
	}
	if lastJob, _ := stats["last_failed_job"].(string); !strings.Contains(lastJob, "save_bundle") {
		t.Fatalf("last_failed_job = %#v, want save_bundle stats=%#v", stats["last_failed_job"], stats)
	}
}

func TestQueueHealthRecoversAfterLaterSuccess(t *testing.T) {
	repo := &flakeyBundleRepo{Repository: memory.New()}
	q := New(repo, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx, 1)
	if err := q.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_fail", Version: "v1", ImportedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveBundle() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := q.SaveBundle(context.Background(), policy.Bundle{ID: "bundle_ok", Version: "v1", ImportedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveBundle() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	stats := q.Stats()
	q.Stop()
	if healthy, _ := stats["healthy"].(bool); !healthy {
		t.Fatalf("healthy = %#v, want true after later success stats=%#v", stats["healthy"], stats)
	}
}
