package customer

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

const (
	PreferenceStatusActive   = "active"
	PreferenceStatusPending  = "pending"
	PreferenceStatusRejected = "rejected"
	PreferenceStatusExpired  = "expired"

	MemoryCategoryPreference     = "preference"
	MemoryCategoryFact           = "fact"
	MemoryCategoryTemporaryState = "temporary_state"
	MemoryCategorySummary        = "summary"

	MemoryStatusActive   = "active"
	MemoryStatusPending  = "pending"
	MemoryStatusRejected = "rejected"
	MemoryStatusExpired  = "expired"
	MemoryStatusBlocked  = "blocked"

	MemorySensitivityLow       = "low"
	MemorySensitivitySensitive = "sensitive"
)

type Preference struct {
	ID              string            `json:"id"`
	ArtifactMeta    artifactmeta.Meta `json:"artifact_meta,omitempty"`
	AgentID         string            `json:"agent_id"`
	CustomerID      string            `json:"customer_id"`
	Key             string            `json:"key"`
	Value           string            `json:"value"`
	Source          string            `json:"source"`
	Confidence      float64           `json:"confidence"`
	Status          string            `json:"status"`
	EvidenceRefs    []string          `json:"evidence_refs,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	LastConfirmedAt *time.Time        `json:"last_confirmed_at,omitempty"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type PreferenceEvent struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	PreferenceID string            `json:"preference_id,omitempty"`
	AgentID      string            `json:"agent_id"`
	CustomerID   string            `json:"customer_id"`
	Key          string            `json:"key,omitempty"`
	Value        string            `json:"value,omitempty"`
	Action       string            `json:"action"`
	Source       string            `json:"source"`
	Confidence   float64           `json:"confidence,omitempty"`
	EvidenceRefs []string          `json:"evidence_refs,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type PreferenceQuery struct {
	AgentID        string
	CustomerID     string
	Status         string
	Key            string
	Source         string
	MinConfidence  float64
	IncludeExpired bool
	Limit          int
}

type MemoryItem struct {
	ID              string            `json:"id"`
	ArtifactMeta    artifactmeta.Meta `json:"artifact_meta,omitempty"`
	AgentID         string            `json:"agent_id"`
	CustomerID      string            `json:"customer_id"`
	Category        string            `json:"category"`
	Key             string            `json:"key"`
	Value           string            `json:"value"`
	Source          string            `json:"source"`
	Confidence      float64           `json:"confidence"`
	Status          string            `json:"status"`
	Sensitivity     string            `json:"sensitivity,omitempty"`
	PromptSafe      bool              `json:"prompt_safe"`
	EvidenceRefs    []string          `json:"evidence_refs,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	ValidFrom       *time.Time        `json:"valid_from,omitempty"`
	ValidUntil      *time.Time        `json:"valid_until,omitempty"`
	ObservedAt      time.Time         `json:"observed_at,omitempty"`
	LastSeenAt      time.Time         `json:"last_seen_at,omitempty"`
	LastConfirmedAt *time.Time        `json:"last_confirmed_at,omitempty"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type MemoryEvent struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	MemoryID     string            `json:"memory_id,omitempty"`
	AgentID      string            `json:"agent_id"`
	CustomerID   string            `json:"customer_id"`
	Category     string            `json:"category,omitempty"`
	Key          string            `json:"key,omitempty"`
	Value        string            `json:"value,omitempty"`
	Action       string            `json:"action"`
	Source       string            `json:"source"`
	Confidence   float64           `json:"confidence,omitempty"`
	EvidenceRefs []string          `json:"evidence_refs,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type MemoryQuery struct {
	AgentID        string
	CustomerID     string
	Category       string
	Status         string
	Key            string
	Source         string
	MinConfidence  float64
	PromptSafeOnly bool
	IncludeExpired bool
	Limit          int
}
