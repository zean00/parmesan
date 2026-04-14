package execution

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
	StatusAbandoned Status = "abandoned"
)

const (
	DefaultStepMaxAttempts    = 5
	DefaultStepBackoffSeconds = 1
)

const (
	BlockedReasonApprovalRequired     = "approval_required"
	BlockedReasonRetryBudgetExhausted = "retry_budget_exhausted"
)

const ResumeSignalApproval = "approval"

type RetryPolicy struct {
	MaxAttempts       int  `json:"max_attempts,omitempty"`
	BackoffSeconds    int  `json:"backoff_seconds,omitempty"`
	MaxElapsedSeconds int  `json:"max_elapsed_seconds,omitempty"`
	RetryUntilValid   bool `json:"retry_until_valid,omitempty"`
}

type ModelOverride struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func (o ModelOverride) IsZero() bool {
	return o.Provider == "" && o.Model == ""
}

type RetryModelOverride struct {
	Reasoning  ModelOverride `json:"reasoning,omitempty"`
	Structured ModelOverride `json:"structured,omitempty"`
}

func (o RetryModelOverride) IsZero() bool {
	return o.Reasoning.IsZero() && o.Structured.IsZero()
}

type TurnExecution struct {
	ID                  string             `json:"id"`
	ArtifactMeta        artifactmeta.Meta  `json:"artifact_meta,omitempty"`
	SessionID           string             `json:"session_id"`
	TriggerEventID      string             `json:"trigger_event_id"`
	TriggerEventIDs     []string           `json:"trigger_event_ids,omitempty"`
	PolicyBundleID      string             `json:"policy_bundle_id,omitempty"`
	PolicySnapshotID    string             `json:"policy_snapshot_id,omitempty"`
	ProposalID          string             `json:"proposal_id,omitempty"`
	RolloutID           string             `json:"rollout_id,omitempty"`
	SelectionReason     string             `json:"selection_reason,omitempty"`
	TraceID             string             `json:"trace_id,omitempty"`
	Status              Status             `json:"status"`
	LeaseOwner          string             `json:"lease_owner,omitempty"`
	LeaseExpiresAt      time.Time          `json:"lease_expires_at,omitempty"`
	BlockedReason       string             `json:"blocked_reason,omitempty"`
	ResumeSignal        string             `json:"resume_signal,omitempty"`
	RetryModelProfileID string             `json:"retry_model_profile_id,omitempty"`
	RetryModelOverride  RetryModelOverride `json:"retry_model_override,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type ExecutionStep struct {
	ID                string            `json:"id"`
	ArtifactMeta      artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ExecutionID       string            `json:"execution_id"`
	Name              string            `json:"name"`
	Status            Status            `json:"status"`
	Attempt           int               `json:"attempt"`
	Recomputable      bool              `json:"recomputable"`
	LeaseOwner        string            `json:"lease_owner,omitempty"`
	LeaseExpiresAt    time.Time         `json:"lease_expires_at,omitempty"`
	IdempotencyKey    string            `json:"idempotency_key"`
	LastError         string            `json:"last_error,omitempty"`
	NextAttemptAt     time.Time         `json:"next_attempt_at,omitempty"`
	MaxAttempts       int               `json:"max_attempts,omitempty"`
	MaxElapsedSeconds int               `json:"max_elapsed_seconds,omitempty"`
	BackoffSeconds    int               `json:"backoff_seconds,omitempty"`
	RetryReason       string            `json:"retry_reason,omitempty"`
	BlockedReason     string            `json:"blocked_reason,omitempty"`
	ResumeSignal      string            `json:"resume_signal,omitempty"`
	StartedAt         time.Time         `json:"started_at,omitempty"`
	FinishedAt        time.Time         `json:"finished_at,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func DefaultRetryPolicy(recomputable bool) RetryPolicy {
	if !recomputable {
		return RetryPolicy{MaxAttempts: 1, BackoffSeconds: DefaultStepBackoffSeconds}
	}
	return RetryPolicy{
		MaxAttempts:     DefaultStepMaxAttempts,
		BackoffSeconds:  DefaultStepBackoffSeconds,
		RetryUntilValid: true,
	}
}
