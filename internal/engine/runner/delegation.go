package runner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/sessionwatch"
)

var delegationTemplatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func delegatedAgentOutput(resultText string) map[string]any {
	fields := map[string]any{}
	text := strings.TrimSpace(resultText)
	if text == "" {
		return fields
	}
	fields["raw_result_text"] = text
	fields["result_text"] = text
	parsed := parseDelegatedAgentJSON(text)
	if len(parsed) == 0 {
		return fields
	}
	fields["result"] = parsed
	for key, value := range delegatedAgentScalarFields(parsed) {
		fields[key] = value
	}
	if message := strings.TrimSpace(fmt.Sprint(parsed["user_message"])); message != "" {
		fields["result_text"] = message
	}
	return fields
}

func parseDelegatedAgentJSON(text string) map[string]any {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "{") {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func delegatedAgentScalarFields(values map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		switch value.(type) {
		case string, bool, float64, float32, int, int32, int64, uint, uint32, uint64:
			out[key] = value
		}
	}
	return out
}

func delegatedScalarField(target map[string]any, key string) string {
	return strings.TrimSpace(fmt.Sprint(target[key]))
}

func delegatedAgentUsable(delegated map[string]any) bool {
	if delegated == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(delegated["status"])))
	if status == "failed" || status == "error" || status == "timeout" || status == "canceled" || status == "cancelled" {
		return false
	}
	if strings.TrimSpace(fmt.Sprint(delegated["result_text"])) != "" {
		return true
	}
	if verification, _ := delegated["verification"].(map[string]any); len(verification) > 0 {
		if verified, ok := verification["verified"].(bool); ok && verified {
			return true
		}
	}
	return false
}

func delegatedAgentContract(view resolvedView, delegated map[string]any) (policy.DelegationContract, bool) {
	if delegated == nil || view.Bundle == nil {
		return policy.DelegationContract{}, false
	}
	if contractID := strings.TrimSpace(fmt.Sprint(delegated["contract_id"])); contractID != "" {
		for _, item := range view.Bundle.DelegationContracts {
			if strings.EqualFold(strings.TrimSpace(item.ID), contractID) {
				return item, true
			}
		}
	}
	serverID := strings.TrimSpace(fmt.Sprint(delegated["server_id"]))
	if serverID == "" {
		return policy.DelegationContract{}, false
	}
	for _, item := range view.Bundle.DelegationContracts {
		for _, agentID := range item.AgentIDs {
			if strings.EqualFold(strings.TrimSpace(agentID), serverID) {
				return item, true
			}
		}
	}
	return policy.DelegationContract{}, false
}

func delegatedApplyContract(contract policy.DelegationContract, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	fields["contract_id"] = contract.ID
	result, _ := fields["result"].(map[string]any)
	resource := delegatedExtractResource(contract.FieldAliases, result, fields)
	if len(resource) == 0 {
		resource = map[string]any{}
	}
	if strings.TrimSpace(contract.ResourceType) != "" {
		setDelegatedPath(resource, "type", strings.TrimSpace(contract.ResourceType))
	}
	if strings.TrimSpace(contract.ResultTextField) != "" {
		if text := delegatedLookupString(fields, "result."+strings.TrimSpace(contract.ResultTextField)); text != "" {
			fields["result_text"] = text
		}
	}
	fields["resource"] = resource
	delegatedBackfillAliasFields(contract.FieldAliases, resource, fields, result)
}

func delegatedExtractResource(aliases []policy.DelegationFieldAlias, maps ...map[string]any) map[string]any {
	resource := map[string]any{}
	for _, alias := range aliases {
		target := strings.TrimSpace(alias.Target)
		if !strings.HasPrefix(target, "resource.") {
			continue
		}
		value := delegatedFirstValue(alias.Sources, maps...)
		if isDelegatedEmpty(value) {
			continue
		}
		setDelegatedPath(resource, strings.TrimPrefix(target, "resource."), value)
	}
	return resource
}

