package policyruntime

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func projectJourneyNodes(bundle policy.Bundle) []ProjectedJourneyNode {
	var out []ProjectedJourneyNode
	for _, j := range bundle.Journeys {
		indexByState := map[string]int{}
		for i, state := range journeyOrderedStates(j) {
			indexByState[state.ID] = i + 1
		}
		for _, state := range j.States {
			nodeKind := projectedJourneyNodeKind(state)
			customerDependent, agentDependent := projectedJourneyDependencyFlags(state, nodeKind)
			item := ProjectedJourneyNode{
				ID:              projectedStateID(j.ID, state.ID),
				JourneyID:       j.ID,
				StateID:         state.ID,
				SourceEdgeID:    projectedSourceEdgeID(j, state.ID),
				Index:           indexByState[state.ID],
				Instruction:     state.Instruction,
				FollowUps:       projectFollowUps(j, state.ID),
				LegalFollowUps:  projectFollowUps(j, state.ID),
				Labels:          dedupe(append(append(append([]string(nil), j.Labels...), j.When...), state.Labels...)),
				CompositionMode: strings.TrimSpace(firstNonEmpty(state.CompositionMode, state.Mode, j.CompositionMode, jMode(j))),
				Metadata: map[string]any{
					"journey_node": map[string]any{
						"journey_id": j.ID,
						"state_id":   state.ID,
						"index":      indexByState[state.ID],
						"follow_ups": projectFollowUps(j, state.ID),
						"labels":     dedupe(append(append(append([]string(nil), j.Labels...), j.When...), state.Labels...)),
						"kind":       nodeKind,
						"type":       strings.TrimSpace(state.Type),
					},
					"customer_dependent_action_data": map[string]any{
						"is_customer_dependent": customerDependent,
						"customer_action":       strings.TrimSpace(state.Instruction),
						"is_agent_dependent":    agentDependent,
						"agent_action":          strings.TrimSpace(state.Instruction),
					},
				},
				Priority: j.Priority + state.Priority,
			}
			mergeJourneyMetadata(item.Metadata, j.Metadata)
			mergeJourneyMetadata(item.Metadata, state.Metadata)
			if edge := incomingEdge(j, state.ID); edge != nil {
				item.SourceEdgeID = edge.ID
				mergeJourneyMetadata(item.Metadata, edge.Metadata)
				if nodeMeta, ok := item.Metadata["journey_node"].(map[string]any); ok {
					nodeMeta["source_edge_id"] = edge.ID
					if strings.TrimSpace(edge.Condition) != "" {
						nodeMeta["edge_condition"] = edge.Condition
					}
				}
			}
			if strings.TrimSpace(state.Tool) != "" {
				item.ToolRefs = append(item.ToolRefs, state.Tool)
			}
			if state.MCP != nil {
				if strings.TrimSpace(state.MCP.Tool) != "" {
					item.ToolRefs = append(item.ToolRefs, state.MCP.Server+"."+state.MCP.Tool)
				}
				for _, toolName := range state.MCP.Tools {
					item.ToolRefs = append(item.ToolRefs, state.MCP.Server+"."+toolName)
				}
			}
			out = append(out, item)
		}
	}
	return out
}

func projectedJourneyNodeKind(state policy.JourneyNode) string {
	if strings.TrimSpace(state.Kind) != "" {
		return strings.TrimSpace(strings.ToLower(state.Kind))
	}
	switch strings.ToLower(strings.TrimSpace(state.Type)) {
	case "tool":
		return "tool"
	case "message", "chat":
		return "chat"
	default:
		return "na"
	}
}

func projectedJourneyDependencyFlags(state policy.JourneyNode, kind string) (customer bool, agent bool) {
	switch kind {
	case "tool":
		return false, false
	case "fork":
		return false, false
	default:
		if strings.TrimSpace(state.Instruction) == "" {
			return false, false
		}
		return true, strings.EqualFold(kind, "chat")
	}
}

func projectedStateID(journeyID string, stateID string) string {
	return "journey_node:" + journeyID + ":" + stateID
}

func projectFollowUps(j policy.Journey, stateID string) []string {
	out := []string{}
	for _, edge := range outgoingEdges(j, stateID) {
		if strings.TrimSpace(edge.Target) == "" {
			continue
		}
		out = append(out, projectedStateID(j.ID, edge.Target))
	}
	if len(out) == 0 {
		if state := findJourneyState(j, stateID); state != nil {
			for _, next := range state.Next {
				next = strings.TrimSpace(next)
				if next != "" {
					out = append(out, projectedStateID(j.ID, next))
				}
			}
		}
	}
	return dedupe(out)
}

func projectedSourceEdgeID(j policy.Journey, stateID string) string {
	if edge := incomingEdge(j, stateID); edge != nil {
		return edge.ID
	}
	return ""
}

func jMode(j policy.Journey) string {
	for _, tmpl := range j.Templates {
		if strings.TrimSpace(tmpl.Mode) != "" {
			return tmpl.Mode
		}
	}
	return ""
}

func incomingEdge(j policy.Journey, stateID string) *policy.JourneyEdge {
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.Target) == strings.TrimSpace(stateID) {
			copied := edge
			return &copied
		}
	}
	return nil
}

func outgoingEdges(j policy.Journey, sourceID string) []policy.JourneyEdge {
	var out []policy.JourneyEdge
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.Source) == strings.TrimSpace(sourceID) {
			out = append(out, edge)
		}
	}
	return out
}

func findJourneyState(j policy.Journey, stateID string) *policy.JourneyNode {
	for _, state := range j.States {
		if state.ID == stateID {
			copied := state
			return &copied
		}
	}
	return nil
}

func journeyOrderedStates(j policy.Journey) []policy.JourneyNode {
	if len(j.Edges) == 0 || strings.TrimSpace(j.RootID) == "" {
		return append([]policy.JourneyNode(nil), j.States...)
	}
	index := map[string]policy.JourneyNode{}
	for _, state := range j.States {
		index[state.ID] = state
	}
	seen := map[string]struct{}{}
	var out []policy.JourneyNode
	queue := []string{strings.TrimSpace(j.RootID)}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		state, ok := index[current]
		if ok {
			out = append(out, state)
		}
		for _, edge := range outgoingEdges(j, current) {
			queue = append(queue, edge.Target)
		}
	}
	for _, state := range j.States {
		if _, ok := seen[state.ID]; ok {
			continue
		}
		out = append(out, state)
	}
	return out
}

func mergeJourneyMetadata(dst map[string]any, src map[string]any) {
	if len(src) == 0 {
		return
	}
	for key, value := range src {
		if key == "journey_node" {
			existing, _ := dst[key].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			if nested, ok := value.(map[string]any); ok {
				for nestedKey, nestedValue := range nested {
					existing[nestedKey] = nestedValue
				}
			}
			dst[key] = existing
			continue
		}
		dst[key] = value
	}
}
