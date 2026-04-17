package policyruntime

import (
	"reflect"
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestResolveAgentExposurePreservesPolicyOrder(t *testing.T) {
	got, bindings := resolveAgentExposure(
		[]policy.GuidelineAgentAssociation{
			{GuidelineID: "g1", AgentID: "AgentB", WorkflowID: "wf_ticket"},
			{GuidelineID: "g1", AgentID: "AgentA"},
			{GuidelineID: "g2", AgentID: "AgentC"},
		},
		[]policy.Guideline{
			{ID: "g1", Agents: []string{"AgentA", "AgentD"}, AgentBindings: []policy.GuidelineAgentBinding{{AgentID: "AgentF", WorkflowID: "wf_extra"}}},
			{ID: "g2", Agents: []string{"AgentC"}},
		},
		&policy.JourneyNode{Agent: "AgentE"},
		policy.CapabilityIsolation{},
	)
	want := []string{"AgentB", "AgentA", "AgentC", "AgentD", "AgentF", "AgentE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveAgentExposure() = %#v, want %#v", got, want)
	}
	if len(bindings) != 6 {
		t.Fatalf("bindings = %#v, want 6 items", bindings)
	}
	if bindings[0].AgentID != "AgentB" || bindings[0].WorkflowID != "wf_ticket" {
		t.Fatalf("first binding = %#v, want AgentB wf_ticket", bindings[0])
	}
}

func TestSelectedWorkflowForAgentDetectsAmbiguity(t *testing.T) {
	got := selectedWorkflowForAgent("AgentA", []ExposedAgentBinding{
		{AgentID: "AgentA", WorkflowID: "wf_one"},
		{AgentID: "AgentA", WorkflowID: "wf_two"},
	})
	if got != "__ambiguous__" {
		t.Fatalf("selectedWorkflowForAgent() = %q, want __ambiguous__", got)
	}
}

func TestSelectAgentCandidatePreservesWorkflowForJourneyPinnedAgent(t *testing.T) {
	state := &matchingState{
		activeJourneyState: &policy.JourneyNode{Agent: "AgentA"},
	}
	selected, workflowID, rationale := selectAgentCandidate(nil, nil, MatchingContext{}, state, []string{"AgentA"}, []ExposedAgentBinding{
		{AgentID: "AgentA", WorkflowID: "wf_ticket"},
	})
	if selected != "AgentA" {
		t.Fatalf("selected = %q, want AgentA", selected)
	}
	if workflowID != "wf_ticket" {
		t.Fatalf("workflowID = %q, want wf_ticket", workflowID)
	}
	if rationale == "" {
		t.Fatal("rationale = empty, want explicit journey-state rationale")
	}
}
