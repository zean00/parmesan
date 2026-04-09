package delivery

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Attempt struct {
	ID             string            `json:"id"`
	ArtifactMeta   artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SessionID      string            `json:"session_id"`
	ExecutionID    string            `json:"execution_id"`
	EventID        string            `json:"event_id"`
	Channel        string            `json:"channel"`
	Status         string            `json:"status"`
	IdempotencyKey string            `json:"idempotency_key"`
	CreatedAt      time.Time         `json:"created_at"`
}
