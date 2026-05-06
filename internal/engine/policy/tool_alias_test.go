package policyruntime

import (
	"strings"
	"testing"

	"github.com/sahal/parmesan/internal/domain/tool"
)

func TestBuildToolModelNameMapUsesConfiguredAliases(t *testing.T) {
	catalog := []tool.CatalogEntry{
		{ID: "builtin_current_time", ProviderID: "builtin", Name: "get_current_time"},
		{ID: "commerce_get_order", ProviderID: "commerce", Name: "get.order"},
	}
	got := BuildToolModelNameMap(catalog, map[string]string{
		"builtin.get_current_time": "get_current_time",
		"commerce_get_order":       "commerce_lookup",
	})
	if got["builtin.get_current_time"] != "get_current_time" {
		t.Fatalf("builtin alias = %q, want get_current_time", got["builtin.get_current_time"])
	}
	if got["commerce.get.order"] != "commerce_lookup" {
		t.Fatalf("commerce alias = %q, want commerce_lookup", got["commerce.get.order"])
	}
}

func TestBuildToolModelNameMapSanitizesAndDisambiguates(t *testing.T) {
	catalog := []tool.CatalogEntry{
		{ID: "a", ProviderID: "mcp", Name: "tool.one"},
		{ID: "b", ProviderID: "mcp_tool", Name: "one"},
		{ID: "c", ProviderID: "9bad", Name: "name"},
	}
	got := BuildToolModelNameMap(catalog, nil)
	if !strings.HasPrefix(got["mcp.tool.one"], "mcp_tool_one_") {
		t.Fatalf("first collision alias = %q, want mcp_tool_one_<hash>", got["mcp.tool.one"])
	}
	if !strings.HasPrefix(got["mcp_tool.one"], "mcp_tool_one_") {
		t.Fatalf("second collision alias = %q, want mcp_tool_one_<hash>", got["mcp_tool.one"])
	}
	if got["9bad.name"] != "tool_9bad_name" {
		t.Fatalf("digit-prefixed alias = %q, want tool_9bad_name", got["9bad.name"])
	}
}

func TestFirstMatchingCandidateToolIDAcceptsModelToolName(t *testing.T) {
	candidates := []ToolCandidate{{
		ToolID:         "builtin.get_current_time",
		ModelToolName:  "get_current_time",
		ToolName:       "get_current_time",
		CatalogEntryID: "builtin_current_time",
	}}
	if got := firstMatchingCandidateToolID(candidates, "get_current_time"); got != "builtin.get_current_time" {
		t.Fatalf("matched tool = %q, want canonical tool id", got)
	}
}

func TestFirstMatchingCandidateToolIDPrefersModelAliasOverRawName(t *testing.T) {
	candidates := []ToolCandidate{
		{ToolID: "commerce.get_order", ModelToolName: "lookup", ToolName: "get_order"},
		{ToolID: "support.lookup", ModelToolName: "support_lookup", ToolName: "lookup"},
	}
	if got := firstMatchingCandidateToolID(candidates, "lookup"); got != "commerce.get_order" {
		t.Fatalf("matched tool = %q, want model alias target", got)
	}
}
