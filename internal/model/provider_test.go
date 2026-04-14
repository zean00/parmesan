package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sahal/parmesan/internal/config"
)

func TestDefaultEmbeddingProviderPrefersOpenRouterEmbedding(t *testing.T) {
	router := NewRouter(config.ProviderConfig{
		OpenRouterAPIKey:  "sk-test",
		OpenRouterBase:    "https://openrouter.test/api/v1",
		DefaultEmbedding:  "openrouter",
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
	})

	provider, err := router.Route(CapabilityEmbedding)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "openrouter-embedding" {
		t.Fatalf("embedding provider = %q, want openrouter-embedding", provider.Name())
	}
}

func TestGenerateUsesProviderOverride(t *testing.T) {
	openaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "openai"}}},
		})
	}))
	defer openaiServer.Close()
	openrouterServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "openrouter"}}},
		})
	}))
	defer openrouterServer.Close()

	router := NewRouter(config.ProviderConfig{
		OpenAIAPIKey:      "sk-openai",
		OpenRouterAPIKey:  "sk-openrouter",
		OpenRouterBase:    openrouterServer.URL,
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
	})
	router.Register(NewHTTPProvider("openai", CapabilityReasoning, openaiServer.URL, "sk-openai"))

	resp, err := router.Generate(context.Background(), CapabilityReasoning, Request{
		Prompt:           "hello",
		ProviderOverride: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "openai" || resp.Text != "openai" {
		t.Fatalf("resp = %#v, want openai override result", resp)
	}
}

func TestSupportsRejectsUnknownProviderOverride(t *testing.T) {
	router := NewRouter(config.ProviderConfig{
		DefaultReasoning:  "openrouter",
		DefaultStructured: "openrouter",
		DefaultEmbedding:  "openrouter",
	})
	if router.Supports(CapabilityReasoning, "does-not-exist") {
		t.Fatal("Supports() = true, want false for unknown provider override")
	}
}

func TestGenerateDoesNotFallbackWhenProviderOverrideFails(t *testing.T) {
	router := &Router{
		providers: map[string]Provider{
			"forced": stubProvider{name: "forced", capability: CapabilityReasoning, err: errors.New("forced failure")},
			"other":  stubProvider{name: "other", capability: CapabilityReasoning, text: "other"},
		},
		defaults: map[Capability]string{
			CapabilityReasoning: "other",
		},
		health: map[string]bool{
			"forced": true,
			"other":  true,
		},
		stats: map[string]ProviderStats{
			"forced": {Name: "forced", Capability: CapabilityReasoning, Healthy: true},
			"other":  {Name: "other", Capability: CapabilityReasoning, Healthy: true},
		},
	}

	_, err := router.Generate(context.Background(), CapabilityReasoning, Request{
		Prompt:           "hello",
		ProviderOverride: "forced",
	})
	if err == nil || err.Error() != "forced failure" {
		t.Fatalf("err = %v, want forced failure", err)
	}
}

func TestGenerateUsesModelOverride(t *testing.T) {
	var seenModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		seenModel, _ = body["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer server.Close()

	provider := NewHTTPProvider("openrouter", CapabilityReasoning, server.URL, "sk-openrouter")
	resp, err := provider.Generate(context.Background(), Request{Prompt: "hello", ModelOverride: "custom/model"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" {
		t.Fatalf("resp.Text = %q, want ok", resp.Text)
	}
	if seenModel != "custom/model" {
		t.Fatalf("model = %q, want custom/model", seenModel)
	}
}

type stubProvider struct {
	name       string
	capability Capability
	text       string
	err        error
}

func (p stubProvider) Name() string {
	return p.name
}

func (p stubProvider) Capability() Capability {
	return p.capability
}

func (p stubProvider) Generate(_ context.Context, _ Request) (Response, error) {
	if p.err != nil {
		return Response{}, p.err
	}
	return Response{Provider: p.name, Text: p.text}, nil
}
