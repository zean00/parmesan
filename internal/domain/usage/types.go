package usage

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

const (
	ScopeCustomer     = "customer"
	ScopeAgent        = "agent"
	ScopeOrganization = "organization"

	MetricCustomerTurns       = "customer_turns"
	MetricModelRequests       = "model_requests"
	MetricInputTokens         = "input_tokens"
	MetricOutputTokens        = "output_tokens"
	MetricTotalTokens         = "total_tokens"
	MetricEstimatedCostMicros = "estimated_cost_micros"
	MetricToolCalls           = "tool_calls"

	WindowMinute = "minute"
	WindowHour   = "hour"
	WindowDay    = "day"
	WindowMonth  = "month"

	EnforcementBlock        = "block"
	EnforcementWarn         = "warn"
	EnforcementAllowOverage = "allow_overage"

	PolicyActive   = "active"
	PolicyDisabled = "disabled"

	DecisionAllowed = "allowed"
	DecisionWarned  = "warned"
	DecisionBlocked = "blocked"
)

type QuotaPolicy struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ScopeKind    string            `json:"scope_kind"`
	ScopeID      string            `json:"scope_id,omitempty"`
	Metric       string            `json:"metric"`
	Window       string            `json:"window"`
	Limit        int64             `json:"limit"`
	Enforcement  string            `json:"enforcement"`
	Status       string            `json:"status"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type PolicyQuery struct {
	ID        string
	ScopeKind string
	ScopeID   string
	Metric    string
	Status    string
	Limit     int
}

type UsageEvent struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	PolicyID     string            `json:"policy_id,omitempty"`
	Decision     string            `json:"decision,omitempty"`
	ScopeKind    string            `json:"scope_kind"`
	ScopeID      string            `json:"scope_id"`
	Metric       string            `json:"metric"`
	Quantity     int64             `json:"quantity"`
	Window       string            `json:"window,omitempty"`
	WindowStart  time.Time         `json:"window_start,omitempty"`
	WindowEnd    time.Time         `json:"window_end,omitempty"`
	UsedBefore   int64             `json:"used_before,omitempty"`
	UsedAfter    int64             `json:"used_after,omitempty"`
	Limit        int64             `json:"limit,omitempty"`
	Resource     string            `json:"resource,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	ToolID       string            `json:"tool_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	ExecutionID  string            `json:"execution_id,omitempty"`
	ResponseID   string            `json:"response_id,omitempty"`
	TraceID      string            `json:"trace_id,omitempty"`
	Estimated    bool              `json:"estimated,omitempty"`
	Status       string            `json:"status,omitempty"`
	Error        string            `json:"error,omitempty"`
	LatencyMS    int64             `json:"latency_ms,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	OccurredAt   time.Time         `json:"occurred_at"`
	RecordedAt   time.Time         `json:"recorded_at"`
}

type EventQuery struct {
	ScopeKind   string
	ScopeID     string
	Metric      string
	SessionID   string
	ExecutionID string
	Provider    string
	Status      string
	Since       time.Time
	Until       time.Time
	Limit       int
}

type Bucket struct {
	PolicyID    string    `json:"policy_id,omitempty"`
	ScopeKind   string    `json:"scope_kind"`
	ScopeID     string    `json:"scope_id"`
	Metric      string    `json:"metric"`
	Window      string    `json:"window"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Quantity    int64     `json:"quantity"`
	Limit       int64     `json:"limit,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SummaryQuery struct {
	ScopeKind string
	ScopeID   string
	Metric    string
	Window    string
	Since     time.Time
	Until     time.Time
	Limit     int
}

type Reservation struct {
	Policy     QuotaPolicy
	ScopeID    string
	Quantity   int64
	Now        time.Time
	WindowFrom time.Time
	WindowTo   time.Time
}

type Decision struct {
	Policy      QuotaPolicy `json:"policy"`
	Decision    string      `json:"decision"`
	Allowed     bool        `json:"allowed"`
	ScopeKind   string      `json:"scope_kind"`
	ScopeID     string      `json:"scope_id"`
	Metric      string      `json:"metric"`
	Window      string      `json:"window"`
	Limit       int64       `json:"limit"`
	UsedBefore  int64       `json:"used_before"`
	UsedAfter   int64       `json:"used_after"`
	Requested   int64       `json:"requested"`
	WindowStart time.Time   `json:"window_start"`
	WindowEnd   time.Time   `json:"window_end"`
	ResetAt     time.Time   `json:"reset_at"`
}
