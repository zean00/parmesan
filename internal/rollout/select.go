package rollout

import (
	"hash/fnv"

	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
)

type Selection struct {
	BundleID string
	ProposalID string
	RolloutID string
	Reason string
}

func SelectBundle(sess session.Session, proposals []rollout.Proposal, records []rollout.Record, activeBundleID string) Selection {
	for _, record := range records {
		if record.Status != rollout.RolloutActive || record.Channel != "" && record.Channel != sess.Channel {
			continue
		}
		proposal, ok := findProposal(proposals, record.ProposalID)
		if !ok {
			continue
		}
		if shouldUseCanary(sess.ID, record) {
			return Selection{
				BundleID: proposal.CandidateBundleID,
				ProposalID: proposal.ID,
				RolloutID: record.ID,
				Reason: "canary",
			}
		}
	}
	for _, proposal := range proposals {
		if proposal.State == rollout.StateActive && proposal.CandidateBundleID != "" {
			return Selection{
				BundleID: proposal.CandidateBundleID,
				ProposalID: proposal.ID,
				Reason: "active",
			}
		}
	}
	return Selection{
		BundleID: activeBundleID,
		Reason: "default",
	}
}

func shouldUseCanary(sessionID string, record rollout.Record) bool {
	for _, item := range record.IncludeSessionIDs {
		if item == sessionID {
			return true
		}
	}
	if record.Percentage <= 0 {
		return false
	}
	if record.Percentage >= 100 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return int(h.Sum32()%100) < record.Percentage
}

func findProposal(items []rollout.Proposal, id string) (rollout.Proposal, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return rollout.Proposal{}, false
}
