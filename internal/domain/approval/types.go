package approval

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

type Session struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ExecutionID string    `json:"execution_id"`
	ToolID      string    `json:"tool_id"`
	Status      Status    `json:"status"`
	RequestText string    `json:"request_text"`
	Decision    string    `json:"decision,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
