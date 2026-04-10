package customercontext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/session"
)

func TestEnricherHTTPSourceMergesPromptSafeCustomerContext(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cust_crm","profile":{"name":"Ada","tier":"vip","locale":"id-ID"}}`))
	}))
	defer server.Close()

	enricher := New(config.CustomerContextConfig{Enrichment: config.CustomerContextEnrichmentConfig{
		Enabled:        true,
		TimeoutSeconds: 2,
		Sources: []config.CustomerContextEnrichmentSourceConfig{{
			ID:               "crm",
			Type:             "http",
			MergeStrategy:    "overwrite",
			PromptSafeFields: []string{"name", "tier", "locale"},
			Request: config.CustomerContextHTTPRequestConfig{
				Method:       "POST",
				URL:          server.URL,
				BodyTemplate: `{"customer_id":"{{ .customer_id }}","email":"{{ .customer_context.email }}"}`,
			},
			ResponseMapping: config.CustomerContextMappingConfig{
				CustomerID: "$.id",
				CustomerContext: map[string]string{
					"name":   "$.profile.name",
					"tier":   "$.profile.tier",
					"locale": "$.profile.locale",
				},
			},
		}},
	}})
	got, err := enricher.Enrich(context.Background(), session.Session{
		ID:         "sess_1",
		AgentID:    "agent_1",
		CustomerID: "cust_1",
		Channel:    "acp",
		Metadata: map[string]any{
			"customer_context": map[string]any{"email": "ada@example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["customer_id"] != "cust_1" || gotBody["email"] != "ada@example.com" {
		t.Fatalf("request body = %#v, want templated customer fields", gotBody)
	}
	ctx, _ := got.Metadata["customer_context"].(map[string]any)
	if got.CustomerID != "cust_crm" || ctx["name"] != "Ada" || ctx["tier"] != "vip" || ctx["customer_id"] != "cust_crm" {
		t.Fatalf("enriched session = %#v context=%#v", got, ctx)
	}
	if fields := stringSlice(got.Metadata["customer_context_prompt_safe_fields"]); len(fields) != 3 {
		t.Fatalf("prompt safe fields = %#v, want 3", fields)
	}
}

func TestEnricherKeepBothStoresAlternates(t *testing.T) {
	enricher := New(config.CustomerContextConfig{Enrichment: config.CustomerContextEnrichmentConfig{
		Enabled: true,
		Sources: []config.CustomerContextEnrichmentSourceConfig{{
			ID:              "static",
			Type:            "static",
			MergeStrategy:   "keep_both",
			CustomerContext: map[string]any{"tier": "gold"},
		}},
	}})
	got, err := enricher.Enrich(context.Background(), session.Session{
		ID:         "sess_1",
		AgentID:    "agent_1",
		CustomerID: "cust_1",
		Metadata: map[string]any{
			"customer_context": map[string]any{"tier": "vip"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := got.Metadata["customer_context"].(map[string]any)
	if ctx["tier"] != "vip" {
		t.Fatalf("customer_context = %#v, want original canonical tier", ctx)
	}
	alts, _ := got.Metadata["customer_context_alternates"].(map[string]any)
	if len(alts) == 0 {
		t.Fatalf("metadata = %#v, want alternates", got.Metadata)
	}
}

func TestEnricherFailSessionReturnsError(t *testing.T) {
	enricher := New(config.CustomerContextConfig{Enrichment: config.CustomerContextEnrichmentConfig{
		Enabled:        true,
		TimeoutSeconds: 1,
		OnError:        "fail_session",
		Sources: []config.CustomerContextEnrichmentSourceConfig{{
			ID: "bad", Type: "sql", DatabaseURL: "", Query: "select 1",
		}},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := enricher.Enrich(ctx, session.Session{ID: "sess_1", Metadata: map[string]any{}})
	if err == nil {
		t.Fatal("Enrich() error = nil, want failure")
	}
}
