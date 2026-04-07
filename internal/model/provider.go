package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sahal/parmesan/internal/config"
)

type Capability string

const (
	CapabilityReasoning  Capability = "reasoning"
	CapabilityStructured Capability = "structured"
	CapabilityEmbedding  Capability = "embedding"
)

type Request struct {
	Prompt string `json:"prompt"`
}

type Response struct {
	Provider string `json:"provider"`
	Text     string `json:"text"`
}

type EmbeddingResponse struct {
	Provider string      `json:"provider"`
	Vectors  [][]float32 `json:"vectors"`
}

type ProviderStats struct {
	Name           string        `json:"name"`
	Capability     Capability    `json:"capability"`
	Healthy        bool          `json:"healthy"`
	SuccessCount   int64         `json:"success_count"`
	FailureCount   int64         `json:"failure_count"`
	LastLatency    time.Duration `json:"last_latency"`
	LastError      string        `json:"last_error,omitempty"`
	LastSelectedAt time.Time     `json:"last_selected_at,omitempty"`
}

type Provider interface {
	Name() string
	Capability() Capability
	Generate(ctx context.Context, req Request) (Response, error)
}

type EmbeddingProvider interface {
	Provider
	Embed(ctx context.Context, texts []string) (EmbeddingResponse, error)
}

type Router struct {
	providers map[string]Provider
	defaults  map[Capability]string
	health    map[string]bool
	stats     map[string]ProviderStats
	mu        sync.RWMutex
}

func NewRouter(cfg config.ProviderConfig) *Router {
	router := &Router{
		providers: map[string]Provider{},
		defaults: map[Capability]string{
			CapabilityReasoning:  capabilityDefault(cfg.DefaultReasoning),
			CapabilityStructured: capabilityDefault(cfg.DefaultStructured),
			CapabilityEmbedding:  capabilityDefault(cfg.DefaultEmbedding),
		},
		health: map[string]bool{},
		stats:  map[string]ProviderStats{},
	}

	router.Register(NewHTTPProvider("openai", CapabilityReasoning, "https://api.openai.com/v1", cfg.OpenAIAPIKey))
	router.Register(NewHTTPProvider("openrouter", CapabilityReasoning, cfg.OpenRouterBase, cfg.OpenRouterAPIKey))
	router.Register(NewHTTPProvider("openai-structured", CapabilityStructured, "https://api.openai.com/v1", cfg.OpenAIAPIKey))
	router.Register(NewHTTPProvider("openrouter-structured", CapabilityStructured, cfg.OpenRouterBase, cfg.OpenRouterAPIKey))
	router.Register(NewHTTPProvider("openai-embedding", CapabilityEmbedding, "https://api.openai.com/v1", cfg.OpenAIAPIKey))
	router.Register(NewHTTPProvider("openrouter-embedding", CapabilityEmbedding, cfg.OpenRouterBase, cfg.OpenRouterAPIKey))

	return router
}

func capabilityDefault(name string) string {
	if name == "" {
		return "openrouter"
	}
	return name
}

func (r *Router) Register(provider Provider) {
	r.providers[provider.Name()] = provider
	r.health[provider.Name()] = true
	r.stats[provider.Name()] = ProviderStats{
		Name:       provider.Name(),
		Capability: provider.Capability(),
		Healthy:    true,
	}
}

func (r *Router) Route(cap Capability) (Provider, error) {
	candidates := r.candidates(cap)
	if len(candidates) == 0 {
		return nil, errors.New("no default provider configured")
	}
	return candidates[0], nil
}

func (r *Router) Generate(ctx context.Context, cap Capability, req Request) (Response, error) {
	candidates := r.candidates(cap)
	if len(candidates) == 0 {
		return Response{}, errors.New("no provider candidates registered")
	}
	var lastErr error
	for _, provider := range candidates {
		start := time.Now()
		resp, err := provider.Generate(ctx, req)
		if err == nil {
			r.setHealthy(provider.Name(), true)
			r.recordResult(provider, true, time.Since(start), "")
			return resp, nil
		}
		r.setHealthy(provider.Name(), false)
		r.recordResult(provider, false, time.Since(start), err.Error())
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("all providers failed")
	}
	return Response{}, lastErr
}

func (r *Router) Embed(ctx context.Context, texts []string) (EmbeddingResponse, error) {
	candidates := r.candidates(CapabilityEmbedding)
	if len(candidates) == 0 {
		return EmbeddingResponse{}, errors.New("no embedding providers registered")
	}
	var lastErr error
	for _, provider := range candidates {
		embedder, ok := provider.(EmbeddingProvider)
		if !ok {
			continue
		}
		start := time.Now()
		resp, err := embedder.Embed(ctx, texts)
		if err == nil {
			r.setHealthy(provider.Name(), true)
			r.recordResult(provider, true, time.Since(start), "")
			return resp, nil
		}
		r.setHealthy(provider.Name(), false)
		r.recordResult(provider, false, time.Since(start), err.Error())
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("all embedding providers failed")
	}
	return EmbeddingResponse{}, lastErr
}

