package policyruntime

import (
	"strings"
	"sync"

	semantics "github.com/sahal/parmesan/internal/engine/semantics"
)

type toolSelectionEvalCache struct {
	candidateIDs []string
	results      map[string]semantics.ToolSelectionEvidence
	mu           sync.Mutex
}

func newToolSelectionEvalCache(candidates []ToolCandidate) *toolSelectionEvalCache {
	return &toolSelectionEvalCache{
		candidateIDs: candidateToolIDs(candidates),
		results:      map[string]semantics.ToolSelectionEvidence{},
	}
}

func (c *toolSelectionEvalCache) evaluate(candidate ToolCandidate, selectedToolID string) semantics.ToolSelectionEvidence {
	if c == nil {
		return semantics.DefaultToolSelectionEvaluator{}.Evaluate(
			semantics.ToolSelectionContextFromIDs(candidate.ToolID, candidate.ReferenceTools, selectedToolID, candidateToolIDs([]ToolCandidate{candidate})),
		)
	}
	key := strings.TrimSpace(candidate.ToolID) + "\x00" + strings.TrimSpace(selectedToolID) + "\x00" + strings.Join(candidate.ReferenceTools, "\x00")
	c.mu.Lock()
	if evidence, ok := c.results[key]; ok {
		c.mu.Unlock()
		return evidence
	}
	c.mu.Unlock()
	evidence := semantics.DefaultToolSelectionEvaluator{}.Evaluate(
		semantics.ToolSelectionContextFromIDs(candidate.ToolID, candidate.ReferenceTools, selectedToolID, c.candidateIDs),
	)
	c.mu.Lock()
	c.results[key] = evidence
	c.mu.Unlock()
	return evidence
}
