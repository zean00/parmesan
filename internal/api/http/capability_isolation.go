package httpapi

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
)

func policyCapabilityIsolation(bundles []policy.Bundle) policy.CapabilityIsolation {
	if len(bundles) == 0 {
		return policy.CapabilityIsolation{}
	}
	return bundles[0].CapabilityIsolation
}

func filterCatalogForPolicy(entries []tool.CatalogEntry, bundles []policy.Bundle) []tool.CatalogEntry {
	return policyCapabilityIsolation(bundles).FilterCatalog(entries)
}

func allowedKnowledgeScopeForPolicy(bundles []policy.Bundle, kind string, id string) (string, string) {
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	if !policyCapabilityIsolation(bundles).AllowsKnowledgeScope(kind, id) {
		return "", ""
	}
	return kind, id
}

func capabilityIsolationPayload(item policy.CapabilityIsolation) map[string]any {
	scopes := make([]map[string]any, 0, len(item.AllowedKnowledgeScopes))
	for _, scope := range item.AllowedKnowledgeScopes {
		scopes = append(scopes, map[string]any{
			"kind": scope.Kind,
			"id":   scope.ID,
		})
	}
	return map[string]any{
		"allowed_provider_ids":     append([]string(nil), item.AllowedProviderIDs...),
		"allowed_tool_ids":         append([]string(nil), item.AllowedToolIDs...),
		"allowed_retriever_ids":    append([]string(nil), item.AllowedRetrieverIDs...),
		"allowed_knowledge_scopes": scopes,
	}
}
