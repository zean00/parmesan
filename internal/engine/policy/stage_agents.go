package policyruntime

import (
	"context"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/model"
)

func resolveAgentExposure(associations []policy.GuidelineAgentAssociation, guidelines []policy.Guideline, state *policy.JourneyNode, isolation policy.CapabilityIsolation) []string {
	activeGuidelines := map[string]struct{}{}
	for _, item := range guidelines {
		activeGuidelines[item.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(agentID string) {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" || !isolation.AllowsAgent(agentID) {
			return
		}
		if _, ok := seen[agentID]; ok {
			return
		}
		seen[agentID] = struct{}{}
		out = append(out, agentID)
	}
	for _, assoc := range associations {
		if _, ok := activeGuidelines[assoc.GuidelineID]; !ok {
			continue
		}
		add(assoc.AgentID)
	}
	for _, item := range guidelines {
		for _, agentID := range item.Agents {
			add(agentID)
		}
	}
	if state != nil {
		add(state.Agent)
	}
	return out
}

func buildAgentStageResults(ctx context.Context, router *model.Router, matchCtx MatchingContext, state *matchingState) AgentDecisionStageResult {
	exposed := append([]string(nil), state.agentExposureStage.ExposedAgents...)
	selected, rationale := selectAgentCandidate(ctx, router, matchCtx, state, exposed)
	decision := AgentDecision{
		SelectedAgent: selected,
		CanRun:        strings.TrimSpace(selected) != "",
		Rationale:     rationale,
		Grounded:      len(strings.TrimSpace(matchCtx.LatestCustomerText)) > 0,
	}
	return AgentDecisionStageResult{
		Decision: decision,
		Evaluation: AgentDecisionEvaluation{
			ExposedAgents: exposed,
			SelectedAgent: selected,
			FinalAgent:    selected,
			Rationale:     rationale,
			Grounded:      decision.Grounded,
		},
	}
}

func buildCapabilityDecisionStageResult(ctx context.Context, router *model.Router, matchCtx MatchingContext, state *matchingState) CapabilityDecisionStageResult {
	toolDecision := state.toolDecisionStage.Decision
	agentDecision := state.agentDecisionStage.Decision
	decision := CapabilityDecision{}
	explicitAgent := state.activeJourneyState != nil && strings.TrimSpace(state.activeJourneyState.Agent) != ""
	explicitTool := state.activeJourneyState != nil && (strings.TrimSpace(state.activeJourneyState.Tool) != "" || (state.activeJourneyState.MCP != nil && (strings.TrimSpace(state.activeJourneyState.MCP.Tool) != "" || len(state.activeJourneyState.MCP.Tools) > 0)))

	switch {
	case explicitAgent && agentDecision.SelectedAgent != "":
		decision.Kind = "agent"
		decision.TargetID = agentDecision.SelectedAgent
		decision.Rationale = firstNonEmpty(agentDecision.Rationale, "current journey state explicitly requires an agent")
	case explicitTool && toolDecision.SelectedTool != "":
		decision.Kind = "tool"
		decision.TargetID = toolDecision.SelectedTool
		decision.Rationale = firstNonEmpty(toolDecision.Rationale, "current journey state explicitly requires a tool")
	case agentDecision.SelectedAgent == "" && toolDecision.SelectedTool == "":
	case agentDecision.SelectedAgent != "" && toolDecision.SelectedTool == "":
		decision.Kind = "agent"
		decision.TargetID = agentDecision.SelectedAgent
		decision.Rationale = agentDecision.Rationale
	case agentDecision.SelectedAgent == "" && toolDecision.SelectedTool != "":
		decision.Kind = "tool"
		decision.TargetID = toolDecision.SelectedTool
		decision.Rationale = toolDecision.Rationale
	default:
		decision.Kind = selectCapabilityKind(ctx, router, matchCtx, state, agentDecision, toolDecision)
		if decision.Kind == "agent" {
			decision.TargetID = agentDecision.SelectedAgent
			decision.Rationale = firstNonEmpty(agentDecision.Rationale, "delegated agent is the better fit for this turn")
		} else {
			decision.Kind = "tool"
			decision.TargetID = toolDecision.SelectedTool
			decision.Rationale = firstNonEmpty(toolDecision.Rationale, "direct tool execution is the better fit for this turn")
		}
	}
	return CapabilityDecisionStageResult{
		Decision: decision,
		Evaluation: CapabilityDecisionEvaluation{
			Kind:      decision.Kind,
			TargetID:  decision.TargetID,
			Rationale: decision.Rationale,
		},
	}
}

func selectAgentCandidate(ctx context.Context, router *model.Router, matchCtx MatchingContext, state *matchingState, exposed []string) (string, string) {
	if state.activeJourneyState != nil {
		if selected := strings.TrimSpace(state.activeJourneyState.Agent); selected != "" {
			for _, item := range exposed {
				if item == selected {
					return selected, "current journey state explicitly requires an agent"
				}
			}
		}
	}
	if len(exposed) == 0 {
		return "", ""
	}
	if len(exposed) == 1 {
		return exposed[0], "matched policy exposes one delegated agent"
	}
	if router != nil {
		var structured struct {
			SelectedAgent string `json:"selected_agent"`
			Rationale     string `json:"rationale"`
		}
		var sb strings.Builder
		sb.WriteString("Choose the single best delegated agent for this turn.\n")
		sb.WriteString("Customer message: " + matchCtx.LatestCustomerText + "\n")
		if state.activeJourney != nil {
			sb.WriteString("Active journey: " + state.activeJourney.ID + "\n")
		}
		if state.activeJourneyState != nil {
			sb.WriteString("Active journey state: " + state.activeJourneyState.ID + "\n")
		}
		sb.WriteString("Candidates:\n")
		for _, item := range exposed {
			sb.WriteString("- " + item + "\n")
		}
		sb.WriteString(`Return JSON: {"selected_agent":"agent_id","rationale":"why"}`)
		if generateStructuredWithRetry(ctx, router, sb.String(), &structured) {
			selected := strings.TrimSpace(structured.SelectedAgent)
			for _, item := range exposed {
				if item == selected {
					return selected, strings.TrimSpace(structured.Rationale)
				}
			}
		}
	}
	return exposed[0], "first exposed delegated agent selected by fallback"
}

func selectCapabilityKind(ctx context.Context, router *model.Router, matchCtx MatchingContext, state *matchingState, agentDecision AgentDecision, toolDecision ToolDecision) string {
	if router != nil {
		var structured struct {
			Kind      string `json:"kind"`
			Rationale string `json:"rationale"`
		}
		var sb strings.Builder
		sb.WriteString("Choose the best capability for this turn.\n")
		sb.WriteString("Customer message: " + matchCtx.LatestCustomerText + "\n")
		sb.WriteString("Tool candidate: " + toolDecision.SelectedTool + "\n")
		sb.WriteString("Tool rationale: " + toolDecision.Rationale + "\n")
		sb.WriteString("Delegated agent candidate: " + agentDecision.SelectedAgent + "\n")
		sb.WriteString("Delegated agent rationale: " + agentDecision.Rationale + "\n")
		sb.WriteString(`Return JSON: {"kind":"tool|agent","rationale":"why"}`)
		if generateStructuredWithRetry(ctx, router, sb.String(), &structured) {
			if strings.EqualFold(strings.TrimSpace(structured.Kind), "agent") {
				return "agent"
			}
			if strings.EqualFold(strings.TrimSpace(structured.Kind), "tool") {
				return "tool"
			}
		}
	}
	if !toolDecision.CanRun && agentDecision.CanRun {
		return "agent"
	}
	return "tool"
}
