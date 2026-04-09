package policy

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
)

type GraphArtifact struct {
	ID           string            `json:"id"`
	BundleID     string            `json:"bundle_id"`
	Kind         string            `json:"kind"`
	Version      string            `json:"version"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	SourceYAML   string            `json:"source_yaml,omitempty"`
	Payload      map[string]any    `json:"payload,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type GraphEdge struct {
	ID           string            `json:"id"`
	BundleID     string            `json:"bundle_id"`
	SnapshotID   string            `json:"snapshot_id,omitempty"`
	SourceID     string            `json:"source_id"`
	Kind         string            `json:"kind"`
	TargetID     string            `json:"target_id"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type Snapshot struct {
	ID           string            `json:"id"`
	BundleID     string            `json:"bundle_id"`
	Version      string            `json:"version"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	Bundle       Bundle            `json:"bundle"`
	ArtifactIDs  []string          `json:"artifact_ids,omitempty"`
	Artifacts    []GraphArtifact   `json:"artifacts,omitempty"`
	Edges        []GraphEdge       `json:"edges,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type SnapshotQuery struct {
	BundleID string
	Limit    int
}

type ArtifactQuery struct {
	ID       string
	BundleID string
	Kind     string
	Limit    int
}

type EdgeQuery struct {
	ID         string
	BundleID   string
	SnapshotID string
	SourceID   string
	TargetID   string
	Kind       string
	Limit      int
}

func MaterializeGraph(bundle Bundle) ([]GraphArtifact, []GraphEdge, Snapshot) {
	now := bundle.ImportedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rootMeta := bundle.ArtifactMeta
	rootArtifact := func(id, kind string, payload map[string]any) GraphArtifact {
		meta := rootMeta
		meta.Kind = kind
		meta.Version = bundle.Version
		if meta.LineageRootID == "" {
			meta.LineageRootID = id
		}
		return GraphArtifact{
			ID:           id,
			BundleID:     bundle.ID,
			Kind:         kind,
			Version:      bundle.Version,
			ArtifactMeta: meta,
			SourceYAML:   bundle.SourceYAML,
			Payload:      payload,
			CreatedAt:    now,
		}
	}
	edge := func(sourceID, kind, targetID string, metadata map[string]any) GraphEdge {
		id := stableGraphID(bundle.ID, sourceID, kind, targetID)
		meta := rootMeta
		meta.Kind = "policy_edge"
		meta.Version = bundle.Version
		if meta.LineageRootID == "" {
			meta.LineageRootID = id
		}
		return GraphEdge{
			ID:           id,
			BundleID:     bundle.ID,
			SourceID:     sourceID,
			Kind:         kind,
			TargetID:     targetID,
			ArtifactMeta: meta,
			Metadata:     metadata,
			CreatedAt:    now,
		}
	}
	var artifacts []GraphArtifact
	var edges []GraphEdge

	artifacts = append(artifacts,
		rootArtifact(stableGraphID(bundle.ID, "soul"), "soul", map[string]any{"soul": bundle.Soul, "soul_markdown": bundle.SoulMarkdown}),
		rootArtifact(stableGraphID(bundle.ID, "semantics"), "semantic_pack", map[string]any{"semantics": bundle.Semantics}),
		rootArtifact(stableGraphID(bundle.ID, "quality_profile"), "quality_profile", map[string]any{"quality_profile": bundle.QualityProfile}),
		rootArtifact(stableGraphID(bundle.ID, "lifecycle_policy"), "lifecycle_policy", map[string]any{"lifecycle_policy": bundle.LifecyclePolicy}),
		rootArtifact(stableGraphID(bundle.ID, "capability_isolation"), "capability_isolation", map[string]any{"capability_isolation": bundle.CapabilityIsolation}),
		rootArtifact(stableGraphID(bundle.ID, "domain_boundary"), "domain_boundary", map[string]any{"domain_boundary": bundle.DomainBoundary}),
		rootArtifact(stableGraphID(bundle.ID, "perceived_performance"), "perceived_performance", map[string]any{"perceived_performance": bundle.PerceivedPerformance}),
	)
	for _, item := range bundle.Glossary {
		id := stableGraphID(bundle.ID, "glossary", item.Term)
		artifacts = append(artifacts, rootArtifact(id, "glossary_term", map[string]any{"term": item}))
	}
	for _, item := range bundle.Observations {
		artifacts = append(artifacts, rootArtifact(item.ID, "observation", map[string]any{"observation": item}))
	}
	for _, item := range bundle.Guidelines {
		artifacts = append(artifacts, rootArtifact(item.ID, "guideline", map[string]any{"guideline": item}))
	}
	for _, item := range bundle.Relationships {
		id := stableGraphID(bundle.ID, "relationship", item.Source, item.Kind, item.Target)
		artifacts = append(artifacts, rootArtifact(id, "relationship", map[string]any{"relationship": item}))
		edges = append(edges, edge(item.Source, item.Kind, item.Target, map[string]any{"relationship_artifact_id": id}))
	}
	for _, item := range bundle.Templates {
		artifacts = append(artifacts, rootArtifact(item.ID, "template", map[string]any{"template": item}))
	}
	for _, item := range bundle.ToolPolicies {
		artifacts = append(artifacts, rootArtifact(item.ID, "tool_policy", map[string]any{"tool_policy": item}))
	}
	for _, item := range bundle.Retrievers {
		artifacts = append(artifacts, rootArtifact(item.ID, "retriever", map[string]any{"retriever": item}))
	}
	for _, item := range bundle.WatchCapabilities {
		artifacts = append(artifacts, rootArtifact(item.ID, "watch_capability", map[string]any{"watch_capability": item}))
	}
	for _, item := range bundle.Journeys {
		artifacts = append(artifacts, rootArtifact(item.ID, "journey", map[string]any{"journey": item}))
		for _, state := range item.States {
			stateID := stableGraphID(item.ID, "state", state.ID)
			artifacts = append(artifacts, rootArtifact(stateID, "journey_state", map[string]any{"journey_id": item.ID, "state": state}))
			edges = append(edges, edge(item.ID, "depends_on", stateID, nil))
		}
		for _, rel := range item.Guidelines {
			edges = append(edges, edge(item.ID, "depends_on", rel.ID, nil))
		}
		for _, rel := range item.Templates {
			edges = append(edges, edge(item.ID, "depends_on", rel.ID, nil))
		}
		for _, itemEdge := range item.Edges {
			sourceID := itemEdge.Source
			if sourceID != item.RootID {
				sourceID = stableGraphID(item.ID, "state", itemEdge.Source)
			}
			targetID := stableGraphID(item.ID, "state", itemEdge.Target)
			edges = append(edges, edge(sourceID, "transition_to", targetID, map[string]any{
				"journey_id":      item.ID,
				"journey_edge_id": itemEdge.ID,
				"condition":       itemEdge.Condition,
			}))
		}
	}
	artifactIDs := make([]string, 0, len(artifacts))
	for _, item := range artifacts {
		artifactIDs = append(artifactIDs, item.ID)
	}
	snapshotID := stableGraphID(bundle.ID, bundle.Version, "snapshot")
	for i := range edges {
		edges[i].SnapshotID = snapshotID
	}
	meta := rootMeta
	meta.Kind = "policy_snapshot"
	meta.Version = bundle.Version
	if meta.LineageRootID == "" {
		meta.LineageRootID = snapshotID
	}
	snapshot := Snapshot{
		ID:           snapshotID,
		BundleID:     bundle.ID,
		Version:      bundle.Version,
		ArtifactMeta: meta,
		Bundle:       bundle,
		ArtifactIDs:  artifactIDs,
		Artifacts:    artifacts,
		Edges:        edges,
		CreatedAt:    now,
	}
	return artifacts, edges, snapshot
}

func stableGraphID(parts ...string) string {
	key := strings.Join(parts, "\x00")
	sum := sha1.Sum([]byte(key))
	return "pgraph_" + hex.EncodeToString(sum[:8])
}

func SnapshotBundle(snapshot Snapshot) Bundle {
	return snapshot.Bundle
}

func ArtifactLookup(snapshot Snapshot, kind string) []GraphArtifact {
	var out []GraphArtifact
	for _, item := range snapshot.Artifacts {
		if item.Kind == kind {
			out = append(out, item)
		}
	}
	return out
}

func SnapshotKindID(snapshot Snapshot, kind string) string {
	items := ArtifactLookup(snapshot, kind)
	if len(items) == 0 {
		return ""
	}
	return items[0].ID
}

func SnapshotRef(snapshot Snapshot) string {
	return fmt.Sprintf("%s@%s", snapshot.BundleID, snapshot.Version)
}

func SelectSnapshot(snapshots []Snapshot, preferredBundleID string, fallbackBundleID string) (Snapshot, bool) {
	if preferredBundleID != "" {
		if item, ok := LatestSnapshotForBundle(snapshots, preferredBundleID); ok {
			return item, true
		}
	}
	if fallbackBundleID != "" {
		if item, ok := LatestSnapshotForBundle(snapshots, fallbackBundleID); ok {
			return item, true
		}
	}
	if len(snapshots) == 0 {
		return Snapshot{}, false
	}
	return snapshots[0], true
}

func LatestSnapshotForBundle(snapshots []Snapshot, bundleID string) (Snapshot, bool) {
	var (
		out   Snapshot
		found bool
	)
	for _, item := range snapshots {
		if item.BundleID != bundleID {
			continue
		}
		if !found || item.CreatedAt.After(out.CreatedAt) {
			out = item
			found = true
		}
	}
	return out, found
}
