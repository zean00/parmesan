package controlgraph

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/artifactmeta"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/rollout"
)

func KnowledgeSource(source knowledge.Source) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := knowledgeGroupID(source.ScopeKind, source.ScopeID)
	return []policy.GraphArtifact{artifact(source.ID, groupID, "knowledge_source", versionOrTimestamp(source.ArtifactMeta, source.UpdatedAt, source.CreatedAt), map[string]any{"source": source}, source.ArtifactMeta, source.CreatedAt)}, nil
}

func KnowledgeSyncJob(job knowledge.SyncJob) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := "knowledge_job:" + job.SourceID
	artifacts := []policy.GraphArtifact{
		artifact(job.ID, groupID, "knowledge_sync_job", versionOrTimestamp(job.ArtifactMeta, derefTime(job.FinishedAt), derefTime(job.StartedAt), job.CreatedAt), map[string]any{"sync_job": job}, job.ArtifactMeta, job.CreatedAt),
	}
	var edges []policy.GraphEdge
	edges = append(edges, edge(groupID, job.SourceID, "sync_requested", job.ID, nil, job.ArtifactMeta, job.CreatedAt))
	if job.SnapshotID != "" {
		edges = append(edges,
			edge(groupID, job.ID, "produced", job.SnapshotID, nil, job.ArtifactMeta, job.CreatedAt),
			edge(groupID, job.SourceID, "derived_from", job.SnapshotID, map[string]any{"sync_job_id": job.ID}, job.ArtifactMeta, job.CreatedAt),
		)
	}
	return artifacts, edges
}

func KnowledgeSnapshot(snapshot knowledge.Snapshot) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := knowledgeGroupID(snapshot.ScopeKind, snapshot.ScopeID)
	artifacts := []policy.GraphArtifact{
		artifact(snapshot.ID, groupID, "knowledge_snapshot", versionOrTimestamp(snapshot.ArtifactMeta, snapshot.CreatedAt), map[string]any{"snapshot": snapshot}, snapshot.ArtifactMeta, snapshot.CreatedAt),
	}
	var edges []policy.GraphEdge
	if proposalID := strings.TrimSpace(fmt.Sprint(snapshot.Metadata["proposal_id"])); proposalID != "" {
		edges = append(edges, edge(groupID, proposalID, "applied_to_snapshot", snapshot.ID, map[string]any{"source": snapshot.Metadata["source"]}, snapshot.ArtifactMeta, snapshot.CreatedAt))
	}
	return artifacts, edges
}

func KnowledgeUpdateProposal(item knowledge.UpdateProposal) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := knowledgeGroupID(item.ScopeKind, item.ScopeID)
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, groupID, "knowledge_update_proposal", versionOrTimestamp(item.ArtifactMeta, item.UpdatedAt, item.CreatedAt), map[string]any{"proposal": item}, item.ArtifactMeta, item.CreatedAt),
	}
	var edges []policy.GraphEdge
	if feedbackID := strings.TrimSpace(fmt.Sprint(item.Payload["feedback_id"])); feedbackID != "" {
		edges = append(edges, edge(groupID, feedbackID, "derived_from", item.ID, map[string]any{"output_kind": "knowledge_update_proposal"}, item.ArtifactMeta, item.CreatedAt))
	}
	return artifacts, edges
}

func KnowledgeLintFinding(item knowledge.LintFinding) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := knowledgeGroupID(item.ScopeKind, item.ScopeID)
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, groupID, "knowledge_lint_finding", versionOrTimestamp(item.ArtifactMeta, item.UpdatedAt, item.CreatedAt), map[string]any{"finding": item}, item.ArtifactMeta, item.CreatedAt),
	}
	var edges []policy.GraphEdge
	if item.ProposalID != "" {
		edges = append(edges, edge(groupID, item.ProposalID, "flagged_by", item.ID, nil, item.ArtifactMeta, item.CreatedAt))
	}
	if item.PageID != "" {
		edges = append(edges, edge(groupID, item.PageID, "flagged_by", item.ID, nil, item.ArtifactMeta, item.CreatedAt))
	}
	if item.SourceID != "" {
		edges = append(edges, edge(groupID, item.SourceID, "flagged_by", item.ID, nil, item.ArtifactMeta, item.CreatedAt))
	}
	return artifacts, edges
}

func CustomerPreference(pref customer.Preference, event customer.PreferenceEvent) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := customerGroupID(pref.AgentID, pref.CustomerID)
	artifacts := []policy.GraphArtifact{
		artifact(pref.ID, groupID, "customer_preference", versionOrTimestamp(pref.ArtifactMeta, pref.UpdatedAt, pref.CreatedAt), map[string]any{"preference": pref}, pref.ArtifactMeta, pref.CreatedAt),
	}
	var edges []policy.GraphEdge
	if event.ID != "" {
		artifacts = append(artifacts, artifact(event.ID, groupID, "customer_preference_event", versionOrTimestamp(event.ArtifactMeta, event.CreatedAt), map[string]any{"preference_event": event}, event.ArtifactMeta, event.CreatedAt))
		edges = append(edges, edge(groupID, event.ID, "applies_to", pref.ID, nil, event.ArtifactMeta, event.CreatedAt))
	}
	return artifacts, edges
}

