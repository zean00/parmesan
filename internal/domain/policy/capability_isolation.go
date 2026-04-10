package policy

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func (c CapabilityIsolation) AllowsProvider(providerID string) bool {
	return stringAllowed(c.AllowedProviderIDs, providerID)
}

func (c CapabilityIsolation) AllowsTool(toolID string) bool {
	return stringAllowed(c.AllowedToolIDs, toolID)
}

func (c CapabilityIsolation) AllowsAgent(agentID string) bool {
	return stringAllowed(c.AllowedAgentIDs, agentID)
}

func (c CapabilityIsolation) AllowsRetriever(retrieverID string) bool {
	return stringAllowed(c.AllowedRetrieverIDs, retrieverID)
}

func (c CapabilityIsolation) AllowsKnowledgeScope(kind string, id string) bool {
	if len(c.AllowedKnowledgeScopes) == 0 {
		return true
	}
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	if kind == "" || id == "" {
		return false
	}
	for _, item := range c.AllowedKnowledgeScopes {
		if strings.TrimSpace(item.Kind) == kind && strings.TrimSpace(item.ID) == id {
			return true
		}
	}
	return false
}

func (c CapabilityIsolation) FilterCatalog(entries []tool.CatalogEntry) []tool.CatalogEntry {
	if len(c.AllowedProviderIDs) == 0 && len(c.AllowedToolIDs) == 0 {
		return append([]tool.CatalogEntry(nil), entries...)
	}
	out := make([]tool.CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if !c.AllowsProvider(entry.ProviderID) || !c.AllowsTool(entry.ID) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (c CapabilityIsolation) FilterRetrieverBindings(items []RetrieverBinding) []RetrieverBinding {
	if len(c.AllowedRetrieverIDs) == 0 {
		return append([]RetrieverBinding(nil), items...)
	}
	out := make([]RetrieverBinding, 0, len(items))
	for _, item := range items {
		if c.AllowsRetriever(item.ID) {
			out = append(out, item)
		}
	}
	return out
}

func stringAllowed(allowlist []string, value string) bool {
	if len(allowlist) == 0 {
		return true
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range allowlist {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}
