package policyruntime

import (
	"fmt"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func projectJourneyNodes(bundle policy.Bundle) []ProjectedJourneyNode {
	var out []ProjectedJourneyNode
	for _, raw := range bundle.Journeys {
		j := normalizeJourneyForProjection(raw)
		indexByState := map[string]int{}
		for i, state := range journeyOrderedStates(j) {
			indexByState[state.ID] = i + 1
		}
		for _, entry := range journeyProjectionEntries(j) {
			state := entry.State
			nodeKind := projectedJourneyNodeKind(state)
			customerDependent, agentDependent := projectedJourneyDependencyFlags(state, nodeKind)
			item := ProjectedJourneyNode{
				ID:              projectedStateID(j.ID, state.ID, entry.SourceEdgeID),
				JourneyID:       j.ID,
				StateID:         state.ID,
				SourceEdgeID:    entry.SourceEdgeID,
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
			if edge := edgeByID(j, entry.SourceEdgeID); edge != nil {
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

func projectedStateID(journeyID string, stateID string, sourceEdgeID string) string {
	base := "journey_node:" + journeyID + ":" + stateID
	if strings.TrimSpace(sourceEdgeID) == "" {
		return base
	}
	return base + ":" + strings.TrimSpace(sourceEdgeID)
}

func projectFollowUps(j policy.Journey, stateID string) []string {
	out := []string{}
	for _, edge := range outgoingEdges(j, stateID) {
		if strings.TrimSpace(edge.Target) == "" {
			continue
		}
		out = append(out, projectedStateID(j.ID, edge.Target, edge.ID))
	}
	if len(out) == 0 {
		if state := findJourneyState(j, stateID); state != nil {
			for _, next := range state.Next {
				next = strings.TrimSpace(next)
				if next != "" {
					out = append(out, projectedStateID(j.ID, next, ""))
				}
			}
		}
	}
	return dedupe(out)
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

func edgeByID(j policy.Journey, edgeID string) *policy.JourneyEdge {
	edgeID = strings.TrimSpace(edgeID)
	if edgeID == "" {
		return nil
	}
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.ID) == edgeID {
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

type projectedJourneyEntry struct {
	State        policy.JourneyNode
	SourceEdgeID string
}

func journeyProjectionEntries(j policy.Journey) []projectedJourneyEntry {
	if len(j.Edges) == 0 || strings.TrimSpace(j.RootID) == "" {
		out := make([]projectedJourneyEntry, 0, len(j.States))
		for _, state := range j.States {
			out = append(out, projectedJourneyEntry{State: state})
		}
		return out
	}

	stateIndex := map[string]policy.JourneyNode{}
	for _, state := range j.States {
		stateIndex[state.ID] = state
	}

	type queuedNode struct {
		StateID      string
		SourceEdgeID string
	}

	queue := []queuedNode{}
	rootID := strings.TrimSpace(j.RootID)
	if incoming := incomingEdges(j, rootID); len(incoming) > 0 {
		for _, edge := range incoming {
			queue = append(queue, queuedNode{StateID: edge.Target, SourceEdgeID: edge.ID})
		}
	} else if _, ok := stateIndex[rootID]; ok {
		queue = append(queue, queuedNode{StateID: rootID})
	}
	for _, edge := range outgoingEdges(j, rootID) {
		queue = append(queue, queuedNode{StateID: edge.Target, SourceEdgeID: edge.ID})
	}

	seen := map[string]struct{}{}
	seenState := map[string]struct{}{}
	var out []projectedJourneyEntry

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		key := strings.TrimSpace(current.StateID) + "::" + strings.TrimSpace(current.SourceEdgeID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		state, ok := stateIndex[strings.TrimSpace(current.StateID)]
		if !ok {
			continue
		}
		seenState[state.ID] = struct{}{}
		out = append(out, projectedJourneyEntry{State: state, SourceEdgeID: current.SourceEdgeID})
		for _, edge := range outgoingEdges(j, state.ID) {
			queue = append(queue, queuedNode{StateID: edge.Target, SourceEdgeID: edge.ID})
		}
	}

	for _, state := range journeyOrderedStates(j) {
		if _, ok := seenState[state.ID]; ok {
			continue
		}
		out = append(out, projectedJourneyEntry{State: state})
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

func normalizeJourneyForProjection(j policy.Journey) policy.Journey {
	if len(j.States) > 0 && strings.TrimSpace(j.RootID) == "" {
		j.RootID = strings.TrimSpace(j.States[0].ID)
	}
	if len(j.Edges) == 0 {
		j.Edges = synthesizeJourneyEdges(j)
	} else {
		j.Edges = normalizeJourneyEdges(j)
	}
	return j
}

func normalizeJourneyEdges(j policy.Journey) []policy.JourneyEdge {
	out := make([]policy.JourneyEdge, 0, len(j.Edges))
	for i, edge := range j.Edges {
		if strings.TrimSpace(edge.ID) == "" {
			edge.ID = fmt.Sprintf("%s:%s->%s#%d", j.ID, strings.TrimSpace(edge.Source), strings.TrimSpace(edge.Target), i+1)
		}
		out = append(out, edge)
	}
	return out
}

func synthesizeJourneyEdges(j policy.Journey) []policy.JourneyEdge {
	var out []policy.JourneyEdge
	seen := map[string]struct{}{}
	rootID := strings.TrimSpace(j.RootID)
	for i, state := range j.States {
		if i == 0 && rootID != "" && strings.TrimSpace(state.ID) != rootID {
			edgeID := fmt.Sprintf("%s:root->%s", j.ID, state.ID)
			if _, ok := seen[edgeID]; !ok {
				seen[edgeID] = struct{}{}
				out = append(out, policy.JourneyEdge{
					ID:     edgeID,
					Source: rootID,
					Target: state.ID,
				})
			}
		}
		for _, next := range state.Next {
			next = strings.TrimSpace(next)
			if next == "" {
				continue
			}
			edgeID := fmt.Sprintf("%s:%s->%s", j.ID, state.ID, next)
			if _, ok := seen[edgeID]; ok {
				continue
			}
			seen[edgeID] = struct{}{}
			out = append(out, policy.JourneyEdge{
				ID:     edgeID,
				Source: state.ID,
				Target: next,
			})
		}
	}
	return out
}

func incomingEdges(j policy.Journey, stateID string) []policy.JourneyEdge {
	var out []policy.JourneyEdge
	for _, edge := range j.Edges {
		if strings.TrimSpace(edge.Target) == strings.TrimSpace(stateID) {
			out = append(out, edge)
		}
	}
	return out
}
