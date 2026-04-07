package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/acp"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/policyyaml"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/toolsync"
)

type Server struct {
	httpServer *http.Server
	store      store.Repository
	writes     *asyncwrite.Queue
	broker     *sse.Broker
	router     *model.Router
	syncer     *toolsync.Syncer
	sessions   *sessionsvc.Service
	listener   *sessionsvc.Listener
}

const adminStreamID = "__admin__"

type streamItem struct {
	id   string
	name string
	when time.Time
	body any
}

func New(addr string, repo store.Repository, writes *asyncwrite.Queue, broker *sse.Broker, router *model.Router, syncer *toolsync.Syncer) *Server {
	s := &Server{
		store:    repo,
		writes:   writes,
		broker:   broker,
		router:   router,
		syncer:   syncer,
		sessions: sessionsvc.New(repo, writes),
		listener: sessionsvc.NewListener(repo),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("GET /v1/models/providers", s.listModelProviders)
	mux.HandleFunc("POST /v1/policy/validate", s.validatePolicy)
	mux.HandleFunc("POST /v1/policy/import", s.importPolicy)
	mux.HandleFunc("GET /v1/policy/bundles", s.listBundles)
	mux.HandleFunc("POST /v1/proposals", s.createProposal)
	mux.HandleFunc("GET /v1/proposals", s.listProposals)
	mux.HandleFunc("GET /v1/proposals/{id}", s.getProposal)
	mux.HandleFunc("GET /v1/proposals/{id}/summary", s.getProposalSummary)
	mux.HandleFunc("POST /v1/proposals/{id}/state", s.transitionProposal)
	mux.HandleFunc("POST /v1/rollouts", s.createRollout)
	mux.HandleFunc("GET /v1/rollouts", s.listRollouts)
	mux.HandleFunc("GET /v1/rollouts/{id}", s.getRollout)
	mux.HandleFunc("POST /v1/rollouts/{id}/disable", s.disableRollout)
	mux.HandleFunc("POST /v1/rollouts/{id}/rollback", s.rollbackRollout)
	mux.HandleFunc("GET /v1/admin/events/stream", s.streamAdminEvents)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("GET /v1/sessions/{id}/events", s.listEvents)
	mux.HandleFunc("POST /v1/sessions/{id}/events", s.appendEvent)
	mux.HandleFunc("GET /v1/sessions/{id}/events/stream", s.streamEvents)
	mux.HandleFunc("POST /v1/acp/sessions", s.acpCreateSession)
	mux.HandleFunc("GET /v1/acp/sessions/{id}/events", s.acpListEvents)
	mux.HandleFunc("POST /v1/acp/sessions/{id}/events", s.acpAppendEvent)
	mux.HandleFunc("GET /v1/acp/sessions/{id}/events/stream", s.streamEvents)
	mux.HandleFunc("POST /v1/tools/providers/register", s.registerProvider)
	mux.HandleFunc("POST /v1/tools/providers/{id}/auth", s.saveProviderAuth)
	mux.HandleFunc("GET /v1/tools/providers/{id}/auth", s.getProviderAuth)
	mux.HandleFunc("POST /v1/tools/providers/{id}/sync", s.syncProvider)
	mux.HandleFunc("GET /v1/tools/providers", s.listProviders)
	mux.HandleFunc("GET /v1/tools/catalog", s.listCatalog)
	mux.HandleFunc("GET /v1/executions", s.listExecutions)
	mux.HandleFunc("GET /v1/executions/{id}", s.getExecution)
	mux.HandleFunc("GET /v1/executions/{id}/resolved-policy", s.getResolvedPolicy)
	mux.HandleFunc("GET /v1/executions/{id}/tool-runs", s.listToolRuns)
	mux.HandleFunc("GET /v1/executions/{id}/delivery-attempts", s.listDeliveryAttempts)
	mux.HandleFunc("POST /v1/replays", s.replayExecution)
	mux.HandleFunc("GET /v1/replays", s.listReplayExecutions)
	mux.HandleFunc("GET /v1/replays/{id}", s.getReplayExecution)
	mux.HandleFunc("GET /v1/replays/{id}/diff", s.getReplayDiff)
	mux.HandleFunc("GET /v1/traces", s.listTraces)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           loggingMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

func (s *Server) Run(ctx context.Context) error {
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	provider, err := s.router.Route(model.CapabilityReasoning)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"service": "api", "provider_error": err.Error()})
		return
	}
	resp := map[string]any{
		"service":                    "api",
		"default_reasoning_provider": provider.Name(),
		"durable_execution":          "resumable_async_checkpoints",
	}
	if s.writes != nil {
		resp["async_write_queue"] = s.writes.Stats()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listModelProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": s.router.Snapshot(),
	})
}