func (r *Router) candidates(cap Capability) []Provider {
	name, ok := r.defaults[cap]
	if !ok {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var preferred []Provider
	var fallback []Provider
	for _, provider := range r.providers {
		if provider.Capability() != cap {
			continue
		}
		healthy := r.health[provider.Name()]
		target := &fallback
		if provider.Name() == name || providerDefaultName(provider.Name()) == name {
			target = &preferred
		} else if !healthy {
			// keep unhealthy providers last
			target = &fallback
		}
		*target = append(*target, provider)
	}
	sort.Slice(preferred, func(i, j int) bool { return preferred[i].Name() < preferred[j].Name() })
	sort.Slice(fallback, func(i, j int) bool {
		li := 0
		if !r.health[fallback[i].Name()] {
			li = 1
		}
		lj := 0
		if !r.health[fallback[j].Name()] {
			lj = 1
		}
		if li != lj {
			return li < lj
		}
		return fallback[i].Name() < fallback[j].Name()
	})
	return append(preferred, fallback...)
}

func providerDefaultName(name string) string {
	name = strings.TrimSuffix(name, "-structured")
	name = strings.TrimSuffix(name, "-embedding")
	return name
}

func (r *Router) setHealthy(name string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health[name] = healthy
	stats := r.stats[name]
	stats.Healthy = healthy
	r.stats[name] = stats
}

func (r *Router) Snapshot() []ProviderStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderStats, 0, len(r.stats))
	for _, item := range r.stats {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Capability != out[j].Capability {
			return out[i].Capability < out[j].Capability
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (r *Router) recordResult(provider Provider, success bool, latency time.Duration, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.stats[provider.Name()]
	stats.Name = provider.Name()
	stats.Capability = provider.Capability()
	stats.Healthy = success
	stats.LastLatency = latency
	stats.LastSelectedAt = time.Now().UTC()
	if success {
		stats.SuccessCount++
		stats.LastError = ""
	} else {
		stats.FailureCount++
		stats.LastError = lastErr
	}
	r.stats[provider.Name()] = stats
}

type HTTPProvider struct {
	name       string
	capability Capability
	baseURL    string
	apiKey     string
	client     *http.Client
}

func NewHTTPProvider(name string, capability Capability, baseURL string, apiKey string) *HTTPProvider {
	return &HTTPProvider{
		name:       name,
		capability: capability,
		baseURL:    baseURL,
		apiKey:     apiKey,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *HTTPProvider) Name() string {
	return p.name
}

func (p *HTTPProvider) Capability() Capability {
	return p.capability
}

func (p *HTTPProvider) Generate(ctx context.Context, req Request) (Response, error) {
	if p.capability == CapabilityEmbedding {
		return Response{}, errors.New("embedding provider does not support Generate")
	}
	if strings.TrimSpace(p.apiKey) == "" {
		return Response{
			Provider: p.name,
			Text:     "provider stub: " + req.Prompt,
		}, nil
	}

	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		return Response{}, errors.New("provider base URL is empty")
	}
	endpoint := baseURL + "/chat/completions"
	modelName := defaultModelForProvider(p.name, p.capability)
	body := map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": req.Prompt},
		},
		"temperature": 0.2,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("%s chat completion failed: status=%d body=%s", p.name, resp.StatusCode, string(respBody))
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return Response{}, err
	}
	if len(decoded.Choices) == 0 {
		return Response{}, errors.New("provider returned no choices")
	}
	return Response{
		Provider: p.name,
		Text:     decoded.Choices[0].Message.Content,
	}, nil
}

func (p *HTTPProvider) Embed(ctx context.Context, texts []string) (EmbeddingResponse, error) {
	if p.capability != CapabilityEmbedding {
		return EmbeddingResponse{}, errors.New("provider does not support embedding")
	}
	if len(texts) == 0 {
		return EmbeddingResponse{Provider: p.name, Vectors: nil}, nil
	}
	if strings.TrimSpace(p.apiKey) == "" {
		return EmbeddingResponse{Provider: p.name, Vectors: deterministicEmbeddings(texts)}, nil
	}
	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		return EmbeddingResponse{}, errors.New("provider base URL is empty")
	}
	body := map[string]any{
		"model": defaultModelForProvider(p.name, p.capability),
		"input": texts,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return EmbeddingResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	if resp.StatusCode >= 300 {
		return EmbeddingResponse{}, fmt.Errorf("%s embeddings failed: status=%d body=%s", p.name, resp.StatusCode, string(respBody))
	}
	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return EmbeddingResponse{}, err
	}
	vectors := make([][]float32, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		vectors = append(vectors, item.Embedding)
	}
	return EmbeddingResponse{Provider: p.name, Vectors: vectors}, nil
}

func defaultModelForProvider(name string, capability Capability) string {
	switch capability {
	case CapabilityStructured:
		if strings.Contains(name, "openrouter") {
			return "openai/gpt-4.1-mini"
		}
		return "gpt-4.1-mini"
	case CapabilityEmbedding:
		if strings.Contains(name, "openrouter") {
			return "openai/text-embedding-3-small"
		}
		return "text-embedding-3-small"
	default:
		if strings.Contains(name, "openrouter") {
			return "openai/gpt-4.1-mini"
		}
		return "gpt-4.1-mini"
	}
}

func deterministicEmbeddings(texts []string) [][]float32 {
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		vector := make([]float32, 64)
		for _, token := range strings.Fields(strings.ToLower(text)) {
			hash := 0
			for _, r := range token {
				hash = (hash*31 + int(r)) % len(vector)
			}
			vector[hash]++
		}
		out = append(out, vector)
	}
	return out
}
