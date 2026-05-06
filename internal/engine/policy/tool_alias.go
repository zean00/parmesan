package policyruntime

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func BuildToolModelNameMap(catalog []tool.CatalogEntry, configured map[string]string) map[string]string {
	if len(catalog) == 0 {
		return nil
	}
	resolvedConfigured := resolveConfiguredToolAliases(catalog, configured)
	entries := append([]tool.CatalogEntry(nil), catalog...)
	sort.SliceStable(entries, func(i, j int) bool {
		return QualifiedToolID(entries[i]) < QualifiedToolID(entries[j])
	})
	baseByTool := map[string]string{}
	counts := map[string]int{}
	for _, entry := range entries {
		canonical := QualifiedToolID(entry)
		if canonical == "" {
			continue
		}
		base := sanitizeModelToolName(resolvedConfigured[canonical])
		if base == "" {
			base = sanitizeModelToolName(canonical)
		}
		if base == "" {
			base = "tool"
		}
		baseByTool[canonical] = base
		counts[base]++
	}
	out := map[string]string{}
	used := map[string]struct{}{}
	for _, entry := range entries {
		canonical := QualifiedToolID(entry)
		base := baseByTool[canonical]
		if canonical == "" || base == "" {
			continue
		}
		alias := base
		if counts[base] > 1 {
			alias = base + "_" + shortToolAliasHash(canonical)
		}
		if _, exists := used[alias]; exists {
			alias = base + "_" + shortToolAliasHash(canonical)
		}
		for i := 2; ; i++ {
			if _, exists := used[alias]; !exists {
				break
			}
			alias = fmt.Sprintf("%s_%s_%d", base, shortToolAliasHash(canonical), i)
		}
		used[alias] = struct{}{}
		out[canonical] = alias
	}
	return out
}

func resolveConfiguredToolAliases(catalog []tool.CatalogEntry, configured map[string]string) map[string]string {
	if len(configured) == 0 {
		return nil
	}
	out := map[string]string{}
	keys := make([]string, 0, len(configured))
	for key := range configured {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry, ok, err := ResolveToolCatalogEntry(catalog, key)
		if err != nil || !ok {
			continue
		}
		alias := sanitizeModelToolName(configured[key])
		if alias == "" {
			continue
		}
		out[QualifiedToolID(entry)] = alias
	}
	return out
}

func sanitizeModelToolName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
		if valid && r <= 127 {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	first := rune(out[0])
	if first >= '0' && first <= '9' {
		out = "tool_" + out
	}
	return out
}

func shortToolAliasHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return fmt.Sprintf("%x", sum[:])[:8]
}
