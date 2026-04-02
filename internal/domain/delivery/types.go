package delivery

import "time"

type Attempt struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ExecutionID string    `json:"execution_id"`
	EventID     string    `json:"event_id"`
	Channel     string    `json:"channel"`
	Status      string    `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt   time.Time `json:"created_at"`
}