func delegatedBackfillAliasFields(aliases []policy.DelegationFieldAlias, resource map[string]any, fields map[string]any, result map[string]any) {
	for _, alias := range aliases {
		target := strings.TrimSpace(alias.Target)
		if !strings.HasPrefix(target, "resource.") {
			continue
		}
		value := delegatedLookup(resource, strings.TrimPrefix(target, "resource."))
		if isDelegatedEmpty(value) {
			continue
		}
		for _, source := range alias.Sources {
			source = strings.TrimSpace(source)
			if source == "" || strings.Contains(source, ".") {
				continue
			}
			fields[source] = value
			if result != nil {
				result[source] = value
			}
		}
	}
}

func delegatedContractMissingFields(contract policy.DelegationContract, fields map[string]any) []string {
	var missing []string
	for _, key := range contract.RequiredResultKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if delegatedLookupString(fields, "result."+key) != "" || delegatedLookupString(fields, key) != "" {
			continue
		}
		missing = append(missing, key)
	}
	return missing
}

func delegatedVerificationTools(contract policy.DelegationContract) []policy.DelegationVerificationTool {
	items := make([]policy.DelegationVerificationTool, 0, 1+len(contract.Verification.FallbackTools))
	if strings.TrimSpace(contract.Verification.PrimaryToolID) != "" {
		items = append(items, policy.DelegationVerificationTool{
			ToolID: strings.TrimSpace(contract.Verification.PrimaryToolID),
			Args:   cloneStringMap(contract.Verification.PrimaryArgs),
		})
	}
	items = append(items, contract.Verification.FallbackTools...)
	return items
}

func delegatedVerificationArgs(raw map[string]string, fields map[string]any) map[string]any {
	args := map[string]any{}
	for key, value := range raw {
		rendered := strings.TrimSpace(renderDelegationTemplate(value, fields))
		if rendered != "" {
			args[key] = rendered
		}
	}
	return args
}

func renderDelegationTemplate(value string, fields map[string]any) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "{{") {
		return value
	}
	return strings.TrimSpace(delegationTemplatePattern.ReplaceAllStringFunc(value, func(token string) string {
		match := delegationTemplatePattern.FindStringSubmatch(token)
		if len(match) != 2 {
			return ""
		}
		return delegatedLookupString(fields, strings.TrimSpace(match[1]))
	}))
}

func delegatedVerifiedResource(contract policy.DelegationContract, output map[string]any) map[string]any {
	if len(output) == 0 {
		return nil
	}
	if !delegatedVerificationOutputUsable(output) {
		return nil
	}
	resource := delegatedExtractResource(contract.Verification.ExtractPaths, output)
	if len(resource) == 0 && len(contract.FieldAliases) > 0 {
		resource = delegatedExtractResource(contract.FieldAliases, output)
	}
	if len(resource) == 0 {
		return nil
	}
	if strings.TrimSpace(contract.ResourceType) != "" {
		setDelegatedPath(resource, "type", strings.TrimSpace(contract.ResourceType))
	}
	return resource
}

func delegatedVerificationOutputUsable(output map[string]any) bool {
	if len(output) == 0 {
		return false
	}
	if errValue, ok := output["error"]; ok && !isDelegatedEmpty(errValue) {
		return false
	}
	return true
}

func delegatedVerifiedResourceSearchResult(contract policy.DelegationContract, output map[string]any, expected map[string]any) map[string]any {
	if len(output) == 0 {
		return nil
	}
	structured, _ := output["structuredContent"].(map[string]any)
	if len(structured) == 0 {
		structured = output
	}
	items, _ := structured["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		resource := delegatedVerifiedResource(contract, item)
		if len(resource) == 0 {
			continue
		}
		if delegatedResourceMatches(expected, resource, contract.Verification.RequireMatchOn) {
			return resource
		}
	}
	return nil
}

