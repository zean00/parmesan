package toolrun

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Run struct {
	ID             string            `json:"id"`
	ArtifactMeta   artifactmeta.Meta `json:"artifact_meta,omitempty"`
	ExecutionID    string            `json:"execution_id"`
	ToolID         string            `json:"tool_id"`
	Status         string            `json:"status"`
	IdempotencyKey string            `json:"idempotency_key"`
	InputJSON      string            `json:"input_json,omitempty"`
	OutputJSON     string            `json:"output_json,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
}
