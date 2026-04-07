package customer

import "time"

const (
	PreferenceStatusActive   = "active"
	PreferenceStatusPending  = "pending"
	PreferenceStatusRejected = "rejected"
	PreferenceStatusExpired  = "expired"
)

type Preference struct {
	ID              string         `json:"id"`
	AgentID         string         `json:"agent_id"`
	CustomerID      string         `json:"customer_id"`
	Key             string         `json:"key"`
	Value           string         `json:"value"`
	Source          string         `json:"source"`
	Confidence      float64        `json:"confidence"`
	Status          string         `json:"status"`
	EvidenceRefs    []string       `json:"evidence_refs,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	LastConfirmedAt *time.Time     `json:"last_confirmed_at,omitempty"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type PreferenceEvent struct {
	ID           string         `json:"id"`
	PreferenceID string         `json:"preference_id,omitempty"`
	AgentID      string         `json:"agent_id"`
	CustomerID   string         `json:"customer_id"`
	Key          string         `json:"key,omitempty"`
	Value        string         `json:"value,omitempty"`
	Action       string         `json:"action"`
	Source       string         `json:"source"`
	Confidence   float64        `json:"confidence,omitempty"`
	EvidenceRefs []string       `json:"evidence_refs,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
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
