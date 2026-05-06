package toolruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sahal/parmesan/internal/builtintools"
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

func TestInvokePropagatesIdempotencyToMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "idem_123" {
			t.Fatalf("Idempotency-Key = %q, want idem_123", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		params, _ := body["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if got, _ := meta["idempotency_key"].(string); got != "idem_123" {
			t.Fatalf("_meta.idempotency_key = %q, want idem_123", got)
		}
		_, _ = w.Write([]byte(`{"result":{"ok":true}}`))
	}))
	defer server.Close()

	_, err := New().InvokeWithOptions(context.Background(),
		tool.ProviderBinding{ID: "provider_1", URI: server.URL},
		tool.AuthBinding{},
		tool.CatalogEntry{ID: "tool_1", ProviderID: "provider_1", Name: "demo", RuntimeProtocol: "mcp"},
		map[string]any{"a": 1},
		InvokeOptions{IdempotencyKey: "idem_123"},
	)
	if err != nil {
		t.Fatalf("InvokeWithOptions() error = %v", err)
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

func TestInvokeNativeBuiltInBypassesRemoteURLPolicy(t *testing.T) {
	invoker := New().WithProviderURLPolicy(toolsecurity.ProviderURLPolicy{
		AllowedHosts: []string{"tools.example.com"},
	})
	out, err := invoker.Invoke(context.Background(),
		tool.ProviderBinding{ID: builtintools.ProviderID, Kind: tool.ProviderNative},
		tool.AuthBinding{},
		tool.CatalogEntry{ID: builtintools.CurrentTimeToolID, ProviderID: builtintools.ProviderID, Name: builtintools.CurrentTimeName, RuntimeProtocol: "native"},
		map[string]any{"timezone": "UTC+07:00"},
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out["utc_offset"] != "+07:00" {
		t.Fatalf("output = %#v, want +07:00 offset", out)
	}
}
