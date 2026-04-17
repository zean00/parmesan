package policyruntime

import (
	"reflect"
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestResolveAgentExposurePreservesPolicyOrder(t *testing.T) {
	got := resolveAgentExposure(
		[]policy.GuidelineAgentAssociation{
			{GuidelineID: "g1", AgentID: "AgentB"},
			{GuidelineID: "g1", AgentID: "AgentA"},
			{GuidelineID: "g2", AgentID: "AgentC"},
		},
		[]policy.Guideline{
			{ID: "g1", Agents: []string{"AgentA", "AgentD"}},
			{ID: "g2", Agents: []string{"AgentC"}},
		},
		&policy.JourneyNode{Agent: "AgentE"},
		policy.CapabilityIsolation{},
	)
	want := []string{"AgentB", "AgentA", "AgentC", "AgentD", "AgentE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveAgentExposure() = %#v, want %#v", got, want)
	}
}
