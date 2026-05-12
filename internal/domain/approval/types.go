package approval

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

type Session struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	SessionID    string            `json:"session_id"`
	ExecutionID  string            `json:"execution_id"`
	ToolID       string            `json:"tool_id"`
	ToolName     string            `json:"tool_name,omitempty"`
	Arguments    map[string]any    `json:"arguments,omitempty"`
	Status       Status            `json:"status"`
	RequestText  string            `json:"request_text"`
	Decision     string            `json:"decision,omitempty"`
	ExpiresAt    time.Time         `json:"expires_at,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}
