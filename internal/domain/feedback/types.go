package feedback

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Record struct {
	ID             string            `json:"id"`
	ArtifactMeta   artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SessionID      string            `json:"session_id"`
	ExecutionID    string            `json:"execution_id,omitempty"`
	TraceID        string            `json:"trace_id,omitempty"`
	OperatorID     string            `json:"operator_id,omitempty"`
	Rating         int               `json:"rating,omitempty"`
	Category       string            `json:"category,omitempty"`
	Text           string            `json:"text"`
	Labels         []string          `json:"labels,omitempty"`
	TargetEventIDs []string          `json:"target_event_ids,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	Outputs        Outputs           `json:"outputs,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type Outputs struct {
	PreferenceIDs        []string `json:"preference_ids,omitempty"`
	PreferenceEventIDs   []string `json:"preference_event_ids,omitempty"`
	KnowledgeProposalIDs []string `json:"knowledge_proposal_ids,omitempty"`
	PolicyProposalIDs    []string `json:"policy_proposal_ids,omitempty"`
	Unclassified         []string `json:"unclassified,omitempty"`
}

type Query struct {
	SessionID  string
	OperatorID string
	Category   string
	Limit      int
}
