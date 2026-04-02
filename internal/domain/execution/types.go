package execution

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
	StatusAbandoned Status = "abandoned"
)

type TurnExecution struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	TriggerEventID string    `json:"trigger_event_id"`
	PolicyBundleID string    `json:"policy_bundle_id,omitempty"`
	ProposalID     string    `json:"proposal_id,omitempty"`
	RolloutID      string    `json:"rollout_id,omitempty"`
	SelectionReason string   `json:"selection_reason,omitempty"`
	TraceID        string    `json:"trace_id,omitempty"`
	Status         Status    `json:"status"`
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ExecutionStep struct {
	ID            string    `json:"id"`
	ExecutionID   string    `json:"execution_id"`
	Name          string    `json:"name"`
	Status        Status    `json:"status"`
	Attempt       int       `json:"attempt"`
	Recomputable  bool      `json:"recomputable"`
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
	LastError     string    `json:"last_error,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
