package policy

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type ChangeRequest struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	GroupID      string            `json:"group_id"`
	Domain       string            `json:"domain"`
	Action       string            `json:"action"`
	Status       string            `json:"status"`
	RequestedBy  string            `json:"requested_by,omitempty"`
	TargetIDs    []string          `json:"target_ids,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at,omitempty"`
}

type ChangeDecision struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	GroupID      string            `json:"group_id"`
	ChangeID     string            `json:"change_id"`
	Domain       string            `json:"domain"`
	Decision     string            `json:"decision"`
	ActorID      string            `json:"actor_id,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type ChangeApplication struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	GroupID      string            `json:"group_id"`
	ChangeID     string            `json:"change_id"`
	Domain       string            `json:"domain"`
	Status       string            `json:"status"`
	AppliedBy    string            `json:"applied_by,omitempty"`
	ResultIDs    []string          `json:"result_ids,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}
