package policyruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/sahal/parmesan/internal/model"
)

const (
	defaultARQBatchSize = 4
	defaultARQRetries   = 3
)

type structuredRetryAttemptsContextKey struct{}

func WithStructuredRetryAttempts(ctx context.Context, attempts int) context.Context {
	if attempts <= 0 {
		return ctx
	}
	return context.WithValue(ctx, structuredRetryAttemptsContextKey{}, attempts)
}

func generateStructuredWithRetry(ctx context.Context, router *model.Router, prompt string, out any) bool {
	if router == nil {
		return false
	}
	attempts := defaultARQRetries
	if configured, ok := ctx.Value(structuredRetryAttemptsContextKey{}).(int); ok && configured > attempts {
		attempts = configured
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if generateStructured(ctx, router, prompt, out) {
			return true
		}
	}
	return false
}

func chunkGeneric[T any](items []T, size int) [][]T {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 {
		size = defaultARQBatchSize
	}
	var out [][]T
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}

func processBatchesInParallel[T any, R any](ctx context.Context, batches []T, fn func(context.Context, T) (R, bool)) ([]R, bool) {
	if len(batches) == 0 {
		return nil, true
	}
	results := make([]R, len(batches))
	ok := make([]bool, len(batches))
	var wg sync.WaitGroup
	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch T) {
			defer wg.Done()
			results[i], ok[i] = fn(ctx, batch)
		}(i, batch)
	}
	wg.Wait()
	for _, item := range ok {
		if !item {
			return nil, false
		}
	}
	return results, true
}

func generateStructured(ctx context.Context, router *model.Router, prompt string, out any) bool {
	if router == nil {
		return false
	}
	resp, err := router.Generate(ctx, model.CapabilityStructured, model.RequestWithContextOverride(ctx, model.CapabilityStructured, model.Request{Prompt: prompt}))
	if err != nil {
		return false
	}
	raw := strings.TrimSpace(resp.Text)
	if strings.HasPrefix(raw, "provider stub: ") {
		return false
	}
	raw = extractJSONObject(raw)
	if raw == "" {
		return false
	}
	return json.Unmarshal([]byte(raw), out) == nil
}
