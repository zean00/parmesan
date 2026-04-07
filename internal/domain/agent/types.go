package agent

import "time"

type Profile struct {
	ID                        string         `json:"id"`
	Name                      string         `json:"name"`
	Description               string         `json:"description,omitempty"`
	Status                    string         `json:"status"`
	DefaultPolicyBundleID     string         `json:"default_policy_bundle_id,omitempty"`
	DefaultKnowledgeScopeKind string         `json:"default_knowledge_scope_kind,omitempty"`
	DefaultKnowledgeScopeID   string         `json:"default_knowledge_scope_id,omitempty"`
	Metadata                  map[string]any `json:"metadata,omitempty"`
	SoulHash                  string         `json:"soul_hash,omitempty"`
	ActiveSessionCount        int            `json:"active_session_count,omitempty"`
	CreatedAt                 time.Time      `json:"created_at"`
	UpdatedAt                 time.Time      `json:"updated_at"`
}