func (s *Server) validatePolicy(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	bundle, err := policyyaml.ParseBundle(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, bundle)
}

func (s *Server) importPolicy(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	bundle, err := policyyaml.ParseBundle(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.writes.SaveBundle(r.Context(), bundle); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "policy.import",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued policy bundle import",
		Fields:    map[string]any{"bundle_id": bundle.ID, "version": bundle.Version},
		CreatedAt: time.Now().UTC(),
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "queued",
		"bundle": bundle,
	})
}

func (s *Server) listBundles(w http.ResponseWriter, r *http.Request) {
	bundles, err := s.store.ListBundles(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, bundles)
}

func (s *Server) createProposal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID                     string   `json:"id"`
		SourceBundleID         string   `json:"source_bundle_id"`
		CandidateBundleID      string   `json:"candidate_bundle_id"`
		Rationale              string   `json:"rationale"`
		EvidenceRefs           []string `json:"evidence_refs"`
		RiskFlags              []string `json:"risk_flags"`
		RequiresManualApproval bool     `json:"requires_manual_approval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SourceBundleID) == "" || strings.TrimSpace(req.CandidateBundleID) == "" {
		http.Error(w, "source_bundle_id and candidate_bundle_id are required", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("proposal_%d", time.Now().UnixNano())
	}
	now := time.Now().UTC()
	proposal := rollout.Proposal{
		ID:                     req.ID,
		SourceBundleID:         req.SourceBundleID,
		CandidateBundleID:      req.CandidateBundleID,
		State:                  rollout.StateProposed,
		Rationale:              req.Rationale,
		EvidenceRefs:           req.EvidenceRefs,
		RiskFlags:              req.RiskFlags,
		RequiresManualApproval: req.RequiresManualApproval,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := s.writes.SaveProposal(r.Context(), proposal); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:      fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:    "proposal.created",
		TraceID: fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message: "queued proposal creation",
		Fields: map[string]any{
			"proposal_id":                 proposal.ID,
			"source_bundle_id":            proposal.SourceBundleID,
			"candidate_bundle_id":         proposal.CandidateBundleID,
			"requires_high_risk_approval": proposal.RequiresManualApproval,
		},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, proposal)
}

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListProposals(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) getProposalSummary(w http.ResponseWriter, r *http.Request) {
	proposalID := r.PathValue("id")
	item, err := s.store.GetProposal(r.Context(), proposalID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	rollouts, err := s.store.ListRollouts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	evalRuns, err := s.store.ListEvalRuns(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filteredRollouts := make([]rollout.Record, 0)
	for _, record := range rollouts {
		if record.ProposalID == proposalID {
			filteredRollouts = append(filteredRollouts, record)
		}
	}
	filteredRuns := make([]replay.Run, 0)
	for _, run := range evalRuns {
		if run.ProposalID == proposalID {
			filteredRuns = append(filteredRuns, run)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"proposal":  item,
		"rollouts":  filteredRollouts,
		"eval_runs": filteredRuns,
	})
}

func (s *Server) transitionProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		State            rollout.ProposalState `json:"state"`
		ApprovedHighRisk bool                  `json:"approved_high_risk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validProposalTransition(item.State, req.State, item.RequiresManualApproval, req.ApprovedHighRisk) {
		http.Error(w, "invalid proposal state transition", http.StatusBadRequest)
		return
	}
	if req.State == rollout.StateActive {
		if err := s.promoteProposalActive(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		item.State = rollout.StateActive
		item.UpdatedAt = time.Now().UTC()
		if err := s.writes.SaveProposal(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.appendTrace(r.Context(), audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "proposal.state.changed",
			TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Message:   "queued proposal state transition",
			Fields:    map[string]any{"proposal_id": item.ID, "state": item.State},
			CreatedAt: time.Now().UTC(),
		})
		writeJSON(w, http.StatusAccepted, item)
		return
	}
	item.State = req.State
	item.UpdatedAt = time.Now().UTC()
	if err := s.writes.SaveProposal(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "proposal.state.changed",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued proposal state transition",
		Fields:    map[string]any{"proposal_id": item.ID, "state": item.State},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) createRollout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID                string   `json:"id"`
		ProposalID        string   `json:"proposal_id"`
		Channel           string   `json:"channel"`
		Percentage        int      `json:"percentage"`
		IncludeSessionIDs []string `json:"include_session_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ProposalID) == "" {
		http.Error(w, "proposal_id is required", http.StatusBadRequest)
		return
	}
	proposal, err := s.store.GetProposal(r.Context(), req.ProposalID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	switch proposal.State {
	case rollout.StateShadow:
	case rollout.StateCanary:
	default:
		http.Error(w, "proposal must be in shadow or canary state before rollout", http.StatusConflict)
		return
	}
	rollouts, err := s.store.ListRollouts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Channel == "" {
		req.Channel = "web"
	}
	for _, item := range rollouts {
		if item.Status == rollout.RolloutActive && item.Channel == req.Channel {
			http.Error(w, "active rollout already exists for channel", http.StatusConflict)
			return
		}
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("rollout_%d", time.Now().UnixNano())
	}
	now := time.Now().UTC()
	record := rollout.Record{
		ID:                req.ID,
		ProposalID:        proposal.ID,
		Status:            rollout.RolloutActive,
		Channel:           req.Channel,
		Percentage:        req.Percentage,
		IncludeSessionIDs: req.IncludeSessionIDs,
		PreviousBundleID:  proposal.SourceBundleID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.writes.SaveRollout(r.Context(), record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if proposal.State != rollout.StateCanary {
		proposal.State = rollout.StateCanary
		proposal.UpdatedAt = now
		if err := s.writes.SaveProposal(r.Context(), proposal); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "rollout.started",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued canary rollout creation",
		Fields:    map[string]any{"rollout_id": record.ID, "proposal_id": record.ProposalID, "channel": record.Channel, "percentage": record.Percentage},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, record)
}

func (s *Server) listRollouts(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRollouts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getRollout(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRollout(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) disableRollout(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRollout(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	item.Status = rollout.RolloutDisabled
	item.UpdatedAt = time.Now().UTC()
	if err := s.writes.SaveRollout(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "rollout.disabled",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued rollout disable",
		Fields:    map[string]any{"rollout_id": item.ID, "proposal_id": item.ProposalID},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) rollbackRollout(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRollout(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	item.Status = rollout.RolloutRolledBack
	item.UpdatedAt = time.Now().UTC()
	if err := s.writes.SaveRollout(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proposal, err := s.store.GetProposal(r.Context(), item.ProposalID)
	if err == nil && proposal.State == rollout.StateCanary {
		proposal.State = rollout.StateShadow
		proposal.UpdatedAt = item.UpdatedAt
		if err := s.writes.SaveProposal(r.Context(), proposal); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "rollout.rolled_back",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued rollout rollback",
		Fields:    map[string]any{"rollout_id": item.ID, "proposal_id": item.ProposalID},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(req.Channel) == "" {
		req.Channel = "web"
	}

	sess := session.Session{
		ID:        req.ID,
		Channel:   req.Channel,
		Metadata:  map[string]any{},
		Labels:    []string{},
		CreatedAt: time.Now().UTC(),
	}
	sess, err := s.sessions.CreateSession(r.Context(), sess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) appendEvent(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		ID      string                `json:"id"`
		Source  string                `json:"source"`
		Kind    string                `json:"kind"`
		Content []session.ContentPart `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		req.ID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if req.Source == "" {
		req.Source = "customer"
	}
	if req.Kind == "" {
		req.Kind = "message"
	}

	execID := fmt.Sprintf("exec_%d", time.Now().UnixNano())
	traceID := fmt.Sprintf("trace_%d", time.Now().UnixNano())
	exec := execution.TurnExecution{
		ID:             execID,
		SessionID:      sessionID,
		TriggerEventID: req.ID,
		TraceID:        traceID,
		Status:         execution.StatusRunning,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
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

	event, err := s.sessions.CreateEvent(r.Context(), sessionsvc.CreateEventParams{
		ID:          req.ID,
		SessionID:   sessionID,
		Source:      req.Source,
		Kind:        req.Kind,
		Content:     req.Content,
		ExecutionID: execID,
		TraceID:     traceID,
		CreatedAt:   time.Now().UTC(),
		Async:       true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:          traceID,
		Kind:        "session.event",
		SessionID:   sessionID,
		ExecutionID: execID,
		TraceID:     traceID,
		Message:     "queued session event append",
		Fields:      map[string]any{"event_id": event.ID, "kind": event.Kind, "source": event.Source},
		CreatedAt:   time.Now().UTC(),
	})

	s.broker.Publish(sessionID, sse.Envelope{
		EventID:     event.ID,
		SessionID:   sessionID,
		ExecutionID: execID,
		Type:        "session.event.created",
		Payload:     event,
		CreatedAt:   time.Now().UTC(),
	})

	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.sessions.ListEvents(r.Context(), session.EventQuery{SessionID: r.PathValue("id")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	seen := map[string]struct{}{}
	lastOffset := int64(0)

	for {
		var err error
		lastOffset, err = s.flushPersistedStream(ctx, w, flusher, sessionID, seen, lastOffset)
		if err != nil {
			return
		}
		waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, _ = s.listener.WaitForMoreEvents(waitCtx, session.EventQuery{
			SessionID:      sessionID,
			MinOffset:      lastOffset + 1,
			ExcludeDeleted: true,
		})
		cancel()
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (s *Server) acpCreateSession(w http.ResponseWriter, r *http.Request) {
	service := acp.NewService(s.sessions)
	var req acp.Session
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(req.Channel) == "" {
		req.Channel = "web"
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	created, err := service.OpenSession(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) acpAppendEvent(w http.ResponseWriter, r *http.Request) {
	service := acp.NewService(s.sessions)
	var req acp.Event
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.SessionID = r.PathValue("id")
	if strings.TrimSpace(req.ID) == "" {
		req.ID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	created, err := service.CreateEvent(r.Context(), req, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) acpListEvents(w http.ResponseWriter, r *http.Request) {
	service := acp.NewService(s.sessions)
	minOffset := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("min_offset")); raw != "" {
		if _, err := fmt.Sscan(raw, &minOffset); err != nil {
			http.Error(w, "invalid min_offset", http.StatusBadRequest)
			return
		}
	}
	events, err := service.ListEvents(r.Context(), r.PathValue("id"), minOffset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) streamAdminEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	seen := map[string]struct{}{}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := s.flushAuditStream(ctx, w, flusher, seen, func(audit.Record) bool { return true }); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) registerProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string            `json:"id"`
		Kind tool.ProviderKind `json:"kind"`
		Name string            `json:"name"`
		URI  string            `json:"uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("provider_%d", time.Now().UnixNano())
	}
	binding := tool.ProviderBinding{
		ID:           req.ID,
		Kind:         req.Kind,
		Name:         req.Name,
		URI:          req.URI,
		RegisteredAt: time.Now().UTC(),
		Healthy:      true,
	}
	if err := s.store.RegisterProvider(r.Context(), binding); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "tool.provider.register",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued tool provider registration",
		Fields:    map[string]any{"provider_id": binding.ID, "kind": binding.Kind, "uri": binding.URI},
		CreatedAt: time.Now().UTC(),
	})
	if s.syncer != nil {
		go func(binding tool.ProviderBinding) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			entries, err := s.syncer.SyncProvider(ctx, binding)
			if err != nil {
				log.Printf("sync provider %s: %v", binding.ID, err)
				return
			}
			if err := s.writes.SaveCatalogEntries(ctx, entries); err != nil {
				log.Printf("save catalog entries for %s: %v", binding.ID, err)
			}
		}(binding)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":   "queued",
		"provider": binding,
	})
}

func (s *Server) saveProviderAuth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if _, err := s.store.GetProvider(r.Context(), providerID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		Type       tool.AuthType `json:"type"`
		HeaderName string        `json:"header_name,omitempty"`
		Secret     string        `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		http.Error(w, "secret is required", http.StatusBadRequest)
		return
	}
	if req.Type != tool.AuthBearer && req.Type != tool.AuthHeader {
		http.Error(w, "unsupported auth type", http.StatusBadRequest)
		return
	}
	binding := tool.AuthBinding{
		ProviderID: providerID,
		Type:       req.Type,
		HeaderName: req.HeaderName,
		Secret:     req.Secret,
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.writes.SaveProviderAuthBinding(r.Context(), binding); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "tool.provider.auth.updated",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued provider auth update",
		Fields:    map[string]any{"provider_id": providerID, "auth_type": binding.Type},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, redactedAuthBinding(binding))
}

func (s *Server) getProviderAuth(w http.ResponseWriter, r *http.Request) {
	binding, err := s.store.GetProviderAuthBinding(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, redactedAuthBinding(binding))
}

func (s *Server) syncProvider(w http.ResponseWriter, r *http.Request) {
	if s.syncer == nil {
		http.Error(w, "syncer unavailable", http.StatusServiceUnavailable)
		return
	}
	providerID := r.PathValue("id")
	binding, err := s.store.GetProvider(r.Context(), providerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	entries, err := s.syncer.SyncProvider(r.Context(), binding)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.writes.SaveCatalogEntries(r.Context(), entries); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "tool.provider.sync",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued tool provider sync",
		Fields:    map[string]any{"provider_id": binding.ID, "entries": len(entries)},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":      "queued",
		"provider_id": binding.ID,
		"entries":     len(entries),
	})
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, providers)
}

func redactedAuthBinding(binding tool.AuthBinding) map[string]any {
	return map[string]any{
		"provider_id": binding.ProviderID,
		"type":        binding.Type,
		"header_name": binding.HeaderName,
		"has_secret":  strings.TrimSpace(binding.Secret) != "",
		"updated_at":  binding.UpdatedAt,
	}
}

func (s *Server) listCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListCatalogEntries(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, catalogEntriesResponse(entries))
}

func catalogEntriesResponse(entries []tool.CatalogEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":               entry.ID,
			"provider_id":      entry.ProviderID,
			"name":             entry.Name,
			"description":      entry.Description,
			"schema":           entry.Schema,
			"runtime_protocol": entry.RuntimeProtocol,
			"metadata_json":    entry.MetadataJSON,
			"imported_at":      entry.ImportedAt,
		}
		metadata := parseCatalogMetadata(entry.MetadataJSON)
		if len(metadata) > 0 {
			item["metadata"] = metadata
			if value, ok := metadata["document_id"]; ok {
				item["document_id"] = value
			}
			if value, ok := metadata["module_path"]; ok {
				item["module_path"] = value
			}
		}
		out = append(out, item)
	}
	return out
}

func parseCatalogMetadata(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	executions, err := s.store.ListExecutions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, executions)
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	exec, steps, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"execution": exec,
		"steps":     steps,
	})
}

