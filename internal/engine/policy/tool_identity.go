package policyruntime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func ToolIdentityForEntry(entry tool.CatalogEntry) ToolIdentity {
	return ToolIdentity{
		ToolID:         QualifiedToolID(entry),
		ProviderID:     strings.TrimSpace(entry.ProviderID),
		ToolName:       strings.TrimSpace(entry.Name),
		CatalogEntryID: strings.TrimSpace(entry.ID),
	}
}

func QualifiedToolID(entry tool.CatalogEntry) string {
	providerID := strings.TrimSpace(entry.ProviderID)
	toolName := strings.TrimSpace(entry.Name)
	switch {
	case providerID != "" && toolName != "":
		return providerID + "." + toolName
	case toolName != "":
		return toolName
	default:
		return strings.TrimSpace(entry.ID)
	}
}

func ToolRefMatchesEntry(ref string, entry tool.CatalogEntry) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	qualified := QualifiedToolID(entry)
	legacyQualified := strings.TrimSpace(entry.ProviderID + ":" + entry.Name)
	switch ref {
	case qualified, strings.TrimSpace(entry.ID), strings.TrimSpace(entry.Name), legacyQualified:
		return true
	}
	if strings.Contains(ref, ":") {
		ref = strings.ReplaceAll(ref, ":", ".")
	}
	return ref == qualified
}

func ResolveToolCatalogEntries(catalog []tool.CatalogEntry, ref string) []tool.CatalogEntry {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	var direct []tool.CatalogEntry
	var byName []tool.CatalogEntry
	for _, entry := range catalog {
		if ToolRefMatchesEntry(ref, entry) {
			if strings.TrimSpace(entry.Name) == ref {
				byName = append(byName, entry)
				continue
			}
			direct = append(direct, entry)
		} else if strings.TrimSpace(entry.Name) == ref {
			byName = append(byName, entry)
		}
	}
	if len(direct) > 0 {
		return dedupeCatalogEntries(direct)
	}
	return dedupeCatalogEntries(byName)
}

func ResolveToolCatalogEntry(catalog []tool.CatalogEntry, ref string) (tool.CatalogEntry, bool, error) {
	matches := ResolveToolCatalogEntries(catalog, ref)
	switch len(matches) {
	case 0:
		return tool.CatalogEntry{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		ids := make([]string, 0, len(matches))
		for _, entry := range matches {
			ids = append(ids, QualifiedToolID(entry))
		}
		sort.Strings(ids)
		return tool.CatalogEntry{}, false, fmt.Errorf("ambiguous tool reference %q matches %v", ref, ids)
	}
}

func dedupeCatalogEntries(entries []tool.CatalogEntry) []tool.CatalogEntry {
	seen := map[string]struct{}{}
	out := make([]tool.CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		key := QualifiedToolID(entry)
		if key == "" {
			key = strings.TrimSpace(entry.ID)
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}
