package policyruntime

import "testing"

type staticToolArgumentResolver map[string]any

func (s staticToolArgumentResolver) ResolveToolArguments(MatchingContext, ToolIdentity, []string) map[string]any {
	return s
}

func TestInferToolArgumentsFromContextPrefersResolverValues(t *testing.T) {
	specs := map[string]toolArgumentSpec{
		"query": {},
	}
	matchCtx := MatchingContext{
		LatestCustomerText: "I'm also evaluating Espresso Double 20260417-011117 for CRM Demo Customer 20260417011116. Please tell me the key product details and why it fits a compact counter setup, then have sales follow up if it looks suitable.",
	}

	got := inferToolArgumentsFromContext(matchCtx, ToolIdentity{ToolID: "orbyte_full.crm.customer.summary", ProviderID: "orbyte_full", ToolName: "crm.customer.summary"}, specs, staticToolArgumentResolver{
		"query": "CRM Demo Customer 20260417011116",
	})

	if got["query"] != "CRM Demo Customer 20260417011116" {
		t.Fatalf("query = %#v, want resolver-derived customer target", got["query"])
	}
}
