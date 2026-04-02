package policyruntime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sahal/parmesan/internal/model"
)

const (
	defaultARQBatchSize = 4
	defaultARQRetries   = 3
)

func generateStructuredWithRetry(ctx context.Context, router *model.Router, prompt string, out any) bool {
	if router == nil {
		return false
	}
	for attempt := 0; attempt < defaultARQRetries; attempt++ {
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

func generateStructured(ctx context.Context, router *model.Router, prompt string, out any) bool {
	if router == nil {
		return false
	}
	resp, err := router.Generate(ctx, model.CapabilityStructured, model.Request{Prompt: prompt})
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
