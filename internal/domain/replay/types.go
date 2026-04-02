package replay

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

type RunType string

const (
	TypeReplay RunType = "replay"
	TypeShadow RunType = "shadow"
)

type Run struct {
	ID             string    `json:"id"`
	Type           RunType   `json:"type"`
	SourceExecutionID string  `json:"source_execution_id"`
	ProposalID     string    `json:"proposal_id,omitempty"`
	ActiveBundleID string    `json:"active_bundle_id,omitempty"`
	ShadowBundleID string    `json:"shadow_bundle_id,omitempty"`
	Status         Status    `json:"status"`
	ResultJSON     string    `json:"result_json,omitempty"`
	DiffJSON       string    `json:"diff_json,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
