package model

import (
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
