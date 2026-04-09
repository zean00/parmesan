package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
)

type Server struct {
	httpServer *http.Server
	repo       store.Repository
	writes     *asyncwrite.Queue
	sessions   *sessionsvc.Service
	interval   time.Duration
}

func New(addr string, repo store.Repository, writes *asyncwrite.Queue) *Server {
	s := &Server{
		repo:     repo,
		writes:   writes,
		sessions: sessionsvc.New(repo, writes),
		interval: time.Second,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /v1/web/messages", s.inboundMessage)
	mux.HandleFunc("GET /v1/web/conversations/{id}/events/stream", s.streamConversation)
	mux.HandleFunc("GET /v1/web/conversations/{id}/approvals", s.listApprovals)
	mux.HandleFunc("POST /v1/web/conversations/{id}/approvals/{approval_id}", s.respondApproval)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	go s.deliveryLoop(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "gateway", "channel": "web"})
}

func (s *Server) inboundMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConversationID string `json:"conversation_id"`
		UserID         string `json:"user_id"`
		Text           string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ConversationID) == "" || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "conversation_id and text are required", http.StatusBadRequest)
		return
	}

	binding, err := s.ensureBinding(r.Context(), req.ConversationID, req.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	execID := fmt.Sprintf("exec_%d", now.UnixNano())
	eventID := fmt.Sprintf("evt_%d", now.UnixNano())
	traceID := fmt.Sprintf("trace_%d", now.UnixNano())

	exec := execution.TurnExecution{
		ID:             execID,
		SessionID:      binding.SessionID,
		TriggerEventID: eventID,
		TraceID:        traceID,
		Status:         execution.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	steps := []execution.ExecutionStep{
		newStep(execID, "ingest", false),
		newStep(execID, "resolve_policy", true),
		newStep(execID, "match_and_plan", true),
		newStep(execID, "compose_response", true),
		newStep(execID, "deliver_response", false),
	}
	if err := s.writes.CreateExecution(r.Context(), exec, steps); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = s.sessions.CreateEvent(r.Context(), sessionsvc.CreateEventParams{
		ID:        eventID,
		SessionID: binding.SessionID,
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: req.Text}},
		Metadata: map[string]any{
			"conversation_id": req.ConversationID,
			"user_id":         req.UserID,
		},
		ExecutionID: execID,
		TraceID:     traceID,
		CreatedAt:   now,
		Async:       true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.writes.AppendAuditRecord(r.Context(), audit.Record{
		ID:          traceID,
		Kind:        "gateway.ingress",
		SessionID:   binding.SessionID,
		ExecutionID: execID,
		TraceID:     traceID,
		Message:     "gateway accepted inbound web message",
		Fields:      map[string]any{"conversation_id": req.ConversationID, "event_id": eventID},
		CreatedAt:   now,
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"conversation_id": binding.ExternalConversationID,
		"session_id":      binding.SessionID,
		"execution_id":    execID,
		"event_id":        eventID,
		"status":          "queued",
	})
}

func (s *Server) streamConversation(w http.ResponseWriter, r *http.Request) {
	binding, err := s.repo.GetConversationBinding(r.Context(), "web", r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	seen := map[string]struct{}{}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.flushConversation(r.Context(), w, flusher, binding, seen); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	binding, err := s.repo.GetConversationBinding(r.Context(), "web", r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	items, err := s.repo.ListApprovalSessions(r.Context(), binding.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) respondApproval(w http.ResponseWriter, r *http.Request) {
	binding, err := s.repo.GetConversationBinding(r.Context(), "web", r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	item, err := s.repo.GetApprovalSession(r.Context(), r.PathValue("approval_id"))
	if err != nil || item.SessionID != binding.SessionID {
		http.Error(w, "approval session not found", http.StatusNotFound)
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision != "approve" && decision != "reject" {
		http.Error(w, "decision must be approve or reject", http.StatusBadRequest)
		return
	}
	item.Decision = decision
	item.UpdatedAt = time.Now().UTC()
	if decision == "approve" {
		item.Status = approval.StatusApproved
	} else {
		item.Status = approval.StatusRejected
	}
	if err := s.repo.SaveApprovalSession(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	execs, err := s.repo.ListExecutions(r.Context())
	traceID := ""
	if err == nil {
		for _, exec := range execs {
			if exec.ID == item.ExecutionID {
				traceID = exec.TraceID
			}
			if exec.ID == item.ExecutionID && exec.Status == execution.StatusBlocked {
				exec.Status = execution.StatusPending
				exec.UpdatedAt = time.Now().UTC()
				_ = s.repo.UpdateExecution(r.Context(), exec)
			}
		}
	}
	event, err := s.sessions.CreateApprovalResolvedEvent(r.Context(), binding.SessionID, "gateway", item.ExecutionID, traceID, item.ID, item.ToolID, decision, nil, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = event
	_ = s.writes.AppendAuditRecord(r.Context(), audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        "approval.resolved",
		SessionID:   binding.SessionID,
		ExecutionID: item.ExecutionID,
		Message:     "approval resolved in gateway",
		Fields:      map[string]any{"approval_id": item.ID, "decision": decision},
		CreatedAt:   time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) ensureBinding(ctx context.Context, conversationID, userID string) (gatewaydomain.ConversationBinding, error) {
	binding, err := s.repo.GetConversationBinding(ctx, "web", conversationID)
	if err == nil {
		sess, sessErr := s.repo.GetSession(ctx, binding.SessionID)
		if sessErr == nil && sess.Status != session.StatusClosed {
			return binding, nil
		}
	}
	now := time.Now().UTC()
	sess := session.Session{
		ID:             fmt.Sprintf("sess_%d", now.UnixNano()),
		Channel:        "web",
		CustomerID:     userID,
		Status:         session.StatusActive,
		Metadata:       map[string]any{"external_conversation_id": conversationID},
		Labels:         []string{},
		CreatedAt:      now,
		LastActivityAt: now,
	}
	if _, err := s.sessions.CreateSession(ctx, sess); err != nil {
		return gatewaydomain.ConversationBinding{}, err
	}
	binding = gatewaydomain.ConversationBinding{
		ID:                     fmt.Sprintf("binding_%d", now.UnixNano()),
		Channel:                "web",
		ExternalConversationID: conversationID,
		ExternalUserID:         userID,
		SessionID:              sess.ID,
		CapabilityProfile: gatewaydomain.CapabilityProfile{
			SupportsText:                true,
			SupportsImageUpload:         true,
			SupportsInteractiveApproval: true,
			SupportsThreads:             false,
			MaxAttachmentBytes:          5 << 20,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.writes.UpsertConversationBinding(ctx, binding); err != nil {
		return gatewaydomain.ConversationBinding{}, err
	}
	return binding, nil
}

func (s *Server) flushConversation(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, binding gatewaydomain.ConversationBinding, seen map[string]struct{}) error {
	events, err := s.repo.ListEvents(ctx, binding.SessionID)
	if err != nil {
		return err
	}
	approvals, err := s.repo.ListApprovalSessions(ctx, binding.SessionID)
	if err != nil {
		return err
	}
	records, err := s.repo.ListAuditRecords(ctx)
	if err != nil {
		return err
	}

	type item struct {
		id   string
		name string
		when time.Time
		body any
	}
	var items []item
	for _, event := range events {
		if _, ok := seen[event.ID]; ok {
			continue
		}
		if event.Source == "ai_agent" {
			_ = s.saveDelivery(ctx, binding, event)
		}
		items = append(items, item{id: event.ID, name: "session.event.created", when: event.CreatedAt, body: event})
	}
	for _, approvalItem := range approvals {
		if approvalItem.Status != approval.StatusPending {
			continue
		}
		if _, ok := seen[approvalItem.ID]; ok {
			continue
		}
		items = append(items, item{id: approvalItem.ID, name: "approval.requested", when: approvalItem.CreatedAt, body: approvalItem})
	}
	for _, record := range records {
		if record.SessionID != binding.SessionID {
			continue
		}
		if _, ok := seen[record.ID]; ok {
			continue
		}
		items = append(items, item{id: record.ID, name: record.Kind, when: record.CreatedAt, body: record})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].when.Before(items[j].when) })
	for _, item := range items {
		seen[item.id] = struct{}{}
		raw, err := json.Marshal(item.body)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", item.id, item.name, raw); err != nil {
			return err
		}
	}
	flusher.Flush()
	return nil
}

func (s *Server) saveDelivery(ctx context.Context, binding gatewaydomain.ConversationBinding, event session.Event) error {
	attempts, err := s.repo.ListDeliveryAttempts(ctx, event.ExecutionID)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if attempt.EventID == event.ID {
			return nil
		}
	}
	attempt := delivery.Attempt{
		ID:             fmt.Sprintf("delivery_%d", time.Now().UnixNano()),
		SessionID:      binding.SessionID,
		ExecutionID:    event.ExecutionID,
		EventID:        event.ID,
		Channel:        binding.Channel,
		Status:         "delivered",
		IdempotencyKey: binding.ID + "_" + event.ID,
		CreatedAt:      time.Now().UTC(),
	}
	if s.writes != nil {
		return s.writes.SaveDeliveryAttempt(ctx, attempt)
	}
	return s.repo.SaveDeliveryAttempt(ctx, attempt)
}

func (s *Server) deliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bindings, err := s.repo.ListConversationBindings(ctx)
			if err != nil {
				continue
			}
			for _, binding := range bindings {
				if binding.Channel != "web" {
					continue
				}
				events, err := s.repo.ListEvents(ctx, binding.SessionID)
				if err != nil {
					continue
				}
				for _, event := range events {
					if event.Source == "ai_agent" {
						_ = s.saveDelivery(ctx, binding, event)
					}
				}
			}
		}
	}
}

func newStep(execID, name string, recomputable bool) execution.ExecutionStep {
	now := time.Now().UTC()
	return execution.ExecutionStep{
		ID:             fmt.Sprintf("%s_%s", execID, name),
		ExecutionID:    execID,
		Name:           name,
		Status:         execution.StatusPending,
		Recomputable:   recomputable,
		IdempotencyKey: fmt.Sprintf("%s_%s", execID, name),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
