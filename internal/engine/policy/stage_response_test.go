package policyruntime

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestResolveResponseCapabilityPrefersJourneyState(t *testing.T) {
	id, source, candidates := resolveResponseCapability(&policy.JourneyNode{ID: "state_1", ResponseCapabilityID: "journey_cap"}, []policy.Guideline{
		{ID: "g1", ResponseCapabilityID: "guideline_cap"},
	})
	if id != "journey_cap" || source != "journey_state" {
		t.Fatalf("resolveResponseCapability() = (%q, %q), want journey state capability", id, source)
	}
	if len(candidates) != 1 || candidates[0] != "journey_cap" {
		t.Fatalf("candidates = %#v, want [journey_cap]", candidates)
	}
}

func TestResolveResponseCapabilityUsesFirstDistinctGuideline(t *testing.T) {
	id, source, candidates := resolveResponseCapability(nil, []policy.Guideline{
		{ID: "g1", ResponseCapabilityID: "cap_a"},
		{ID: "g2", ResponseCapabilityID: "cap_a"},
		{ID: "g3", ResponseCapabilityID: "cap_b"},
	})
	if id != "cap_a" || source != "guideline" {
		t.Fatalf("resolveResponseCapability() = (%q, %q), want first guideline capability", id, source)
	}
	if len(candidates) != 2 || candidates[0] != "cap_a" || candidates[1] != "cap_b" {
		t.Fatalf("candidates = %#v, want [cap_a cap_b]", candidates)
	}
}

func TestResolveStyleProfilePrefersJourneyState(t *testing.T) {
	id, source, candidates := resolveStyleProfile(policy.Bundle{
		Soul: policy.Soul{StyleProfileID: "soul_style"},
	}, &policy.JourneyNode{ID: "state_1", StyleProfileID: "journey_style"}, []policy.Guideline{
		{ID: "g1", StyleProfileID: "guideline_style"},
	})
	if id != "journey_style" || source != "journey_state" {
		t.Fatalf("resolveStyleProfile() = (%q, %q), want journey state style", id, source)
	}
	if len(candidates) != 1 || candidates[0] != "journey_style" {
		t.Fatalf("candidates = %#v, want [journey_style]", candidates)
	}
}

func TestResolveStyleProfileFallsBackToSoul(t *testing.T) {
	id, source, candidates := resolveStyleProfile(policy.Bundle{
		Soul: policy.Soul{StyleProfileID: "soul_style"},
	}, nil, nil)
	if id != "soul_style" || source != "soul" {
		t.Fatalf("resolveStyleProfile() = (%q, %q), want soul style", id, source)
	}
	if len(candidates) != 1 || candidates[0] != "soul_style" {
		t.Fatalf("candidates = %#v, want [soul_style]", candidates)
	}
}
