package runner

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
)

func bundleCapabilityIsolation(bundles []policy.Bundle) policy.CapabilityIsolation {
	if len(bundles) == 0 {
		return policy.CapabilityIsolation{}
	}
	return bundles[0].CapabilityIsolation
}

func filterCatalogForBundles(entries []tool.CatalogEntry, bundles []policy.Bundle) []tool.CatalogEntry {
	return bundleCapabilityIsolation(bundles).FilterCatalog(entries)
}

func knowledgeScopeAllowed(bundles []policy.Bundle, kind string, id string) bool {
	return bundleCapabilityIsolation(bundles).AllowsKnowledgeScope(kind, id)
}

func candidateKnowledgeScope(bundles []policy.Bundle, kind string, id string) (string, string) {
	if !knowledgeScopeAllowed(bundles, kind, id) {
		return "", ""
	}
	return strings.TrimSpace(kind), strings.TrimSpace(id)
}
