package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/controlgraph"
	"github.com/sahal/parmesan/internal/domain/artifactmeta"
	"github.com/sahal/parmesan/internal/domain/policy"
)

func (s *Server) operatorListChanges(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 100
	}
	items, err := s.listControlChangeArtifacts(r.Context(), strings.TrimSpace(r.URL.Query().Get("group_id")), strings.TrimSpace(r.URL.Query().Get("status")), strings.TrimSpace(r.URL.Query().Get("kind")), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetChange(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetPolicyArtifact(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !isControlChangeKind(item.Kind) {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.changeArtifactView(item))
}

func (s *Server) operatorGetChangeLineage(w http.ResponseWriter, r *http.Request) {
	payload, err := s.graphLineagePayload(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) operatorGetPendingControlState(w http.ResponseWriter, r *http.Request) {
	payload, err := s.operatorControlStateFilteredChanges(r.Context(), r.URL.Query(), "pending")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) operatorGetAppliedControlState(w http.ResponseWriter, r *http.Request) {
	payload, err := s.operatorControlStateFilteredChanges(r.Context(), r.URL.Query(), "applied")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) operatorControlStateFilteredChanges(ctx context.Context, query map[string][]string, mode string) (map[string]any, error) {
	payload, err := s.operatorControlStatePayload(ctx, query, false)
	if err != nil {
		return nil, err
	}
	controlGroups := map[string]string{}
	if raw, ok := payload["control_groups"].(map[string]string); ok {
		controlGroups = raw
	} else if raw, ok := payload["control_groups"].(map[string]any); ok {
		for key, value := range raw {
			controlGroups[key] = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	groupIDs := orderedControlGroups(controlGroups)
	limit, _ := strconv.Atoi(strings.TrimSpace(firstQueryValue(query, "limit")))
	if limit <= 0 {
		limit = 50
	}
	items, err := s.listControlChangeArtifacts(ctx, "", mode, "", limit*4)
	if err != nil {
		return nil, err
	}
	allowed := map[string]struct{}{}
	for _, groupID := range groupIDs {
		allowed[groupID] = struct{}{}
	}
	var changes []map[string]any
	for _, item := range items {
		groupID := strings.TrimSpace(fmt.Sprint(item["group_id"]))
		if len(allowed) > 0 {
			if _, ok := allowed[groupID]; !ok {
				continue
			}
		}
		changes = append(changes, item)
		if len(changes) >= limit {
			break
		}
	}
	return map[string]any{
		"scope":          payload["scope"],
		"control_groups": controlGroups,
		"changes":        changes,
	}, nil
}

func (s *Server) listControlChangeArtifacts(ctx context.Context, groupID string, status string, kind string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	var kinds []string
	if strings.TrimSpace(kind) != "" {
		kinds = []string{strings.TrimSpace(kind)}
	} else {
		kinds = []string{"change_request", "change_decision", "change_application"}
	}
	var artifacts []policy.GraphArtifact
	for _, itemKind := range kinds {
		items, err := s.store.ListPolicyArtifacts(ctx, policy.ArtifactQuery{
			BundleID: strings.TrimSpace(groupID),
			Kind:     itemKind,
			Limit:    limit * 4,
		})
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, items...)
	}
	slices.SortFunc(artifacts, func(a, b policy.GraphArtifact) int {
		switch {
		case a.CreatedAt.After(b.CreatedAt):
			return -1
		case a.CreatedAt.Before(b.CreatedAt):
			return 1
		default:
			return strings.Compare(a.ID, b.ID)
		}
	})
	var out []map[string]any
	for _, artifact := range artifacts {
		view := s.changeArtifactView(artifact)
		if status != "" && !matchesControlChangeStatus(view, status) {
			continue
		}
		out = append(out, view)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func isControlChangeKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "change_request", "change_decision", "change_application":
		return true
	default:
		return false
	}
}

func matchesControlChangeStatus(view map[string]any, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(view["status"])))
	switch filter {
	case "", "all":
		return true
	case "pending":
		return status == "pending" || status == "approved" || status == "requested" || status == "queued"
	case "applied":
		return status == "applied" || status == "completed" || status == "active" || status == "disabled" || status == "rolled_back"
	case "rejected":
		return status == "rejected"
	default:
		return status == filter
	}
}

func (s *Server) changeArtifactView(artifact policy.GraphArtifact) map[string]any {
	view := map[string]any{
		"id":            artifact.ID,
		"group_id":      artifact.BundleID,
		"kind":          artifact.Kind,
		"version":       artifact.Version,
		"artifact_meta": artifact.ArtifactMeta,
		"created_at":    artifact.CreatedAt,
	}
	switch artifact.Kind {
	case "change_request":
		req := payloadMapValue(artifact.Payload, "change_request")
		view["change"] = req
		view["domain"] = req["domain"]
		view["action"] = req["action"]
		view["status"] = req["status"]
		view["requested_by"] = req["requested_by"]
		view["target_ids"] = req["target_ids"]
	case "change_decision":
		decision := payloadMapValue(artifact.Payload, "change_decision")
		view["change"] = decision
		view["domain"] = decision["domain"]
		view["status"] = decision["decision"]
		view["decision"] = decision["decision"]
		view["change_id"] = decision["change_id"]
		view["actor_id"] = decision["actor_id"]
	case "change_application":
		application := payloadMapValue(artifact.Payload, "change_application")
		view["change"] = application
		view["domain"] = application["domain"]
		view["status"] = application["status"]
		view["change_id"] = application["change_id"]
		view["applied_by"] = application["applied_by"]
		view["result_ids"] = application["result_ids"]
	}
	return view
}

func payloadMapValue(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return nil
	}
	if mapped, ok := raw.(map[string]any); ok {
		return mapped
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var mapped map[string]any
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil
	}
	return mapped
}

func firstQueryValue(query map[string][]string, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (s *Server) saveControlChange(ctx context.Context, request policy.ChangeRequest, decisions []policy.ChangeDecision, applications []policy.ChangeApplication) error {
	artifacts, edges := controlgraph.ChangeArtifacts(request, decisions, applications)
	if err := s.store.SavePolicyArtifacts(ctx, artifacts); err != nil {
		return err
	}
	return s.store.SavePolicyEdges(ctx, edges)
}

func controlChangeRequest(groupID string, domain string, action string, status string, actorID string, targetIDs []string, metadata map[string]any, now time.Time) policy.ChangeRequest {
	id := stableServerID("chgreq", groupID, domain, action, now.Format(time.RFC3339Nano))
	meta := artifactmeta.Meta{
		Kind:          "change_request",
		Source:        "operator_api",
		CreatedBy:     actorID,
		ApprovedBy:    actorID,
		Version:       now.Format(time.RFC3339Nano),
		LineageRootID: id,
	}
	return policy.ChangeRequest{
		ID:           id,
		ArtifactMeta: meta,
		GroupID:      groupID,
		Domain:       domain,
		Action:       action,
		Status:       status,
		RequestedBy:  actorID,
		TargetIDs:    append([]string(nil), targetIDs...),
		Metadata:     copyMap(metadata),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func controlChangeDecision(request policy.ChangeRequest, decision string, actorID string, metadata map[string]any, now time.Time) policy.ChangeDecision {
	id := stableServerID("chgdec", request.ID, decision, now.Format(time.RFC3339Nano))
	meta := request.ArtifactMeta
	meta.Kind = "change_decision"
	meta.CreatedBy = actorID
	meta.ApprovedBy = actorID
	meta.LineageRootID = request.ID
	meta.Version = now.Format(time.RFC3339Nano)
	return policy.ChangeDecision{
		ID:           id,
		ArtifactMeta: meta,
		GroupID:      request.GroupID,
		ChangeID:     request.ID,
		Domain:       request.Domain,
		Decision:     decision,
		ActorID:      actorID,
		Metadata:     copyMap(metadata),
		CreatedAt:    now,
	}
}

func controlChangeApplication(request policy.ChangeRequest, status string, actorID string, resultIDs []string, metadata map[string]any, now time.Time) policy.ChangeApplication {
	id := stableServerID("chgapp", request.ID, status, now.Format(time.RFC3339Nano))
	meta := request.ArtifactMeta
	meta.Kind = "change_application"
	meta.CreatedBy = actorID
	meta.ApprovedBy = actorID
	meta.LineageRootID = request.ID
	meta.Version = now.Format(time.RFC3339Nano)
	return policy.ChangeApplication{
		ID:           id,
		ArtifactMeta: meta,
		GroupID:      request.GroupID,
		ChangeID:     request.ID,
		Domain:       request.Domain,
		Status:       status,
		AppliedBy:    actorID,
		ResultIDs:    append([]string(nil), resultIDs...),
		Metadata:     copyMap(metadata),
		CreatedAt:    now,
	}
}

func copyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