func (s *Server) getResolvedPolicy(w http.ResponseWriter, r *http.Request) {
	exec, _, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	view, err := s.resolveExecutionView(r.Context(), exec, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) listTraces(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListAuditRecords(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) listToolRuns(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListToolRuns(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listDeliveryAttempts(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDeliveryAttempts(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) replayExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionID    string `json:"execution_id"`
		ProposalID     string `json:"proposal_id,omitempty"`
		ShadowBundleID string `json:"shadow_bundle_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ExecutionID) == "" {
		http.Error(w, "execution_id is required", http.StatusBadRequest)
		return
	}
	exec, _, err := s.store.GetExecution(r.Context(), req.ExecutionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	run := replay.Run{
		ID:                fmt.Sprintf("eval_%d", now.UnixNano()),
		SourceExecutionID: exec.ID,
		ProposalID:        req.ProposalID,
		ActiveBundleID:    exec.PolicyBundleID,
		ShadowBundleID:    req.ShadowBundleID,
		Status:            replay.StatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if req.ShadowBundleID != "" {
		run.Type = replay.TypeShadow
	} else {
		run.Type = replay.TypeReplay
	}
	if err := s.writes.CreateEvalRun(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "replay.started",
		TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Message:   "queued replay or shadow evaluation",
		Fields:    map[string]any{"eval_run_id": run.ID, "execution_id": run.SourceExecutionID, "proposal_id": run.ProposalID, "type": run.Type},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) listReplayExecutions(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListEvalRuns(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func validProposalTransition(current, next rollout.ProposalState, requiresManualApproval bool, approvedHighRisk bool) bool {
	switch current {
	case rollout.StateProposed:
		return next == rollout.StateReviewed
	case rollout.StateReviewed:
		return next == rollout.StateShadow
	case rollout.StateShadow:
		return next == rollout.StateCanary
	case rollout.StateCanary:
		if next == rollout.StateActive {
			if requiresManualApproval && !approvedHighRisk {
				return false
			}
			return true
		}
		return next == rollout.StateRetired
	case rollout.StateActive:
		return next == rollout.StateRetired
	default:
		return false
	}
}

func (s *Server) promoteProposalActive(ctx context.Context, active rollout.Proposal) error {
	proposals, err := s.store.ListProposals(ctx)
	if err != nil {
		return err
	}
	rollouts, err := s.store.ListRollouts(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, item := range proposals {
		if item.ID == active.ID {
			continue
		}
		if item.State == rollout.StateActive {
			item.State = rollout.StateRetired
			item.UpdatedAt = now
			if err := s.writes.SaveProposal(ctx, item); err != nil {
				return err
			}
			s.appendTrace(ctx, audit.Record{
				ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
				Kind:      "proposal.retired",
				TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
				Message:   "queued retirement of previously active proposal",
				Fields:    map[string]any{"proposal_id": item.ID},
				CreatedAt: now,
			})
		}
	}
	for _, record := range rollouts {
		if record.ProposalID != active.ID || record.Status != rollout.RolloutActive {
			continue
		}
		record.Status = rollout.RolloutDisabled
		record.UpdatedAt = now
		if err := s.writes.SaveRollout(ctx, record); err != nil {
			return err
		}
		s.appendTrace(ctx, audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "rollout.disabled",
			TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Message:   "queued rollout disable after active promotion",
			Fields:    map[string]any{"rollout_id": record.ID, "proposal_id": record.ProposalID},
			CreatedAt: now,
		})
	}
	return nil
}

func (s *Server) getReplayExecution(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetEvalRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) getReplayDiff(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetEvalRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var diff any = map[string]any{}
	if strings.TrimSpace(run.DiffJSON) != "" {
		if err := json.Unmarshal([]byte(run.DiffJSON), &diff); err != nil {
			diff = map[string]any{"raw": run.DiffJSON}
		}
	}
	var result any = map[string]any{}
	if strings.TrimSpace(run.ResultJSON) != "" {
		if err := json.Unmarshal([]byte(run.ResultJSON), &result); err != nil {
			result = map[string]any{"raw": run.ResultJSON}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     run.ID,
		"type":   run.Type,
		"status": run.Status,
		"result": result,
		"diff":   diff,
		"error":  run.LastError,
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newStep(execID, name string, recomputable bool) execution.ExecutionStep {
	now := time.Now().UTC()
	return execution.ExecutionStep{
		ID:             fmt.Sprintf("%s_%s", execID, name),
		ExecutionID:    execID,
		Name:           name,
		Status:         execution.StatusPending,
		Attempt:        0,
		Recomputable:   recomputable,
		IdempotencyKey: fmt.Sprintf("%s_%s", execID, name),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (s *Server) appendTrace(ctx context.Context, record audit.Record) {
	if s.writes == nil {
		return
	}
	if err := s.writes.AppendAuditRecord(ctx, record); err != nil {
		log.Printf("append trace: %v", err)
	}
	if s.broker != nil {
		s.broker.Publish(adminStreamID, sse.Envelope{
			EventID:   record.ID,
			TraceID:   record.TraceID,
			Type:      record.Kind,
			Payload:   record,
			CreatedAt: record.CreatedAt,
		})
	}
}

func (s *Server) flushPersistedStream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sessionID string, seen map[string]struct{}, lastOffset int64) (int64, error) {
	events, err := s.sessions.ListEvents(ctx, session.EventQuery{
		SessionID:      sessionID,
		MinOffset:      lastOffset + 1,
		ExcludeDeleted: true,
	})
	if err != nil {
		return lastOffset, err
	}
	var items []streamItem
	for _, event := range events {
		if _, ok := seen[event.ID]; ok {
			continue
		}
		items = append(items, streamItem{
			id:   event.ID,
			name: "session.event.created",
			when: event.CreatedAt,
			body: event,
		})
		if event.Offset > lastOffset {
			lastOffset = event.Offset
		}
	}
	items, err = s.appendAuditItems(ctx, items, seen, func(record audit.Record) bool {
		return record.SessionID == sessionID
	})
	if err != nil {
		return lastOffset, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].when.Before(items[j].when) })
	for _, item := range items {
		seen[item.id] = struct{}{}
		raw, err := json.Marshal(item.body)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", item.id, item.name, raw); err != nil {
			return lastOffset, err
		}
	}
	flusher.Flush()
	return lastOffset, nil
}

func (s *Server) flushAuditStream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, seen map[string]struct{}, include func(audit.Record) bool) error {
	items, err := s.appendAuditItems(ctx, nil, seen, include)
	if err != nil {
		return err
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

func (s *Server) appendAuditItems(ctx context.Context, items []streamItem, seen map[string]struct{}, include func(audit.Record) bool) ([]streamItem, error) {
	records, err := s.store.ListAuditRecords(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if include != nil && !include(record) {
			continue
		}
		if _, ok := seen[record.ID]; ok {
			continue
		}
		items = append(items, streamItem{
			id:   record.ID,
			name: record.Kind,
			when: record.CreatedAt,
			body: record,
		})
	}
	return items, nil
}

type resolvedPolicyResponse struct {
	BundleID             string                            `json:"bundle_id,omitempty"`
	ProposalID           string                            `json:"proposal_id,omitempty"`
	RolloutID            string                            `json:"rollout_id,omitempty"`
	SelectionReason      string                            `json:"selection_reason,omitempty"`
	CompositionMode      string                            `json:"composition_mode,omitempty"`
	NoMatch              string                            `json:"no_match,omitempty"`
	MatchedObservations  []string                          `json:"matched_observations,omitempty"`
	MatchedGuidelines    []string                          `json:"matched_guidelines,omitempty"`
	SuppressedGuidelines []string                          `json:"suppressed_guidelines,omitempty"`
	ReapplyGuidelines    []string                          `json:"reapply_guidelines,omitempty"`
	CustomerBlocked      []string                          `json:"customer_blocked,omitempty"`
	ActiveJourney        string                            `json:"active_journey,omitempty"`
	ActiveJourneyState   string                            `json:"active_journey_state,omitempty"`
	JourneyDecision      string                            `json:"journey_decision,omitempty"`
	SelectedTool         string                            `json:"selected_tool,omitempty"`
	ToolCanRun           bool                              `json:"tool_can_run,omitempty"`
	ToolMissingArgs      []string                          `json:"tool_missing_args,omitempty"`
	ToolInvalidArgs      []string                          `json:"tool_invalid_args,omitempty"`
	ToolMissingIssues    []policyruntime.ToolArgumentIssue `json:"tool_missing_issues,omitempty"`
	ToolInvalidIssues    []policyruntime.ToolArgumentIssue `json:"tool_invalid_issues,omitempty"`
	ResponseRevision     bool                              `json:"response_revision,omitempty"`
	ResponseStrict       bool                              `json:"response_strict,omitempty"`
	ExposedTools         []string                          `json:"exposed_tools,omitempty"`
	CandidateTemplates   []string                          `json:"candidate_templates,omitempty"`
	DisambiguationPrompt string                            `json:"disambiguation_prompt,omitempty"`
	BatchResults         []policyruntime.BatchResult       `json:"batch_results,omitempty"`
	PromptSetVersions    map[string]string                 `json:"prompt_set_versions,omitempty"`
	ARQResults           []string                          `json:"arq_results,omitempty"`
}

func (s *Server) resolveExecutionView(ctx context.Context, exec execution.TurnExecution, bundleID string) (resolvedPolicyResponse, error) {
	events, err := s.store.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return resolvedPolicyResponse{}, err
	}
	journeyInstances, err := s.store.ListJourneyInstances(ctx, exec.SessionID)
	if err != nil {
		return resolvedPolicyResponse{}, err
	}
	catalog, err := s.store.ListCatalogEntries(ctx)
	if err != nil {
		return resolvedPolicyResponse{}, err
	}
	bundles, err := s.store.ListBundles(ctx)
	if err != nil {
		return resolvedPolicyResponse{}, err
	}
	selected := selectBundles(bundles, bundleID, exec.PolicyBundleID)
	view, err := policyruntime.ResolveWithRouter(ctx, s.router, events, selected, journeyInstances, catalog)
	if err != nil {
		return resolvedPolicyResponse{}, err
	}
	return toResolvedPolicyResponse(exec, view), nil
}

func selectBundles(bundles []policy.Bundle, explicitID string, executionBundleID string) []policy.Bundle {
	if explicitID != "" {
		for _, bundle := range bundles {
			if bundle.ID == explicitID {
				return []policy.Bundle{bundle}
			}
		}
	}
	if executionBundleID != "" {
		for _, bundle := range bundles {
			if bundle.ID == executionBundleID {
				return []policy.Bundle{bundle}
			}
		}
	}
	if len(bundles) == 0 {
		return nil
	}
	return []policy.Bundle{bundles[0]}
}

func toResolvedPolicyResponse(exec execution.TurnExecution, view policyruntime.EngineResult) resolvedPolicyResponse {
	journeyDecision := view.JourneyProgressStage.Decision
	toolDecision := view.ToolDecisionStage.Decision
	matchedObservations := view.ObservationStage.Observations
	matchedGuidelines := view.MatchFinalizeStage.MatchedGuidelines
	resp := resolvedPolicyResponse{
		ProposalID:           exec.ProposalID,
		RolloutID:            exec.RolloutID,
		SelectionReason:      exec.SelectionReason,
		CompositionMode:      view.CompositionMode,
		NoMatch:              view.NoMatch,
		MatchedObservations:  observationIDs(matchedObservations),
		MatchedGuidelines:    guidelineIDs(matchedGuidelines),
		SuppressedGuidelines: suppressedGuidelineIDs(view.SuppressedGuidelines),
		ReapplyGuidelines:    reapplyGuidelineIDs(view.PreviouslyAppliedStage.Decisions),
		CustomerBlocked:      customerBlockedIDs(view.CustomerDependencyStage.Decisions),
		JourneyDecision:      journeyDecision.Action,
		SelectedTool:         toolDecision.SelectedTool,
		ToolCanRun:           toolDecision.CanRun,
		ToolMissingArgs:      append([]string(nil), toolDecision.MissingArguments...),
		ToolInvalidArgs:      append([]string(nil), toolDecision.InvalidArguments...),
		ToolMissingIssues:    append([]policyruntime.ToolArgumentIssue(nil), toolDecision.MissingIssues...),
		ToolInvalidIssues:    append([]policyruntime.ToolArgumentIssue(nil), toolDecision.InvalidIssues...),
		ResponseRevision:     view.ResponseAnalysisStage.Analysis.NeedsRevision,
		ResponseStrict:       view.ResponseAnalysisStage.Analysis.NeedsStrictMode,
		ExposedTools:         append([]string(nil), view.ToolExposureStage.ExposedTools...),
		CandidateTemplates:   templateIDsForAPI(view.ResponseAnalysisStage.CandidateTemplates),
		DisambiguationPrompt: view.DisambiguationPrompt,
		BatchResults:         append([]policyruntime.BatchResult(nil), view.BatchResults...),
		PromptSetVersions:    cloneStringMap(view.PromptSetVersions),
		ARQResults:           arqNames(view.ARQResults),
	}
	if view.Bundle != nil {
		resp.BundleID = view.Bundle.ID
	}
	if view.ActiveJourney != nil {
		resp.ActiveJourney = view.ActiveJourney.ID
	}
	if view.ActiveJourneyState != nil {
		resp.ActiveJourneyState = view.ActiveJourneyState.ID
	}
	return resp
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func observationIDs(items []policy.Observation) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func guidelineIDs(items []policy.Guideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func templateIDsForAPI(items []policy.Template) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func suppressedGuidelineIDs(items []policyruntime.SuppressedGuideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func arqNames(items []policyruntime.ARQResult) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func reapplyGuidelineIDs(items []policyruntime.ReapplyDecision) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.ShouldReapply {
			out = append(out, item.ID)
		}
	}
	return out
}

func customerBlockedIDs(items []policyruntime.CustomerDependencyDecision) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if len(item.MissingCustomerData) > 0 {
			out = append(out, item.ID)
		}
	}
	return out
}

func changedIDs(left, right []string) map[string][]string {
	return map[string][]string{
		"only_left":  difference(left, right),
		"only_right": difference(right, left),
	}
}

func difference(left, right []string) []string {
	rightSet := map[string]struct{}{}
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	var out []string
	for _, item := range left {
		if _, ok := rightSet[item]; ok {
			continue
		}
		out = append(out, item)
	}
	return out
}