func CustomerPreferenceRecord(pref customer.Preference) ([]policy.GraphArtifact, []policy.GraphEdge) {
	return CustomerPreference(pref, customer.PreferenceEvent{})
}

func CustomerPreferenceEvent(event customer.PreferenceEvent) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := customerGroupID(event.AgentID, event.CustomerID)
	artifacts := []policy.GraphArtifact{
		artifact(event.ID, groupID, "customer_preference_event", versionOrTimestamp(event.ArtifactMeta, event.CreatedAt), map[string]any{"preference_event": event}, event.ArtifactMeta, event.CreatedAt),
	}
	var edges []policy.GraphEdge
	if event.PreferenceID != "" {
		edges = append(edges, edge(groupID, event.ID, "applies_to", event.PreferenceID, nil, event.ArtifactMeta, event.CreatedAt))
	}
	return artifacts, edges
}

func FeedbackRecord(record feedback.Record) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := feedbackGroupID(record.SessionID)
	artifacts := []policy.GraphArtifact{
		artifact(record.ID, groupID, "operator_feedback", versionOrTimestamp(record.ArtifactMeta, record.UpdatedAt, record.CreatedAt), map[string]any{"feedback": record}, record.ArtifactMeta, record.CreatedAt),
	}
	var edges []policy.GraphEdge
	for _, targetID := range record.Outputs.PreferenceIDs {
		edges = append(edges, edge(groupID, record.ID, "derived_from", targetID, map[string]any{"output_kind": "customer_preference"}, record.ArtifactMeta, record.CreatedAt))
	}
	for _, targetID := range record.Outputs.PreferenceEventIDs {
		edges = append(edges, edge(groupID, record.ID, "produced", targetID, map[string]any{"output_kind": "customer_preference_event"}, record.ArtifactMeta, record.CreatedAt))
	}
	for _, targetID := range record.Outputs.KnowledgeProposalIDs {
		edges = append(edges, edge(groupID, record.ID, "derived_from", targetID, map[string]any{"output_kind": "knowledge_update_proposal"}, record.ArtifactMeta, record.CreatedAt))
	}
	for _, targetID := range record.Outputs.PolicyProposalIDs {
		edges = append(edges, edge(groupID, record.ID, "derived_from", targetID, map[string]any{"output_kind": "rollout_proposal"}, record.ArtifactMeta, record.CreatedAt))
	}
	if fixture, ok := regressionFixtureCandidate(record); ok {
		artifacts = append(artifacts, artifact(fixture.ID, groupID, "regression_fixture", versionOrTimestamp(fixture.ArtifactMeta, fixture.CreatedAt), map[string]any{"fixture": fixture}, fixture.ArtifactMeta, fixture.CreatedAt))
		edges = append(edges, edge(groupID, record.ID, "produced", fixture.ID, map[string]any{"output_kind": "regression_fixture"}, fixture.ArtifactMeta, fixture.CreatedAt))
	}
	return artifacts, edges
}

type regressionFixtureArtifact struct {
	ID           string            `json:"id"`
	FeedbackID   string            `json:"feedback_id"`
	ScenarioID   string            `json:"scenario_id"`
	ReviewStatus string            `json:"review_status"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	ArtifactMeta artifactmeta.Meta `json:"artifact_meta,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

func regressionFixtureCandidate(record feedback.Record) (regressionFixtureArtifact, bool) {
	if record.Metadata == nil {
		return regressionFixtureArtifact{}, false
	}
	raw, ok := record.Metadata["regression_fixture_candidate"].(map[string]any)
	if !ok {
		return regressionFixtureArtifact{}, false
	}
	meta := record.ArtifactMeta
	meta.Kind = "regression_fixture"
	if meta.Version == "" {
		meta.Version = versionOrTimestamp(meta, record.UpdatedAt, record.CreatedAt)
	}
	id := stableGraphID(feedbackGroupID(record.SessionID), "regression_fixture", record.ID)
	if meta.LineageRootID == "" {
		meta.LineageRootID = id
	}
	status := strings.TrimSpace(fmt.Sprint(raw["review_status"]))
	if status == "" {
		status = "candidate"
	}
	return regressionFixtureArtifact{
		ID:           id,
		FeedbackID:   record.ID,
		ScenarioID:   strings.TrimSpace(fmt.Sprint(raw["scenario_id"])),
		ReviewStatus: status,
		Metadata:     raw,
		ArtifactMeta: meta,
		CreatedAt:    record.CreatedAt,
	}, true
}

func RolloutProposal(item rollout.Proposal) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := rolloutGroupID(item.SourceBundleID)
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, groupID, "rollout_proposal", versionOrTimestamp(item.ArtifactMeta, item.UpdatedAt, item.CreatedAt), map[string]any{"proposal": item}, item.ArtifactMeta, item.CreatedAt),
		bundleRefArtifact(groupID, item.SourceBundleID, item.ArtifactMeta, item.CreatedAt),
		bundleRefArtifact(groupID, item.CandidateBundleID, item.ArtifactMeta, item.CreatedAt),
	}
	edges := []policy.GraphEdge{
		edge(groupID, bundleRefID(groupID, item.SourceBundleID), "derived_from", item.ID, nil, item.ArtifactMeta, item.CreatedAt),
		edge(groupID, item.ID, "refines", bundleRefID(groupID, item.CandidateBundleID), nil, item.ArtifactMeta, item.CreatedAt),
	}
	return artifacts, edges
}

