package rollout

import (
	"testing"

	rolloutdomain "github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
)

func TestSelectBundleUsesExplicitCanarySession(t *testing.T) {
	sel := SelectBundle(
		session.Session{ID: "sess_1", Channel: "web"},
		[]rolloutdomain.Proposal{{
			ID:                "proposal_1",
			SourceBundleID:    "bundle_active",
			CandidateBundleID: "bundle_candidate",
			State:             rolloutdomain.StateCanary,
		}},
		[]rolloutdomain.Record{{
			ID:                "rollout_1",
			ProposalID:        "proposal_1",
			Status:            rolloutdomain.RolloutActive,
			Channel:           "web",
			IncludeSessionIDs: []string{"sess_1"},
		}},
		"bundle_active",
	)
	if sel.BundleID != "bundle_candidate" || sel.RolloutID != "rollout_1" || sel.Reason != "canary" {
		t.Fatalf("selection = %#v, want canary candidate bundle", sel)
	}
}

func TestSelectBundleFallsBackToActiveProposal(t *testing.T) {
	sel := SelectBundle(
		session.Session{ID: "sess_2", Channel: "web"},
		[]rolloutdomain.Proposal{{
			ID:                "proposal_active",
			SourceBundleID:    "bundle_old",
			CandidateBundleID: "bundle_new",
			State:             rolloutdomain.StateActive,
		}},
		nil,
		"bundle_old",
	)
	if sel.BundleID != "bundle_new" || sel.ProposalID != "proposal_active" || sel.Reason != "active" {
		t.Fatalf("selection = %#v, want active proposal bundle", sel)
	}
}
