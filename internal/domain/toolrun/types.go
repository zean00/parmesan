package toolrun

import "time"

type Run struct {
	ID             string    `json:"id"`
	ExecutionID    string    `json:"execution_id"`
	ToolID         string    `json:"tool_id"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	InputJSON      string    `json:"input_json,omitempty"`
	OutputJSON     string    `json:"output_json,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