func RolloutRecord(item rollout.Record) ([]policy.GraphArtifact, []policy.GraphEdge) {
	groupID := "rollout:" + item.ProposalID
	artifacts := []policy.GraphArtifact{
		artifact(item.ID, groupID, "rollout_record", versionOrTimestamp(item.ArtifactMeta, item.UpdatedAt, item.CreatedAt), map[string]any{"rollout": item}, item.ArtifactMeta, item.CreatedAt),
	}
	edges := []policy.GraphEdge{
		edge(groupID, item.ID, "derived_from", item.ProposalID, nil, item.ArtifactMeta, item.CreatedAt),
	}
	if item.PreviousBundleID != "" {
		artifacts = append(artifacts, bundleRefArtifact(groupID, item.PreviousBundleID, item.ArtifactMeta, item.CreatedAt))
		edges = append(edges, edge(groupID, item.ID, "supersedes", bundleRefID(groupID, item.PreviousBundleID), nil, item.ArtifactMeta, item.CreatedAt))
	}
	return artifacts, edges
}

func knowledgeGroupID(scopeKind, scopeID string) string {
	return "knowledge:" + scopeKind + ":" + scopeID
}

func customerGroupID(agentID, customerID string) string {
	return "customer:" + agentID + ":" + customerID
}

func feedbackGroupID(sessionID string) string {
	return "feedback:" + sessionID
}

func rolloutGroupID(sourceBundleID string) string {
	return "rollout:" + sourceBundleID
}

func bundleRefArtifact(groupID, bundleID string, meta artifactmeta.Meta, createdAt time.Time) policy.GraphArtifact {
	return artifact(bundleRefID(groupID, bundleID), groupID, "policy_bundle_ref", bundleID, map[string]any{"bundle_id": bundleID}, meta, createdAt)
}

func bundleRefID(groupID, bundleID string) string {
	return stableGraphID(groupID, "policy_bundle_ref", bundleID)
}

func artifact(id, groupID, kind, version string, payload map[string]any, meta artifactmeta.Meta, createdAt time.Time) policy.GraphArtifact {
	meta.Kind = kind
	meta.Version = version
	if meta.LineageRootID == "" {
		meta.LineageRootID = id
	}
	return policy.GraphArtifact{
		ID:           id,
		BundleID:     groupID,
		Kind:         kind,
		Version:      version,
		ArtifactMeta: meta,
		Payload:      payload,
		CreatedAt:    createdAt.UTC(),
	}
}

func edge(groupID, sourceID, kind, targetID string, metadata map[string]any, meta artifactmeta.Meta, createdAt time.Time) policy.GraphEdge {
	id := stableGraphID(groupID, sourceID, kind, targetID, mustJSON(metadata))
	meta.Kind = "control_graph_edge"
	if meta.Version == "" {
		meta.Version = createdAt.UTC().Format(time.RFC3339Nano)
	}
	if meta.LineageRootID == "" {
		meta.LineageRootID = id
	}
	return policy.GraphEdge{
		ID:           id,
		BundleID:     groupID,
		SourceID:     sourceID,
		Kind:         kind,
		TargetID:     targetID,
		ArtifactMeta: meta,
		Metadata:     metadata,
		CreatedAt:    createdAt.UTC(),
	}
}

func versionOrTimestamp(meta artifactmeta.Meta, times ...time.Time) string {
	if meta.Version != "" {
		return meta.Version
	}
	for _, t := range times {
		if !t.IsZero() {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return "v1"
}

func derefTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

func stableGraphID(parts ...string) string {
	key := strings.Join(parts, "\x00")
	sum := sha1.Sum([]byte(key))
	return "pgraph_" + hex.EncodeToString(sum[:8])
}

func mustJSON(v any) string {
	if v == nil {
		return ""
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}
