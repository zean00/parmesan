package controlgraph

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func ChangeRequest(item policy.ChangeRequest) ([]policy.GraphArtifact, []policy.GraphEdge) {
	meta := item.ArtifactMeta
	meta.Kind = "change_request"
	meta.Version = versionOrTimestamp(meta, item.UpdatedAt, item.CreatedAt)
	if meta.LineageRootID == "" {
		meta.LineageRootID = item.ID
	}
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, item.GroupID, "change_request", meta.Version, map[string]any{"change_request": item}, meta, item.CreatedAt),
	}
	var edges []policy.GraphEdge
	for _, targetID := range item.TargetIDs {
		targetID = strings.TrimSpace(targetID)
		if targetID == "" {
			continue
		}
		edges = append(edges, edge(item.GroupID, item.ID, "proposes", targetID, nil, meta, item.CreatedAt))
	}
	return artifacts, edges
}

func ChangeDecision(item policy.ChangeDecision) ([]policy.GraphArtifact, []policy.GraphEdge) {
	meta := item.ArtifactMeta
	meta.Kind = "change_decision"
	meta.Version = versionOrTimestamp(meta, item.CreatedAt)
	if meta.LineageRootID == "" {
		meta.LineageRootID = item.ChangeID
	}
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, item.GroupID, "change_decision", meta.Version, map[string]any{"change_decision": item}, meta, item.CreatedAt),
	}
	edgeKind := "reviews"
	switch strings.ToLower(strings.TrimSpace(item.Decision)) {
	case "approved", "accepted", "confirmed":
		edgeKind = "approves"
	case "rejected":
		edgeKind = "rejects"
	case "active", "shadow", "canary", "reviewed", "promoted":
		edgeKind = "promotes"
	}
	edges := []policy.GraphEdge{
		edge(item.GroupID, item.ID, edgeKind, item.ChangeID, nil, meta, item.CreatedAt),
	}
	return artifacts, edges
}

func ChangeApplication(item policy.ChangeApplication) ([]policy.GraphArtifact, []policy.GraphEdge) {
	meta := item.ArtifactMeta
	meta.Kind = "change_application"
	meta.Version = versionOrTimestamp(meta, item.CreatedAt)
	if meta.LineageRootID == "" {
		meta.LineageRootID = item.ChangeID
	}
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, item.GroupID, "change_application", meta.Version, map[string]any{"change_application": item}, meta, item.CreatedAt),
	}
	edges := []policy.GraphEdge{
		edge(item.GroupID, item.ID, "applies", item.ChangeID, nil, meta, item.CreatedAt),
	}
	for _, resultID := range item.ResultIDs {
		resultID = strings.TrimSpace(resultID)
		if resultID == "" {
			continue
		}
		edges = append(edges, edge(item.GroupID, item.ID, "produces", resultID, nil, meta, item.CreatedAt))
	}
	return artifacts, edges
}

func ChangeArtifacts(request policy.ChangeRequest, decisions []policy.ChangeDecision, applications []policy.ChangeApplication) ([]policy.GraphArtifact, []policy.GraphEdge) {
	var artifacts []policy.GraphArtifact
	var edges []policy.GraphEdge
	items, itemEdges := ChangeRequest(request)
	artifacts = append(artifacts, items...)
	edges = append(edges, itemEdges...)
	for _, decision := range decisions {
		items, itemEdges = ChangeDecision(decision)
		artifacts = append(artifacts, items...)
		edges = append(edges, itemEdges...)
	}
	for _, application := range applications {
		items, itemEdges = ChangeApplication(application)
		artifacts = append(artifacts, items...)
		edges = append(edges, itemEdges...)
	}
	return artifacts, edges
}
