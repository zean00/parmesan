package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/session"
)

type operatorNotification struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Title       string         `json:"title"`
	SessionID   string         `json:"session_id,omitempty"`
	ExecutionID string         `json:"execution_id,omitempty"`
	AgentID     string         `json:"agent_id,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	Status      string         `json:"status"`
	Payload     map[string]any `json:"payload,omitempty"`
}

type agentStats struct {
	AgentID                 string    `json:"agent_id"`
	Window                  string    `json:"window"`
	WindowStartedAt         time.Time `json:"window_started_at"`
	WindowFinishedAt        time.Time `json:"window_finished_at"`
	ActiveSessionCount      int       `json:"active_session_count"`
	SessionsCreated         int       `json:"sessions_created"`
	FailedExecutions        int       `json:"failed_executions"`
	PendingApprovals        int       `json:"pending_approvals"`
	Takeovers               int       `json:"takeovers"`
	OperatorReplies         int       `json:"operator_replies"`
	AverageFirstResponseSec float64   `json:"average_first_response_seconds"`
}

func (s *Server) operatorGetAgentStats(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimSpace(r.PathValue("id"))
	if agentID == "" {
		http.Error(w, "agent id is required", http.StatusBadRequest)
		return
	}
	windowName, windowDuration, err := operatorStatsWindow(r.URL.Query().Get("window"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	finishedAt := time.Now().UTC()
	startedAt := finishedAt.Add(-windowDuration)
	stats, err := s.buildAgentStats(r.Context(), agentID, windowName, startedAt, finishedAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) operatorListNotifications(w http.ResponseWriter, r *http.Request) {
	limit, err := positiveQueryInt(r.URL.Query().Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if limit <= 0 {
		limit = 50
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	items, err := s.buildNotifications(r.Context(), agentID, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorStreamNotifications(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	seen := map[string]struct{}{}
	initial, err := s.buildNotifications(ctx, agentID, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(initial, func(i, j int) bool { return initial[i].CreatedAt.Before(initial[j].CreatedAt) })
	for _, item := range initial {
		if err := writeNotificationEvent(w, flusher, item); err != nil {
			return
		}
		seen[item.ID] = struct{}{}
	}

	if s.broker == nil {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}

	ch := make(chan sse.Envelope, 32)
	cancel := s.broker.Subscribe(adminStreamID, ch)
	defer cancel()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case env, ok := <-ch:
			if !ok {
				return
			}
			record, ok := env.Payload.(audit.Record)
			if !ok {
				continue
			}
			item, keep, err := s.notificationFromAuditRecord(ctx, record, agentID)
			if err != nil || !keep || item == nil {
				continue
			}
			if _, ok := seen[item.ID]; ok {
				continue
			}
			if err := writeNotificationEvent(w, flusher, *item); err != nil {
				return
			}
			seen[item.ID] = struct{}{}
		}
	}
}

func writeNotificationEvent(w http.ResponseWriter, flusher http.Flusher, item operatorNotification) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: notification\ndata: %s\n\n", item.ID, raw); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func operatorStatsWindow(raw string) (string, time.Duration, error) {
	switch strings.TrimSpace(raw) {
	case "", "24h":
		return "24h", 24 * time.Hour, nil
	case "1h":
		return "1h", time.Hour, nil
	case "7d":
		return "7d", 7 * 24 * time.Hour, nil
	default:
		return "", 0, fmt.Errorf("unsupported window %q", raw)
	}
}

func (s *Server) buildAgentStats(ctx context.Context, agentID string, windowName string, startedAt, finishedAt time.Time) (agentStats, error) {
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return agentStats{}, err
	}
	executions, err := s.store.ListExecutions(ctx)
	if err != nil {
		return agentStats{}, err
	}
	records, err := s.store.ListAuditRecords(ctx)
	if err != nil {
		return agentStats{}, err
	}

	filteredSessions := make([]session.Session, 0)
	sessionSet := map[string]session.Session{}
	activeCount := 0
	for _, item := range sessions {
		if item.AgentID != agentID {
			continue
		}
		sessionSet[item.ID] = item
		if item.Status != session.StatusClosed {
			activeCount++
		}
		if !item.CreatedAt.Before(startedAt) && !item.CreatedAt.After(finishedAt) {
			filteredSessions = append(filteredSessions, item)
		}
	}

	pendingApprovals := 0
	operatorReplies := 0
	var firstResponseDurations []float64
	for _, item := range filteredSessions {
		approvals, _ := s.store.ListApprovalSessions(ctx, item.ID)
		for _, approvalItem := range approvals {
			if approvalItem.Status == approval.StatusPending {
				pendingApprovals++
			}
		}
		events, _ := s.store.ListEvents(ctx, item.ID)
		firstCustomer := time.Time{}
		firstResponse := time.Time{}
		for _, event := range events {
			if event.CreatedAt.Before(startedAt) || event.CreatedAt.After(finishedAt) {
				continue
			}
			switch event.Source {
			case "customer":
				if firstCustomer.IsZero() {
					firstCustomer = event.CreatedAt
				}
			case "ai_agent", "human_agent", "human_agent_on_behalf_of_ai_agent":
				if event.Source == "human_agent" || event.Source == "human_agent_on_behalf_of_ai_agent" {
					operatorReplies++
				}
				if firstCustomer.IsZero() || !firstResponse.IsZero() {
					continue
				}
				firstResponse = event.CreatedAt
			}
		}
		if !firstCustomer.IsZero() && !firstResponse.IsZero() && firstResponse.After(firstCustomer) {
			firstResponseDurations = append(firstResponseDurations, firstResponse.Sub(firstCustomer).Seconds())
		}
	}

	failedExecutions := 0
	for _, item := range executions {
		sess, ok := sessionSet[item.SessionID]
		if !ok || sess.AgentID != agentID {
			continue
		}
		if item.Status == execution.StatusFailed && !item.UpdatedAt.Before(startedAt) && !item.UpdatedAt.After(finishedAt) {
			failedExecutions++
		}
	}

	takeovers := 0
	for _, record := range records {
		if record.Kind != "operator.takeover.started" {
			continue
		}
		sess, ok := sessionSet[record.SessionID]
		if !ok || sess.AgentID != agentID {
			continue
		}
		if record.CreatedAt.Before(startedAt) || record.CreatedAt.After(finishedAt) {
			continue
		}
		takeovers++
	}

	avgFirstResponse := 0.0
	if len(firstResponseDurations) > 0 {
		sum := 0.0
		for _, item := range firstResponseDurations {
			sum += item
		}
		avgFirstResponse = sum / float64(len(firstResponseDurations))
	}

	return agentStats{
		AgentID:                 agentID,
		Window:                  windowName,
		WindowStartedAt:         startedAt,
		WindowFinishedAt:        finishedAt,
		ActiveSessionCount:      activeCount,
		SessionsCreated:         len(filteredSessions),
		FailedExecutions:        failedExecutions,
		PendingApprovals:        pendingApprovals,
		Takeovers:               takeovers,
		OperatorReplies:         operatorReplies,
		AverageFirstResponseSec: avgFirstResponse,
	}, nil
}

func (s *Server) buildNotifications(ctx context.Context, agentID string, limit int) ([]operatorNotification, error) {
	records, err := s.store.ListAuditRecords(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	out := make([]operatorNotification, 0, limit)
	for _, record := range records {
		item, keep, err := s.notificationFromAuditRecord(ctx, record, agentID)
		if err != nil || !keep || item == nil {
			continue
		}
		out = append(out, *item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Server) notificationFromAuditRecord(ctx context.Context, record audit.Record, agentID string) (*operatorNotification, bool, error) {
	kind := strings.TrimSpace(record.Kind)
	if kind == "" {
		return nil, false, nil
	}
	sessionAgentID := ""
	if record.SessionID != "" {
		sess, err := s.store.GetSession(ctx, record.SessionID)
		if err == nil {
			sessionAgentID = sess.AgentID
		}
	}
	if agentID != "" && sessionAgentID != "" && sessionAgentID != agentID {
		return nil, false, nil
	}
	switch kind {
	case "approval.requested":
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "approval_requested",
			Severity:    "attention",
			Title:       "Approval required",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "execution.failed":
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "execution_failed",
			Severity:    "critical",
			Title:       "Execution failed",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "execution.blocked":
		severity := "attention"
		title := "Execution blocked"
		blockedReason := strings.TrimSpace(fmt.Sprint(record.Fields["blocked_reason"]))
		if blockedReason == execution.BlockedReasonRetryBudgetExhausted {
			severity = "critical"
			title = "Execution blocked after retries"
		}
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "execution_blocked",
			Severity:    severity,
			Title:       title,
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "tool.run.failed":
		severity := "critical"
		if boolValue(record.Fields["retryable"]) {
			severity = "attention"
		}
		title := "Tool call failed"
		toolName := strings.TrimSpace(fmt.Sprint(record.Fields["tool"]))
		if toolName != "" {
			title = fmt.Sprintf("Tool failed: %s", toolName)
		}
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "tool_failed",
			Severity:    severity,
			Title:       title,
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "operator.takeover.started":
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "takeover_started",
			Severity:    "info",
			Title:       "Manual takeover started",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "operator.takeover.ended":
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "takeover_ended",
			Severity:    "neutral",
			Title:       "Manual takeover ended",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "resolved",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "media.reprocess.failed", "media.enrichment.failed":
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "media_failed",
			Severity:    "attention",
			Title:       "Media processing failed",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	case "moderation.flagged":
		severity := "attention"
		if boolValue(record.Fields["jailbreak"]) {
			severity = "critical"
		}
		return &operatorNotification{
			ID:          record.ID,
			Kind:        "moderation_flagged",
			Severity:    severity,
			Title:       "Moderation flagged customer input",
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			AgentID:     sessionAgentID,
			TraceID:     record.TraceID,
			CreatedAt:   record.CreatedAt,
			Status:      "open",
			Payload:     cloneMap(record.Fields),
		}, true, nil
	default:
		return nil, false, nil
	}
}
