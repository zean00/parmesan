package orbyte

import (
	"strings"

	policyruntime "github.com/sahal/parmesan/internal/engine/policy"
)

type ToolArgumentResolver struct{}

func NewToolArgumentResolver() ToolArgumentResolver {
	return ToolArgumentResolver{}
}

func (ToolArgumentResolver) ResolveToolArguments(matchCtx policyruntime.MatchingContext, identity policyruntime.ToolIdentity, fields []string) map[string]any {
	lowerTool := strings.ToLower(strings.TrimSpace(identity.ToolName))
	if lowerTool == "" {
		lowerTool = strings.ToLower(strings.TrimSpace(identity.ToolID))
		if idx := strings.LastIndex(lowerTool, "."); idx >= 0 {
			lowerTool = lowerTool[idx+1:]
		}
		if lowerTool == "" {
			return nil
		}
	}
	text := strings.TrimSpace(matchCtx.LatestCustomerText)
	if text == "" {
		text = strings.TrimSpace(matchCtx.ConversationText)
	}
	if text == "" {
		return nil
	}

	fieldSet := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field != "" {
			fieldSet[field] = struct{}{}
		}
	}

	switch lowerTool {
	case "commercial_core.item.search":
		return resolveCommercialItemSearchArgs(text, fieldSet)
	case "commercial_core.item.get":
		return resolveCommercialItemGetArgs(text, fieldSet)
	case "crm.customer.summary", "crm.lead.search", "crm.opportunity.search", "crm.activity.create", "crm.lead.create":
		return resolveCRMArgs(text, fieldSet)
	case "crm.lead.find_or_create_for_product_interest":
		return resolveProductInterestLeadArgs(text, fieldSet)
	default:
		return nil
	}
}

func resolveCommercialItemSearchArgs(text string, fields map[string]struct{}) map[string]any {
	query := extractProductTarget(text)
	if query == "" {
		return nil
	}
	args := map[string]any{}
	if wantsField(fields, "query") {
		args["query"] = query
	}
	return args
}

func resolveCommercialItemGetArgs(text string, fields map[string]struct{}) map[string]any {
	query := extractProductTarget(text)
	if query == "" {
		return nil
	}
	args := map[string]any{}
	if wantsField(fields, "name") {
		args["name"] = query
	}
	return args
}

func resolveCRMArgs(text string, fields map[string]struct{}) map[string]any {
	target := extractCustomerTarget(text)
	if target == "" {
		return nil
	}
	args := map[string]any{}
	for _, field := range []string{"party_name", "customer_name", "name", "query"} {
		if wantsField(fields, field) {
			args[field] = target
		}
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

func resolveProductInterestLeadArgs(text string, fields map[string]struct{}) map[string]any {
	product := extractProductTarget(text)
	customer := extractCustomerTarget(text)
	args := map[string]any{}
	if product != "" {
		for _, field := range []string{"product_name", "name"} {
			if wantsField(fields, field) {
				args[field] = product
			}
		}
		if wantsField(fields, "title") {
			args["title"] = "Interest in " + product
		}
	}
	if wantsField(fields, "confirm_apply") {
		args["confirm_apply"] = true
	}
	if customer != "" {
		for _, field := range []string{"party_name", "customer_name", "query"} {
			if wantsField(fields, field) {
				args[field] = customer
			}
		}
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

func wantsField(fields map[string]struct{}, field string) bool {
	_, ok := fields[strings.ToLower(strings.TrimSpace(field))]
	return ok
}

func extractProductTarget(text string) string {
	for _, marker := range []string{"evaluating ", "about ", "received ", "the "} {
		if value := extractBetween(text, marker, " for ", ".", ",", " so ", " and "); value != "" {
			return value
		}
	}
	return ""
}

func extractCustomerTarget(text string) string {
	for _, marker := range []string{" for ", "purchasing for ", "contact ", "follow up with "} {
		if value := extractBetween(text, marker, ".", ",", " (", " about ", " please ", " then ", " and ", " so "); value != "" {
			return value
		}
	}
	return ""
}

func extractBetween(text string, marker string, endMarkers ...string) string {
	if strings.TrimSpace(text) == "" || strings.TrimSpace(marker) == "" {
		return ""
	}
	lower := strings.ToLower(text)
	markerLower := strings.ToLower(marker)
	idx := strings.Index(lower, markerLower)
	if idx < 0 {
		return ""
	}
	start := idx + len(markerLower)
	remainder := text[start:]
	cut := len(remainder)
	remainderLower := strings.ToLower(remainder)
	for _, end := range endMarkers {
		end = strings.ToLower(end)
		if end == "" {
			continue
		}
		if pos := strings.Index(remainderLower, end); pos >= 0 && pos < cut {
			cut = pos
		}
	}
	return strings.TrimSpace(strings.Trim(remainder[:cut], " .,:;()"))
}
