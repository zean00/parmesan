package rollout

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type ProposalState string

const (
	StateProposed ProposalState = "proposed"
	StateReviewed ProposalState = "reviewed"
	StateShadow   ProposalState = "shadow"
	StateCanary   ProposalState = "canary"
	StateActive   ProposalState = "active"
	StateRetired  ProposalState = "retired"
)

type Proposal struct {
	ID                     string            `json:"id"`
	ArtifactMeta           artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SourceBundleID         string            `json:"source_bundle_id"`
	CandidateBundleID      string            `json:"candidate_bundle_id"`
	State                  ProposalState     `json:"state"`
	Rationale              string            `json:"rationale,omitempty"`
	EvidenceRefs           []string          `json:"evidence_refs,omitempty"`
	ReplayScore            float64           `json:"replay_score,omitempty"`
	SafetyScore            float64           `json:"safety_score,omitempty"`
	RiskFlags              []string          `json:"risk_flags,omitempty"`
	RequiresManualApproval bool              `json:"requires_manual_approval,omitempty"`
	EvalSummaryJSON        string            `json:"eval_summary_json,omitempty"`
	Origin                 string            `json:"origin,omitempty"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type RolloutStatus string

const (
	RolloutActive     RolloutStatus = "active"
	RolloutDisabled   RolloutStatus = "disabled"
	RolloutRolledBack RolloutStatus = "rolled_back"
)

type Record struct {
	ID                string            `json:"id"`
	ArtifactMeta      artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ProposalID        string            `json:"proposal_id"`
	Status            RolloutStatus     `json:"status"`
	Channel           string            `json:"channel"`
	Percentage        int               `json:"percentage"`
	IncludeSessionIDs []string          `json:"include_session_ids,omitempty"`
	PreviousBundleID  string            `json:"previous_bundle_id,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
