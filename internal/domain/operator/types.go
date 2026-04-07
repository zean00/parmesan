package operator

import "time"

const (
	RoleViewer           = "viewer"
	RoleOperator         = "operator"
	RoleKnowledgeManager = "knowledge_manager"
	RolePolicyManager    = "policy_manager"
	RoleAdmin            = "admin"

	StatusActive   = "active"
	StatusRevoked  = "revoked"
	StatusDisabled = "disabled"
)

type Operator struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Email       string         `json:"email,omitempty"`
	Roles       []string       `json:"roles"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type APIToken struct {
	ID         string         `json:"id"`
	OperatorID string         `json:"operator_id"`
	Name       string         `json:"name"`
	TokenHash  string         `json:"-"`
	Status     string         `json:"status"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	RevokedAt  *time.Time     `json:"revoked_at,omitempty"`
	Plaintext  string         `json:"token,omitempty"`
}
