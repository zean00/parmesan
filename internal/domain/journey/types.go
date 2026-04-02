package journey

import "time"

type InstanceStatus string

const (
	StatusActive    InstanceStatus = "active"
	StatusCompleted InstanceStatus = "completed"
	StatusPaused    InstanceStatus = "paused"
)

type Instance struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	JourneyID string         `json:"journey_id"`
	StateID   string         `json:"state_id"`
	Path      []string       `json:"path,omitempty"`
	Status    InstanceStatus `json:"status"`
	UpdatedAt time.Time      `json:"updated_at"`
}