func delegatedResourceMatches(expected map[string]any, actual map[string]any, matchOn []string) bool {
	if len(matchOn) == 0 {
		return len(actual) > 0
	}
	matched := false
	for _, key := range matchOn {
		expectedValue := delegatedLookupString(expected, strings.TrimSpace(key))
		if expectedValue == "" {
			continue
		}
		actualValue := delegatedLookupString(actual, strings.TrimSpace(key))
		if actualValue == "" || !strings.EqualFold(actualValue, expectedValue) {
			return false
		}
		matched = true
	}
	return matched
}

func delegatedAgentWatchIntent(view resolvedView, toolOutput map[string]any, now time.Time) (sessionwatch.UpdateIntent, bool) {
	delegated, _ := toolOutput["delegated_agent"].(map[string]any)
	if delegated == nil || !delegatedAgentUsable(delegated) {
		return sessionwatch.UpdateIntent{}, false
	}
	contract, ok := delegatedAgentContract(view, delegated)
	if !ok || strings.TrimSpace(contract.WatchCapabilityID) == "" {
		return sessionwatch.UpdateIntent{}, false
	}
	capability, ok := watchCapabilityByID(view.WatchCapabilities, contract.WatchCapabilityID, "")
	if !ok {
		return sessionwatch.UpdateIntent{}, false
	}
	args := delegatedAgentWatchArguments(delegated)
	subjectRef := sessionwatch.ExtractSubjectRef(args, capability.SubjectKeys...)
	if subjectRef == "" {
		return sessionwatch.UpdateIntent{}, false
	}
	return sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, "", subjectRef, args, now)
}

func delegatedAgentWatchArguments(delegated map[string]any) map[string]any {
	result, _ := delegated["result"].(map[string]any)
	args := cloneAnyMap(result)
	if len(args) == 0 {
		args = map[string]any{}
	}
	for key, value := range delegated {
		if _, exists := args[key]; exists {
			continue
		}
		switch value.(type) {
		case string, bool, float64, float32, int, int32, int64, uint, uint32, uint64:
			args[key] = value
		}
	}
	resource, _ := delegated["resource"].(map[string]any)
	for key, value := range flattenDelegationMap(resource, "resource") {
		args[key] = value
	}
	delete(args, "raw_result_text")
	delete(args, "result_text")
	delete(args, "result")
	delete(args, "verification")
	delete(args, "server_id")
	delete(args, "session_id")
	delete(args, "model")
	delete(args, "mcp_servers")
	delete(args, "prompt_prefix_applied")
	delete(args, "prompt_suffix_applied")
	delete(args, "error")
	return args
}

func flattenDelegationMap(values map[string]any, prefix string) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		fullKey := key
		if strings.TrimSpace(prefix) != "" {
			fullKey = prefix + "." + key
		}
		switch typed := value.(type) {
		case map[string]any:
			for nestedKey, nestedValue := range flattenDelegationMap(typed, fullKey) {
				out[nestedKey] = nestedValue
			}
		default:
			out[fullKey] = typed
		}
	}
	return out
}

func delegatedLookup(values map[string]any, path string) any {
	path = strings.TrimSpace(path)
	if values == nil || path == "" {
		return nil
	}
	current := any(values)
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil
		}
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = mapped[part]
		if !ok {
			return nil
		}
	}
	return current
}

func delegatedLookupString(values map[string]any, path string) string {
	return strings.TrimSpace(fmt.Sprint(delegatedLookup(values, path)))
}

func delegatedFirstValue(paths []string, maps ...map[string]any) any {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		for _, values := range maps {
			if value := delegatedLookup(values, path); !isDelegatedEmpty(value) {
				return value
			}
		}
	}
	return nil
}

func setDelegatedPath(values map[string]any, path string, value any) {
	path = strings.TrimSpace(path)
	if values == nil || path == "" {
		return
	}
	current := values
	parts := strings.Split(path, ".")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, _ := current[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
}

func isDelegatedEmpty(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	}
	return strings.TrimSpace(fmt.Sprint(value)) == ""
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
