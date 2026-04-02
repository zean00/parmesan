package toolsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func TestSyncOpenAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
openapi: 3.0.3
info:
  title: Demo
  version: "1.0"
paths:
  /refunds:
    get:
      operationId: listRefunds
      description: List refunds
      responses:
        "200":
          description: ok
`))
	}))
	defer server.Close()

	syncer := New()
	entries, err := syncer.SyncProvider(context.Background(), tool.ProviderBinding{
		ID:   "provider_openapi",
		Kind: tool.ProviderOpenAPI,
		Name: "demo",
		URI:  server.URL,
	})
	if err != nil {
		t.Fatalf("SyncProvider() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	if entries[0].Name != "listRefunds" {
		t.Fatalf("entry name = %q, want listRefunds", entries[0].Name)
	}
	if entries[0].RuntimeProtocol != "mcp" {
		t.Fatalf("runtime protocol = %q, want mcp", entries[0].RuntimeProtocol)
	}
}
