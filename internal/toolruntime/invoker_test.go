package toolruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/toolsecurity"
)

func TestInvokeAppliesBearerAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer secret-token", got)
		}
		_, _ = w.Write([]byte(`{"result":{"ok":true}}`))
	}))
	defer server.Close()

	invoker := New()
	out, err := invoker.Invoke(context.Background(),
		tool.ProviderBinding{ID: "provider_1", URI: server.URL},
		tool.AuthBinding{ProviderID: "provider_1", Type: tool.AuthBearer, Secret: "secret-token"},
		tool.CatalogEntry{ID: "tool_1", ProviderID: "provider_1", Name: "demo", RuntimeProtocol: "mcp"},
		map[string]any{"a": 1},
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("output = %#v, want ok=true", out)
	}
}

func TestInvokeOpenAPIImportSubstitutesPathParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders/ord_123" {
			t.Fatalf("path = %q, want /orders/ord_123", r.URL.Path)
		}
		if got := r.URL.Query().Get("verbose"); got != "true" {
			t.Fatalf("verbose = %q, want true", got)
		}
		_, _ = w.Write([]byte(`{"id":"ord_123"}`))
	}))
	defer server.Close()

	invoker := New()
	out, err := invoker.Invoke(context.Background(),
		tool.ProviderBinding{ID: "provider_1", URI: server.URL},
		tool.AuthBinding{},
		tool.CatalogEntry{
			ID:              "tool_1",
			ProviderID:      "provider_1",
			Name:            "get_order",
			RuntimeProtocol: "mcp",
			MetadataJSON:    `{"source":"openapi_import","method":"GET","path":"/orders/{id}"}`,
		},
		map[string]any{"id": "ord_123", "verbose": true},
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	body, _ := out["body"].(map[string]any)
	if body["id"] != "ord_123" {
		t.Fatalf("output body = %#v, want id=ord_123", body)
	}
}

func TestInvokeRejectsDisallowedProviderURL(t *testing.T) {
	invoker := New().WithProviderURLPolicy(toolsecurity.ProviderURLPolicy{
		AllowedHosts: []string{"tools.example.com"},
	})
	_, err := invoker.Invoke(context.Background(),
		tool.ProviderBinding{ID: "provider_1", URI: "https://internal.example.net"},
		tool.AuthBinding{},
		tool.CatalogEntry{ID: "tool_1", ProviderID: "provider_1", Name: "demo", RuntimeProtocol: "mcp"},
		map[string]any{},
	)
	if err == nil {
		t.Fatal("Invoke() error = nil, want provider policy rejection")
	}
}
