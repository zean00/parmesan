package media

import (
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type Asset struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SessionID    string            `json:"session_id"`
	EventID      string            `json:"event_id"`
	PartIndex    int               `json:"part_index"`
	Type         string            `json:"type"`
	URL          string            `json:"url,omitempty"`
	MimeType     string            `json:"mime_type,omitempty"`
	Checksum     string            `json:"checksum,omitempty"`
	Status       string            `json:"status"`
	Retention    string            `json:"retention,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	EnrichedAt   time.Time         `json:"enriched_at,omitempty"`
}

type DerivedSignal struct {
	ID           string            `json:"id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	AssetID      string            `json:"asset_id"`
	SessionID    string            `json:"session_id"`
	EventID      string            `json:"event_id"`
	Kind         string            `json:"kind"`
	Value        string            `json:"value,omitempty"`
	Confidence   float64           `json:"confidence,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	Extractor    string            `json:"extractor,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}
