package memory

import (
	"context"
	"testing"
	"time"

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
