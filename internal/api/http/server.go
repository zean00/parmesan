package httpapi

import (
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/acp"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	knowledgecompiler "github.com/sahal/parmesan/internal/knowledge/compiler"
	knowledgeenrichment "github.com/sahal/parmesan/internal/knowledge/enrichment"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/policyyaml"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/toolsync"
)

type Server struct {
	httpServer     *http.Server
	store          store.Repository
	writes         *asyncwrite.Queue
	broker         *sse.Broker
	router         *model.Router
	syncer         *toolsync.Syncer
	sessions       *sessionsvc.Service
	listener       *sessionsvc.Listener
	operatorAPIKey string
}

const adminStreamID = "__admin__"

type streamItem struct {
	id   string
	name string
	when time.Time
	body any
}

type sessionSummary struct {
	LastTraceID           string   `json:"last_trace_id,omitempty"`
	LastExecutionID       string   `json:"last_execution_id,omitempty"`
	AppliedGuidelineIDs   []string `json:"applied_guideline_ids,omitempty"`
	ActiveJourneyID       string   `json:"active_journey_id,omitempty"`
	ActiveJourneyStateID  string   `json:"active_journey_state_id,omitempty"`
	CompositionMode       string   `json:"composition_mode,omitempty"`
	KnowledgeSnapshotID   string   `json:"knowledge_snapshot_id,omitempty"`
	RetrieverResultHashes []string `json:"retriever_result_hashes,omitempty"`
}

type sessionView struct {
	ID         string         `json:"id"`
	Channel    string         `json:"channel"`
	CustomerID string         `json:"customer_id,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	Title      string         `json:"title,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
	Summary    sessionSummary `json:"summary"`
	CreatedAt  time.Time      `json:"created_at"`
}

type traceTimelineEntry struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	When        time.Time `json:"when"`
	Payload     any       `json:"payload"`
}

type traceTimelineResponse struct {
	TraceID     string               `json:"trace_id"`
	SessionID   string               `json:"session_id,omitempty"`
	ExecutionID string               `json:"execution_id,omitempty"`
	Entries     []traceTimelineEntry `json:"entries"`
}

type mediaAssetView struct {
	ID               string         `json:"id"`
	SessionID        string         `json:"session_id"`
	EventID          string         `json:"event_id"`
	PartIndex        int            `json:"part_index"`
	Type             string         `json:"type"`
	URL              string         `json:"url,omitempty"`
	MimeType         string         `json:"mime_type,omitempty"`
	Checksum         string         `json:"checksum,omitempty"`
	Status           string         `json:"status"`
	Retention        string         `json:"retention,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	RetryCount       int            `json:"retry_count,omitempty"`
	NextRetryAt      string         `json:"next_retry_at,omitempty"`
	LastRetryAt      string         `json:"last_retry_at,omitempty"`
	EnrichmentStatus string         `json:"enrichment_status,omitempty"`
	Error            string         `json:"error,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	EnrichedAt       time.Time      `json:"enriched_at,omitempty"`
}

func New(addr string, repo store.Repository, writes *asyncwrite.Queue, broker *sse.Broker, router *model.Router, syncer *toolsync.Syncer) *Server {
	s := &Server{
		store:          repo,
		writes:         writes,
		broker:         broker,
		router:         router,
		syncer:         syncer,
		sessions:       sessionsvc.New(repo, writes),
		listener:       sessionsvc.NewListener(repo),
		operatorAPIKey: strings.TrimSpace(os.Getenv("OPERATOR_API_KEY")),
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
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /v1/sessions/{id}/events", s.listEvents)
	mux.HandleFunc("POST /v1/sessions/{id}/events", s.appendEvent)
	mux.HandleFunc("GET /v1/sessions/{id}/events/stream", s.streamEvents)
	mux.HandleFunc("POST /v1/acp/sessions", s.acpCreateSession)
	mux.HandleFunc("GET /v1/acp/sessions/{id}", s.acpGetSession)
	mux.HandleFunc("POST /v1/acp/sessions/{id}/messages", s.acpCreateMessage)
	mux.HandleFunc("GET /v1/acp/sessions/{id}/events", s.acpListEvents)
	mux.HandleFunc("POST /v1/acp/sessions/{id}/events", s.acpAppendEvent)
	mux.HandleFunc("GET /v1/acp/sessions/{id}/events/stream", s.acpStreamEvents)
	mux.HandleFunc("GET /v1/acp/sessions/{id}/approvals", s.acpListApprovals)
	mux.HandleFunc("POST /v1/acp/sessions/{id}/approvals/{approval_id}", s.acpRespondApproval)
	mux.HandleFunc("GET /v1/operator/sessions", s.operatorListSessions)
	mux.HandleFunc("GET /v1/operator/sessions/{id}", s.operatorGetSession)
	mux.HandleFunc("GET /v1/operator/sessions/{id}/events", s.operatorListEvents)
	mux.HandleFunc("GET /v1/operator/sessions/{id}/stream", s.operatorStreamEvents)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/takeover", s.operatorTakeover)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/resume", s.operatorResume)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/messages", s.operatorCreateMessage)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/messages/on-behalf-of-agent", s.operatorCreateMessageOnBehalfOfAgent)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/notes", s.operatorCreateNote)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/process", s.operatorProcessEvent)
	mux.HandleFunc("POST /v1/operator/knowledge/sources", s.operatorCreateKnowledgeSource)
	mux.HandleFunc("POST /v1/operator/knowledge/sources/{id}/compile", s.operatorCompileKnowledgeSource)
	mux.HandleFunc("GET /v1/operator/knowledge/snapshots/{id}", s.operatorGetKnowledgeSnapshot)
	mux.HandleFunc("GET /v1/operator/knowledge/pages", s.operatorListKnowledgePages)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals", s.operatorListKnowledgeProposals)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals/{id}", s.operatorGetKnowledgeProposal)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals/{id}/preview", s.operatorPreviewKnowledgeProposal)
	mux.HandleFunc("POST /v1/operator/knowledge/proposals/{id}/state", s.operatorTransitionKnowledgeProposal)
	mux.HandleFunc("POST /v1/operator/knowledge/proposals/{id}/apply", s.operatorApplyKnowledgeProposal)
	mux.HandleFunc("GET /v1/operator/media/assets", s.operatorListMediaAssets)
	mux.HandleFunc("GET /v1/operator/media/assets/{id}", s.operatorGetMediaAsset)
	mux.HandleFunc("POST /v1/operator/media/assets/{id}/reprocess", s.operatorReprocessMediaAsset)
	mux.HandleFunc("POST /v1/operator/media/assets/reprocess", s.operatorBatchReprocessMediaAssets)
	mux.HandleFunc("GET /v1/operator/media/signals", s.operatorListDerivedSignals)
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
	mux.HandleFunc("GET /v1/traces/{id}", s.getTraceTimeline)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           loggingMiddleware(s.operatorAuthMiddleware(mux)),
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

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess)))
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

	event, execID, traceID, err := s.enqueueSessionTurn(r.Context(), sessionID, req.ID, req.Source, req.Kind, req.Content, nil, nil)
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

	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) acpCreateMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		ID       string         `json:"id"`
		Source   string         `json:"source"`
		Text     string         `json:"text"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		req.Source = "customer"
	}
	content := []session.ContentPart{{Type: "text", Text: req.Text}}
	sess, err := s.store.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if sessionMode(sess) == "manual" {
		metadata := cloneMap(req.Metadata)
		metadata["automation_skipped"] = true
		metadata["automation_skip_reason"] = "manual_mode"
		event, err := s.sessions.CreateEvent(r.Context(), sessionsvc.CreateEventParams{
			ID:        req.ID,
			SessionID: sessionID,
			Source:    req.Source,
			Kind:      acp.EventKindMessage,
			Content:   content,
			Metadata:  metadata,
			Async:     true,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishSessionEvent(sessionID, event, "", event.TraceID, event.CreatedAt)
		writeJSON(w, http.StatusCreated, acp.NormalizeEvent(event))
		return
	}
	event, _, _, err := s.enqueueSessionTurn(r.Context(), sessionID, req.ID, req.Source, acp.EventKindMessage, content, nil, req.Metadata)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, acp.NormalizeEvent(event))
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
	live, cancelLive := s.liveSessionFeed(sessionID)
	defer cancelLive()

	for {
		var err error
		lastOffset, err = s.flushPersistedStream(ctx, w, flusher, sessionID, seen, lastOffset)
		if err != nil {
			return
		}
		if s.forwardLiveSessionEvents(w, flusher, live) {
			return
		}
		env, gotLive, done := s.waitForSessionActivity(ctx, sessionID, lastOffset, live)
		if done {
			return
		}
		if gotLive {
			if s.writeLiveEnvelope(w, flusher, env) {
				return
			}
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

func (s *Server) acpGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	summary := s.sessionSummaryFor(r.Context(), sess)
	out := acp.SessionFromDomain(sess)
	out.Summary = &acp.SessionSummary{
		LastTraceID:           summary.LastTraceID,
		LastExecutionID:       summary.LastExecutionID,
		AppliedGuidelineIDs:   append([]string(nil), summary.AppliedGuidelineIDs...),
		ActiveJourneyID:       summary.ActiveJourneyID,
		ActiveJourneyStateID:  summary.ActiveJourneyStateID,
		CompositionMode:       summary.CompositionMode,
		KnowledgeSnapshotID:   summary.KnowledgeSnapshotID,
		RetrieverResultHashes: append([]string(nil), summary.RetrieverResultHashes...),
	}
	writeJSON(w, http.StatusOK, out)
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
	if err := acp.ValidateEvent(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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

func (s *Server) acpStreamEvents(w http.ResponseWriter, r *http.Request) {
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
	lastOffset := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("min_offset")); raw != "" {
		if _, err := fmt.Sscan(raw, &lastOffset); err != nil {
			http.Error(w, "invalid min_offset", http.StatusBadRequest)
			return
		}
	}
	service := acp.NewService(s.sessions)
	live, cancelLive := s.liveSessionFeed(sessionID)
	defer cancelLive()
	for {
		events, scannedOffset, err := service.ListEventsPage(ctx, sessionID, lastOffset+1)
		if err != nil {
			return
		}
		if scannedOffset > lastOffset {
			lastOffset = scannedOffset
		}
		for _, event := range events {
			raw, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Kind, raw); err != nil {
				return
			}
			if event.Offset > lastOffset {
				lastOffset = event.Offset
			}
		}
		flusher.Flush()
		if s.forwardLiveSessionEvents(w, flusher, live) {
			return
		}
		env, gotLive, done := s.waitForSessionActivity(ctx, sessionID, lastOffset, live)
		if done {
			return
		}
		if gotLive {
			if s.writeLiveEnvelope(w, flusher, env) {
				return
			}
		}
	}
}

func (s *Server) acpListApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListApprovalSessions(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if statusFilter != "" && statusFilter != "all" {
		filtered := items[:0]
		for _, item := range items {
			if string(item.Status) == statusFilter {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) acpRespondApproval(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	item, err := s.resolveSessionApproval(r.Context(), sessionID, r.PathValue("approval_id"), req.Decision, "acp")
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "not found"):
			http.Error(w, err.Error(), http.StatusNotFound)
		case strings.Contains(err.Error(), "decision must be"):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) operatorListSessions(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	query := r.URL.Query()
	customerID := strings.TrimSpace(query.Get("customer_id"))
	agentID := strings.TrimSpace(query.Get("agent_id"))
	mode := strings.TrimSpace(query.Get("mode"))
	label := strings.TrimSpace(query.Get("label"))
	operatorID := strings.TrimSpace(query.Get("operator_id"))
	activeOnly := strings.EqualFold(strings.TrimSpace(query.Get("active")), "true")
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]sessionView, 0, len(items))
	for _, sess := range items {
		if customerID != "" && sess.CustomerID != customerID {
			continue
		}
		if agentID != "" && sess.AgentID != agentID {
			continue
		}
		if mode != "" && sessionMode(sess) != mode {
			continue
		}
		if label != "" && !hasLabel(sess.Labels, label) {
			continue
		}
		if operatorID != "" && stringMetadata(sess.Metadata, "assigned_operator_id") != operatorID {
			continue
		}
		if activeOnly && !(sessionMode(sess) == "manual" || stringMetadata(sess.Metadata, "assigned_operator_id") != "") {
			continue
		}
		out = append(out, sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess)))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess)))
}

func (s *Server) operatorListEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	minOffset := int64(0)
	if raw := strings.TrimSpace(query.Get("min_offset")); raw != "" {
		if _, err := fmt.Sscan(raw, &minOffset); err != nil {
			http.Error(w, "invalid min_offset", http.StatusBadRequest)
			return
		}
	}
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var kinds []string
	if kind := strings.TrimSpace(query.Get("kind")); kind != "" {
		kinds = []string{kind}
	}
	events, err := s.sessions.ListEvents(r.Context(), session.EventQuery{
		SessionID:      r.PathValue("id"),
		Source:         strings.TrimSpace(query.Get("source")),
		TraceID:        strings.TrimSpace(query.Get("trace_id")),
		Kinds:          kinds,
		MinOffset:      minOffset,
		Limit:          limit,
		ExcludeDeleted: true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) operatorStreamEvents(w http.ResponseWriter, r *http.Request) {
	s.streamEvents(w, r)
}

func (s *Server) operatorTakeover(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		OperatorID string `json:"operator_id"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := s.updateOperatorMode(r.Context(), sessionID, "manual", map[string]any{
		"assigned_operator_id": req.OperatorID,
		"handoff_reason":       req.Reason,
		"takeover_started_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	traceID := fmt.Sprintf("trace_%d", time.Now().UnixNano())
	_, _ = s.createOperatorControlEvent(r.Context(), sess.ID, "operator.takeover.started", req.OperatorID, req.Reason, traceID, nil)
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "operator.takeover.started",
		SessionID: sess.ID,
		TraceID:   traceID,
		Message:   "operator takeover started",
		Fields:    map[string]any{"operator_id": req.OperatorID, "reason": req.Reason},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess)))
}

func (s *Server) operatorResume(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		OperatorID string `json:"operator_id"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := s.store.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	sess.Mode = "auto"
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	delete(sess.Metadata, "assigned_operator_id")
	delete(sess.Metadata, "handoff_reason")
	delete(sess.Metadata, "takeover_started_at")
	sess.Metadata["takeover_ended_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.UpdateSession(r.Context(), sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	traceID := fmt.Sprintf("trace_%d", time.Now().UnixNano())
	_, _ = s.createOperatorControlEvent(r.Context(), sess.ID, "operator.takeover.ended", req.OperatorID, req.Reason, traceID, nil)
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "operator.takeover.ended",
		SessionID: sess.ID,
		TraceID:   traceID,
		Message:   "operator takeover ended",
		Fields:    map[string]any{"operator_id": req.OperatorID, "reason": req.Reason},
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess)))
}

func (s *Server) operatorCreateMessage(w http.ResponseWriter, r *http.Request) {
	s.operatorCreateVisibleMessage(w, r, "human_agent")
}

func (s *Server) operatorCreateMessageOnBehalfOfAgent(w http.ResponseWriter, r *http.Request) {
	s.operatorCreateVisibleMessage(w, r, "human_agent_on_behalf_of_ai_agent")
}

func (s *Server) operatorCreateNote(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		ID         string         `json:"id"`
		OperatorID string         `json:"operator_id"`
		Text       string         `json:"text"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	metadata := cloneMap(req.Metadata)
	metadata["operator_id"] = req.OperatorID
	metadata["internal_only"] = true
	event, err := s.sessions.CreateEvent(r.Context(), sessionsvc.CreateEventParams{
		ID:        req.ID,
		SessionID: sessionID,
		Source:    "operator",
		Kind:      "operator.note",
		Content:   []session.ContentPart{{Type: "text", Text: req.Text}},
		Metadata:  metadata,
		Async:     true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "session not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSessionEvent(sessionID, event, "", event.TraceID, event.CreatedAt)
	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) operatorProcessEvent(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.EventID) == "" {
		http.Error(w, "event_id is required", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetSession(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	event, err := s.sessions.ReadEvent(r.Context(), sessionID, req.EventID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if event.Source != "customer" || event.Kind != acp.EventKindMessage {
		http.Error(w, "event_id must reference a customer message event", http.StatusBadRequest)
		return
	}
	exec, err := s.createExecutionForEvent(r.Context(), sessionID, event.ID, event.TraceID, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, exec)
}

func (s *Server) operatorCreateVisibleMessage(w http.ResponseWriter, r *http.Request, source string) {
	sessionID := r.PathValue("id")
	var req struct {
		ID          string         `json:"id"`
		OperatorID  string         `json:"operator_id"`
		DisplayName string         `json:"display_name"`
		Text        string         `json:"text"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	metadata := cloneMap(req.Metadata)
	metadata["operator_id"] = req.OperatorID
	if strings.TrimSpace(req.DisplayName) != "" {
		metadata["display_name"] = req.DisplayName
	}
	event, err := s.sessions.CreateEvent(r.Context(), sessionsvc.CreateEventParams{
		ID:        req.ID,
		SessionID: sessionID,
		Source:    source,
		Kind:      acp.EventKindMessage,
		Content:   []session.ContentPart{{Type: "text", Text: req.Text}},
		Metadata:  metadata,
		Async:     true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "session not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSessionEvent(sessionID, event, "", event.TraceID, event.CreatedAt)
	writeJSON(w, http.StatusCreated, acp.NormalizeEvent(event))
}

func (s *Server) operatorCreateKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string         `json:"id"`
		ScopeKind string         `json:"scope_kind"`
		ScopeID   string         `json:"scope_id"`
		Kind      string         `json:"kind"`
		URI       string         `json:"uri"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ScopeKind) == "" || strings.TrimSpace(req.ScopeID) == "" || strings.TrimSpace(req.URI) == "" {
		http.Error(w, "scope_kind, scope_id, and uri are required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Kind) == "" {
		req.Kind = "folder"
	}
	if req.Kind == "folder" {
		if _, err := validatedKnowledgePath(req.URI); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	now := time.Now().UTC()
	if strings.TrimSpace(req.ID) == "" {
		req.ID = fmt.Sprintf("ksrc_%d", now.UnixNano())
	}
	source := knowledge.Source{
		ID:        req.ID,
		ScopeKind: req.ScopeKind,
		ScopeID:   req.ScopeID,
		Kind:      req.Kind,
		URI:       req.URI,
		Status:    "registered",
		Metadata:  cloneMap(req.Metadata),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveKnowledgeSource(r.Context(), source); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, source)
}

func (s *Server) operatorCompileKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.store.GetKnowledgeSource(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if source.Kind != "folder" {
		http.Error(w, "only folder knowledge sources are supported", http.StatusBadRequest)
		return
	}
	root, err := validatedKnowledgePath(source.URI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	snapshot, err := knowledgecompiler.NewWithEmbedder(s.store, s.router).CompileFolder(r.Context(), knowledgecompiler.Input{
		ScopeKind: source.ScopeKind,
		ScopeID:   source.ScopeID,
		SourceID:  source.ID,
		Root:      root,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	source.Status = "compiled"
	source.UpdatedAt = time.Now().UTC()
	_ = s.store.SaveKnowledgeSource(r.Context(), source)
	writeJSON(w, http.StatusCreated, snapshot)
}

func (s *Server) operatorGetKnowledgeSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.store.GetKnowledgeSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	pages, _ := s.store.ListKnowledgePages(r.Context(), knowledge.PageQuery{
		ScopeKind:  snapshot.ScopeKind,
		ScopeID:    snapshot.ScopeID,
		SnapshotID: snapshot.ID,
	})
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot, "pages": pages})
}

func (s *Server) operatorListKnowledgePages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pages, err := s.store.ListKnowledgePages(r.Context(), knowledge.PageQuery{
		ScopeKind:  strings.TrimSpace(query.Get("scope_kind")),
		ScopeID:    strings.TrimSpace(query.Get("scope_id")),
		SnapshotID: strings.TrimSpace(query.Get("snapshot_id")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pages)
}

func (s *Server) operatorListKnowledgeProposals(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	items, err := s.store.ListKnowledgeUpdateProposals(r.Context(), strings.TrimSpace(query.Get("scope_kind")), strings.TrimSpace(query.Get("scope_id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetKnowledgeProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetKnowledgeUpdateProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) operatorPreviewKnowledgeProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetKnowledgeUpdateProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload := map[string]any{
		"proposal": item,
	}
	if _, ok := item.Payload["page"].(map[string]any); ok {
		currentPage, proposedPage := s.proposalPages(r.Context(), item)
		var current map[string]any
		if currentPage != nil {
			current = map[string]any{
				"id":       currentPage.ID,
				"title":    currentPage.Title,
				"body":     currentPage.Body,
				"checksum": currentPage.Checksum,
			}
		}
		payload["preview"] = map[string]any{
			"current":  current,
			"proposed": proposedPage,
			"changes": map[string]any{
				"title_changed": current == nil || fmt.Sprint(current["title"]) != fmt.Sprint(proposedPage["title"]),
				"body_changed":  current == nil || fmt.Sprint(current["body"]) != fmt.Sprint(proposedPage["final_body"]),
				"conflict":      proposalConflict(currentPage, proposedPage),
			},
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) operatorTransitionKnowledgeProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetKnowledgeUpdateProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(req.State)
	switch state {
	case "draft", "approved", "rejected", "applied":
	default:
		http.Error(w, "state must be one of draft, approved, rejected, applied", http.StatusBadRequest)
		return
	}
	if item.State == "applied" && state != "applied" {
		http.Error(w, "applied proposal state is immutable", http.StatusBadRequest)
		return
	}
	item.State = state
	item.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveKnowledgeUpdateProposal(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) operatorApplyKnowledgeProposal(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetKnowledgeUpdateProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if item.State == "applied" {
		writeJSON(w, http.StatusOK, item)
		return
	}
	if item.State == "rejected" {
		http.Error(w, "rejected proposal cannot be applied", http.StatusBadRequest)
		return
	}
	if item.State != "approved" && item.State != "draft" {
		http.Error(w, "proposal must be draft or approved before apply", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if _, ok := item.Payload["page"].(map[string]any); ok {
		currentPage, proposedPage := s.proposalPages(r.Context(), item)
		if proposalConflict(currentPage, proposedPage) {
			http.Error(w, "proposal is stale relative to current page checksum", http.StatusConflict)
			return
		}
		page := knowledge.Page{
			ID:        strings.TrimSpace(fmt.Sprint(proposedPage["id"])),
			ScopeKind: item.ScopeKind,
			ScopeID:   item.ScopeID,
			Title:     strings.TrimSpace(fmt.Sprint(proposedPage["title"])),
			Body:      strings.TrimSpace(fmt.Sprint(proposedPage["final_body"])),
			PageType:  "proposal_applied",
			Citations: append([]knowledge.Citation(nil), item.Evidence...),
			Metadata:  map[string]any{"proposal_id": item.ID},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if currentPage != nil {
			page.ID = currentPage.ID
			page.SourceID = currentPage.SourceID
			page.PageType = currentPage.PageType
			page.CreatedAt = currentPage.CreatedAt
			page.Citations = mergeCitations(currentPage.Citations, page.Citations)
			page.Metadata = mergeMaps(currentPage.Metadata, page.Metadata)
		}
		if strings.TrimSpace(page.ID) == "" {
			page.ID = fmt.Sprintf("kpage_%d", now.UnixNano())
		}
		page.Checksum = knowledgeChecksum(page.Body)
		chunk := knowledge.Chunk{
			ID:        fmt.Sprintf("kchunk_%d", now.UnixNano()),
			PageID:    page.ID,
			ScopeKind: page.ScopeKind,
			ScopeID:   page.ScopeID,
			Text:      page.Body,
			Citations: append([]knowledge.Citation(nil), page.Citations...),
			Metadata:  map[string]any{"page_title": page.Title},
			CreatedAt: now,
		}
		if err := s.store.SaveKnowledgePage(r.Context(), page, []knowledge.Chunk{chunk}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pages, err := s.store.ListKnowledgePages(r.Context(), knowledge.PageQuery{ScopeKind: item.ScopeKind, ScopeID: item.ScopeID, Limit: 1000})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		chunks, err := s.store.ListKnowledgeChunks(r.Context(), knowledge.ChunkQuery{ScopeKind: item.ScopeKind, ScopeID: item.ScopeID, Limit: 1000})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pageIDs := make([]string, 0, len(pages))
		chunkIDs := make([]string, 0, len(chunks))
		for _, page := range pages {
			pageIDs = append(pageIDs, page.ID)
		}
		for _, chunk := range chunks {
			chunkIDs = append(chunkIDs, chunk.ID)
		}
		if err := s.store.SaveKnowledgeSnapshot(r.Context(), knowledge.Snapshot{
			ID:        fmt.Sprintf("ksnap_%d", now.UnixNano()),
			ScopeKind: item.ScopeKind,
			ScopeID:   item.ScopeID,
			PageIDs:   pageIDs,
			ChunkIDs:  chunkIDs,
			Metadata:  map[string]any{"proposal_id": item.ID, "source": "proposal_apply"},
			CreatedAt: now,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	item.State = "applied"
	item.UpdatedAt = now
	if err := s.store.SaveKnowledgeUpdateProposal(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) proposalPages(ctx context.Context, item knowledge.UpdateProposal) (*knowledge.Page, map[string]any) {
	payloadPage, ok := item.Payload["page"].(map[string]any)
	if !ok {
		return nil, nil
	}
	proposed := map[string]any{
		"id":            strings.TrimSpace(fmt.Sprint(payloadPage["id"])),
		"title":         strings.TrimSpace(fmt.Sprint(payloadPage["title"])),
		"body":          strings.TrimSpace(fmt.Sprint(payloadPage["body"])),
		"base_checksum": strings.TrimSpace(fmt.Sprint(payloadPage["base_checksum"])),
		"operation":     normalizeProposalOperation(fmt.Sprint(payloadPage["operation"])),
	}
	pages, _ := s.store.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: item.ScopeKind, ScopeID: item.ScopeID, Limit: 1000})
	for _, page := range pages {
		if proposed["id"] != "" && page.ID == proposed["id"] {
			proposed["final_body"] = mergeProposalBody(page.Body, fmt.Sprint(proposed["body"]), fmt.Sprint(proposed["operation"]))
			return &page, proposed
		}
	}
	if proposed["title"] != "" {
		for _, page := range pages {
			if strings.EqualFold(strings.TrimSpace(page.Title), fmt.Sprint(proposed["title"])) {
				proposed["id"] = page.ID
				proposed["final_body"] = mergeProposalBody(page.Body, fmt.Sprint(proposed["body"]), fmt.Sprint(proposed["operation"]))
				return &page, proposed
			}
		}
	}
	proposed["final_body"] = strings.TrimSpace(fmt.Sprint(proposed["body"]))
	return nil, proposed
}

func proposalConflict(current *knowledge.Page, proposed map[string]any) bool {
	if current == nil || proposed == nil {
		return false
	}
	baseChecksum := strings.TrimSpace(fmt.Sprint(proposed["base_checksum"]))
	return baseChecksum != "" && current.Checksum != "" && current.Checksum != baseChecksum
}

func mergeMaps(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func mergeCitations(base []knowledge.Citation, extra []knowledge.Citation) []knowledge.Citation {
	seen := map[string]struct{}{}
	var out []knowledge.Citation
	for _, item := range append(append([]knowledge.Citation(nil), base...), extra...) {
		key := item.SourceID + "\x00" + item.URI + "\x00" + item.Title + "\x00" + item.Anchor
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func sortedSet(items map[string]struct{}) []string {
	out := make([]string, 0, len(items))
	for item := range items {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func knowledgeChecksum(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func normalizeProposalOperation(v string) string {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "append":
		return "append"
	case "prepend":
		return "prepend"
	default:
		return "replace"
	}
}

func mergeProposalBody(current string, proposed string, operation string) string {
	current = strings.TrimSpace(current)
	proposed = strings.TrimSpace(proposed)
	switch normalizeProposalOperation(operation) {
	case "append":
		if current == "" {
			return proposed
		}
		if proposed == "" {
			return current
		}
		return current + "\n\n" + proposed
	case "prepend":
		if current == "" {
			return proposed
		}
		if proposed == "" {
			return current
		}
		return proposed + "\n\n" + current
	default:
		return proposed
	}
}

func (s *Server) operatorListMediaAssets(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	items, err := s.store.ListMediaAssets(r.Context(), strings.TrimSpace(query.Get("session_id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := strings.TrimSpace(query.Get("status"))
	partType := strings.TrimSpace(query.Get("type"))
	if status != "" || partType != "" {
		filtered := items[:0]
		for _, item := range items {
			if status != "" && item.Status != status {
				continue
			}
			if partType != "" && item.Type != partType {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	views := make([]mediaAssetView, 0, len(items))
	for _, item := range items {
		views = append(views, mediaAssetToView(item))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) operatorGetMediaAsset(w http.ResponseWriter, r *http.Request) {
	assetID := strings.TrimSpace(r.PathValue("id"))
	if assetID == "" {
		http.Error(w, "asset id is required", http.StatusBadRequest)
		return
	}
	asset, err := s.findMediaAsset(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")), assetID)
	if asset == nil {
		http.Error(w, "media asset not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	signals, err := s.store.ListDerivedSignals(r.Context(), asset.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var filtered []media.DerivedSignal
	for _, signal := range signals {
		if signal.AssetID == asset.ID {
			filtered = append(filtered, signal)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset":   mediaAssetToView(*asset),
		"signals": filtered,
	})
}

func (s *Server) operatorReprocessMediaAsset(w http.ResponseWriter, r *http.Request) {
	assetID := strings.TrimSpace(r.PathValue("id"))
	if assetID == "" {
		http.Error(w, "asset id is required", http.StatusBadRequest)
		return
	}
	asset, err := s.findMediaAsset(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")), assetID)
	if asset == nil {
		http.Error(w, "media asset not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	event, err := s.sessions.ReadEvent(r.Context(), asset.SessionID, asset.EventID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if asset.PartIndex < 0 || asset.PartIndex >= len(event.Content) {
		http.Error(w, "media part not found", http.StatusNotFound)
		return
	}
	part := event.Content[asset.PartIndex]
	asset.Status = "pending"
	asset.Metadata = mergeMaps(asset.Metadata, map[string]any{
		"reprocess_requested_at": time.Now().UTC().Format(time.RFC3339Nano),
		"enrichment_status":      "pending",
	})
	if err := s.store.SaveMediaAsset(r.Context(), *asset); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	signals, statusCode, err := s.reprocessAsset(r.Context(), asset, event, part)
	if err != nil {
		http.Error(w, err.Error(), statusCode)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset": mediaAssetToView(*asset), "signals": signals})
}

func (s *Server) operatorBatchReprocessMediaAssets(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sessionID := strings.TrimSpace(query.Get("session_id"))
	statusFilter := strings.TrimSpace(query.Get("status"))
	typeFilter := strings.TrimSpace(query.Get("type"))
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if limit <= 0 {
		limit = 50
	}
	items, err := s.store.ListMediaAssets(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type result struct {
		AssetID          string `json:"asset_id"`
		Status           string `json:"status"`
		RetryCount       int    `json:"retry_count,omitempty"`
		NextRetryAt      string `json:"next_retry_at,omitempty"`
		LastRetryAt      string `json:"last_retry_at,omitempty"`
		EnrichmentStatus string `json:"enrichment_status,omitempty"`
		Error            string `json:"error,omitempty"`
	}
	results := make([]result, 0, limit)
	for _, item := range items {
		if len(results) >= limit {
			break
		}
		if statusFilter != "" && item.Status != statusFilter {
			continue
		}
		if typeFilter != "" && item.Type != typeFilter {
			continue
		}
		asset := item
		event, err := s.sessions.ReadEvent(r.Context(), asset.SessionID, asset.EventID)
		if err != nil {
			view := mediaAssetToView(asset)
			results = append(results, result{AssetID: asset.ID, Status: "not_found", RetryCount: view.RetryCount, NextRetryAt: view.NextRetryAt, LastRetryAt: view.LastRetryAt, EnrichmentStatus: view.EnrichmentStatus, Error: err.Error()})
			continue
		}
		if asset.PartIndex < 0 || asset.PartIndex >= len(event.Content) {
			view := mediaAssetToView(asset)
			results = append(results, result{AssetID: asset.ID, Status: "not_found", RetryCount: view.RetryCount, NextRetryAt: view.NextRetryAt, LastRetryAt: view.LastRetryAt, EnrichmentStatus: view.EnrichmentStatus, Error: "media part not found"})
			continue
		}
		_, _, err = s.reprocessAsset(r.Context(), &asset, event, event.Content[asset.PartIndex])
		view := mediaAssetToView(asset)
		if err != nil {
			results = append(results, result{AssetID: asset.ID, Status: asset.Status, RetryCount: view.RetryCount, NextRetryAt: view.NextRetryAt, LastRetryAt: view.LastRetryAt, EnrichmentStatus: view.EnrichmentStatus, Error: err.Error()})
			continue
		}
		results = append(results, result{AssetID: asset.ID, Status: asset.Status, RetryCount: view.RetryCount, NextRetryAt: view.NextRetryAt, LastRetryAt: view.LastRetryAt, EnrichmentStatus: view.EnrichmentStatus})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func mediaAssetToView(asset media.Asset) mediaAssetView {
	view := mediaAssetView{
		ID:         asset.ID,
		SessionID:  asset.SessionID,
		EventID:    asset.EventID,
		PartIndex:  asset.PartIndex,
		Type:       asset.Type,
		URL:        asset.URL,
		MimeType:   asset.MimeType,
		Checksum:   asset.Checksum,
		Status:     asset.Status,
		Retention:  asset.Retention,
		Metadata:   asset.Metadata,
		CreatedAt:  asset.CreatedAt,
		EnrichedAt: asset.EnrichedAt,
	}
	if asset.Metadata == nil {
		return view
	}
	view.RetryCount = mediaRetryCount(asset.Metadata)
	view.NextRetryAt = strings.TrimSpace(fmt.Sprint(asset.Metadata["next_retry_at"]))
	view.LastRetryAt = strings.TrimSpace(fmt.Sprint(asset.Metadata["last_retry_at"]))
	view.EnrichmentStatus = strings.TrimSpace(fmt.Sprint(asset.Metadata["enrichment_status"]))
	view.Error = strings.TrimSpace(fmt.Sprint(asset.Metadata["error"]))
	return view
}

func mediaRetryCount(metadata map[string]any) int {
	switch v := metadata["retry_count"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return 0
}

func mediaAssetTraceID(event session.Event, asset *media.Asset) string {
	if traceID := strings.TrimSpace(event.TraceID); traceID != "" {
		return traceID
	}
	sum := sha1.Sum([]byte(asset.EventID + ":" + asset.SessionID))
	return "trace_" + hex.EncodeToString(sum[:8])
}

func (s *Server) reprocessAsset(ctx context.Context, asset *media.Asset, event session.Event, part session.ContentPart) ([]media.DerivedSignal, int, error) {
	traceID := mediaAssetTraceID(event, asset)
	signals, err := knowledgeenrichment.ForPart(asset.Type).Enrich(ctx, event, *asset, part)
	if err != nil {
		asset.Status = "failed"
		asset.EnrichedAt = time.Now().UTC()
		asset.Metadata = mergeMaps(asset.Metadata, map[string]any{
			"error":             err.Error(),
			"enrichment_status": "failed",
			"reprocessed_at":    time.Now().UTC().Format(time.RFC3339Nano),
		})
		if saveErr := s.store.SaveMediaAsset(ctx, *asset); saveErr != nil {
			return nil, http.StatusInternalServerError, saveErr
		}
		s.appendTrace(ctx, audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "media.reprocess.failed",
			SessionID: asset.SessionID,
			TraceID:   traceID,
			Message:   "media reprocess failed",
			Fields: map[string]any{
				"asset_id":          asset.ID,
				"event_id":          asset.EventID,
				"part_index":        asset.PartIndex,
				"type":              asset.Type,
				"error":             err.Error(),
				"enrichment_status": asset.Metadata["enrichment_status"],
			},
			CreatedAt: time.Now().UTC(),
		})
		return nil, http.StatusBadGateway, err
	}
	extractors := map[string]struct{}{}
	providers := map[string]struct{}{}
	requestIDs := map[string]struct{}{}
	var maxLatency int64
	for _, signal := range signals {
		if strings.TrimSpace(signal.Extractor) != "" {
			extractors[signal.Extractor] = struct{}{}
		}
		if provider := strings.TrimSpace(fmt.Sprint(signal.Metadata["provider"])); provider != "" {
			providers[provider] = struct{}{}
		}
		if requestID := strings.TrimSpace(fmt.Sprint(signal.Metadata["request_id"])); requestID != "" {
			requestIDs[requestID] = struct{}{}
		}
		if latency, ok := signal.Metadata["latency_ms"]; ok {
			switch typed := latency.(type) {
			case int64:
				if typed > maxLatency {
					maxLatency = typed
				}
			case int:
				if int64(typed) > maxLatency {
					maxLatency = int64(typed)
				}
			case float64:
				if int64(typed) > maxLatency {
					maxLatency = int64(typed)
				}
			}
		}
		if err := s.store.SaveDerivedSignal(ctx, signal); err != nil {
			return nil, http.StatusInternalServerError, err
		}
	}
	asset.Status = "succeeded"
	asset.EnrichedAt = time.Now().UTC()
	meta := mergeMaps(asset.Metadata, map[string]any{
		"enrichment_status": "succeeded",
		"reprocessed_at":    time.Now().UTC().Format(time.RFC3339Nano),
	})
	if len(extractors) > 0 {
		meta["extractors"] = sortedSet(extractors)
	}
	if len(providers) > 0 {
		meta["providers"] = sortedSet(providers)
	}
	if len(requestIDs) > 0 {
		meta["request_ids"] = sortedSet(requestIDs)
	}
	if maxLatency > 0 {
		meta["latency_ms"] = maxLatency
	}
	delete(meta, "error")
	asset.Metadata = meta
	if err := s.store.SaveMediaAsset(ctx, *asset); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	s.appendTrace(ctx, audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "media.reprocess.succeeded",
		SessionID: asset.SessionID,
		TraceID:   traceID,
		Message:   "media reprocess succeeded",
		Fields: map[string]any{
			"asset_id":          asset.ID,
			"event_id":          asset.EventID,
			"part_index":        asset.PartIndex,
			"type":              asset.Type,
			"signal_count":      len(signals),
			"enrichment_status": asset.Metadata["enrichment_status"],
			"providers":         asset.Metadata["providers"],
			"request_ids":       asset.Metadata["request_ids"],
			"latency_ms":        asset.Metadata["latency_ms"],
		},
		CreatedAt: time.Now().UTC(),
	})
	return signals, http.StatusOK, nil
}

func (s *Server) findMediaAsset(ctx context.Context, sessionID string, assetID string) (*media.Asset, error) {
	items, err := s.store.ListMediaAssets(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == assetID {
			copy := item
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Server) operatorListDerivedSignals(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDerivedSignals(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func validatedKnowledgePath(uri string) (string, error) {
	root := strings.TrimSpace(os.Getenv("KNOWLEDGE_SOURCE_ROOT"))
	if root == "" {
		return "", fmt.Errorf("KNOWLEDGE_SOURCE_ROOT is required for folder knowledge sources")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(uri)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return "", err
	}
	if rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
		return pathAbs, nil
	}
	return "", fmt.Errorf("knowledge source path must be under KNOWLEDGE_SOURCE_ROOT")
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
	query := r.URL.Query()
	traceID := strings.TrimSpace(query.Get("trace_id"))
	sessionID := strings.TrimSpace(query.Get("session_id"))
	executionID := strings.TrimSpace(query.Get("execution_id"))
	kind := strings.TrimSpace(query.Get("kind"))
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]audit.Record, 0, len(records))
	for _, record := range records {
		if traceID != "" && record.TraceID != traceID {
			continue
		}
		if sessionID != "" && record.SessionID != sessionID {
			continue
		}
		if executionID != "" && record.ExecutionID != executionID {
			continue
		}
		if kind != "" && record.Kind != kind {
			continue
		}
		out = append(out, record)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTraceTimeline(w http.ResponseWriter, r *http.Request) {
	resp, err := s.buildTraceTimeline(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, resp)
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

func (s *Server) operatorAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/operator/") && r.URL.Path != "/v1/operator" {
			next.ServeHTTP(w, r)
			return
		}
		if s.operatorAPIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !operatorTokenMatches(r, s.operatorAPIKey) {
			http.Error(w, "operator authorization required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func operatorTokenMatches(r *http.Request, want string) bool {
	got := strings.TrimSpace(r.Header.Get("X-Operator-Token"))
	if got == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			got = strings.TrimSpace(auth[len("Bearer "):])
		}
	}
	if got == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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

func (s *Server) enqueueSessionTurn(ctx context.Context, sessionID, eventID, source, kind string, content []session.ContentPart, data, metadata map[string]any) (session.Event, string, string, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(eventID) == "" {
		eventID = fmt.Sprintf("evt_%d", now.UnixNano())
	}
	traceID := fmt.Sprintf("trace_%d", now.UnixNano())
	exec, err := s.createExecutionForEvent(ctx, sessionID, eventID, traceID, now)
	if err != nil {
		return session.Event{}, "", "", err
	}
	event, err := s.sessions.CreateEvent(ctx, sessionsvc.CreateEventParams{
		ID:          eventID,
		SessionID:   sessionID,
		Source:      source,
		Kind:        kind,
		Content:     content,
		Data:        data,
		Metadata:    metadata,
		ExecutionID: exec.ID,
		TraceID:     traceID,
		CreatedAt:   now,
		Async:       true,
	})
	if err != nil {
		return session.Event{}, "", "", err
	}
	s.publishSessionEvent(sessionID, event, exec.ID, traceID, now)
	return event, exec.ID, traceID, nil
}

func (s *Server) createExecutionForEvent(ctx context.Context, sessionID, eventID, traceID string, now time.Time) (execution.TurnExecution, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(traceID) == "" {
		traceID = fmt.Sprintf("trace_%d", now.UnixNano())
	}
	execID := fmt.Sprintf("exec_%d", now.UnixNano())
	exec := execution.TurnExecution{
		ID:             execID,
		SessionID:      sessionID,
		TriggerEventID: eventID,
		TraceID:        traceID,
		Status:         execution.StatusRunning,
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
	if err := s.writes.CreateExecution(ctx, exec, steps); err != nil {
		return execution.TurnExecution{}, err
	}
	return exec, nil
}

func (s *Server) publishSessionEvent(sessionID string, event session.Event, executionID, traceID string, createdAt time.Time) {
	if s.broker == nil {
		return
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	s.broker.Publish(sessionID, sse.Envelope{
		EventID:     event.ID,
		SessionID:   sessionID,
		ExecutionID: executionID,
		TraceID:     traceID,
		Type:        "session.event.created",
		Payload:     event,
		CreatedAt:   createdAt,
	})
}

func (s *Server) updateOperatorMode(ctx context.Context, sessionID, mode string, metadata map[string]any) (session.Session, error) {
	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	sess.Mode = mode
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	for key, value := range metadata {
		if strings.TrimSpace(key) != "" {
			sess.Metadata[key] = value
		}
	}
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		return session.Session{}, err
	}
	return sess, nil
}

func (s *Server) createOperatorControlEvent(ctx context.Context, sessionID, kind, operatorID, reason, traceID string, metadata map[string]any) (session.Event, error) {
	if strings.TrimSpace(traceID) == "" {
		traceID = fmt.Sprintf("trace_%d", time.Now().UnixNano())
	}
	payload := cloneMap(metadata)
	payload["operator_id"] = operatorID
	payload["reason"] = reason
	payload["internal_only"] = true
	event, err := s.sessions.CreateEvent(ctx, sessionsvc.CreateEventParams{
		SessionID: sessionID,
		Source:    "operator",
		Kind:      kind,
		TraceID:   traceID,
		Data: map[string]any{
			"operator_id": operatorID,
			"reason":      reason,
		},
		Metadata: payload,
		Async:    true,
	})
	if err != nil {
		return session.Event{}, err
	}
	s.publishSessionEvent(sessionID, event, "", event.TraceID, event.CreatedAt)
	return event, nil
}

func sessionMode(sess session.Session) string {
	mode := strings.ToLower(strings.TrimSpace(sess.Mode))
	if mode == "" {
		return "auto"
	}
	return mode
}

func hasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

func positiveQueryInt(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func cloneMap(src map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range src {
		out[key] = value
	}
	return out
}

func (s *Server) resolveSessionApproval(ctx context.Context, sessionID, approvalID, decision, source string) (approval.Session, error) {
	item, err := s.store.GetApprovalSession(ctx, approvalID)
	if err != nil || item.SessionID != sessionID {
		return approval.Session{}, fmt.Errorf("approval session not found")
	}
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approve" && decision != "reject" {
		return approval.Session{}, fmt.Errorf("decision must be approve or reject")
	}
	item.Decision = decision
	item.UpdatedAt = time.Now().UTC()
	if decision == "approve" {
		item.Status = approval.StatusApproved
	} else {
		item.Status = approval.StatusRejected
	}
	if err := s.store.SaveApprovalSession(ctx, item); err != nil {
		return approval.Session{}, err
	}
	execs, err := s.store.ListExecutions(ctx)
	traceID := ""
	if err == nil {
		for _, exec := range execs {
			if exec.ID != item.ExecutionID {
				continue
			}
			traceID = exec.TraceID
			if exec.Status == execution.StatusBlocked {
				exec.Status = execution.StatusPending
				exec.UpdatedAt = time.Now().UTC()
				_ = s.store.UpdateExecution(ctx, exec)
			}
			break
		}
	}
	if _, err := s.sessions.CreateApprovalResolvedEvent(ctx, sessionID, source, item.ExecutionID, traceID, item.ID, item.ToolID, decision, nil, true); err != nil {
		return approval.Session{}, err
	}
	s.appendTrace(ctx, audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        "approval.resolved",
		SessionID:   sessionID,
		ExecutionID: item.ExecutionID,
		TraceID:     traceID,
		Message:     "approval resolved via API",
		Fields:      map[string]any{"approval_id": item.ID, "decision": decision, "source": source},
		CreatedAt:   time.Now().UTC(),
	})
	return item, nil
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

func (s *Server) sessionSummaryFor(ctx context.Context, sess session.Session) sessionSummary {
	summary := sessionSummary{}
	if sess.Metadata != nil {
		summary.LastTraceID = stringMetadata(sess.Metadata, "last_trace_id")
		summary.AppliedGuidelineIDs = stringSliceMetadata(sess.Metadata, "applied_guideline_ids")
		summary.ActiveJourneyID = stringMetadata(sess.Metadata, "active_journey_id")
		summary.ActiveJourneyStateID = stringMetadata(sess.Metadata, "active_journey_state_id")
		summary.CompositionMode = stringMetadata(sess.Metadata, "composition_mode")
		summary.KnowledgeSnapshotID = stringMetadata(sess.Metadata, "knowledge_snapshot_id")
		summary.RetrieverResultHashes = stringSliceMetadata(sess.Metadata, "retriever_result_hashes")
	}
	execs, err := s.store.ListExecutions(ctx)
	if err == nil {
		var latest execution.TurnExecution
		var matched execution.TurnExecution
		for _, exec := range execs {
			if exec.SessionID != sess.ID {
				continue
			}
			if latest.ID == "" || exec.UpdatedAt.After(latest.UpdatedAt) {
				latest = exec
			}
			if summary.LastTraceID != "" && exec.TraceID == summary.LastTraceID {
				if matched.ID == "" || exec.UpdatedAt.After(matched.UpdatedAt) {
					matched = exec
				}
			}
		}
		if matched.ID != "" {
			summary.LastExecutionID = matched.ID
		} else {
			summary.LastExecutionID = latest.ID
		}
	}
	return summary
}

func sessionViewFromDomain(sess session.Session, summary sessionSummary) sessionView {
	return sessionView{
		ID:         sess.ID,
		Channel:    sess.Channel,
		CustomerID: sess.CustomerID,
		AgentID:    sess.AgentID,
		Mode:       sess.Mode,
		Title:      sess.Title,
		Metadata:   sess.Metadata,
		Labels:     append([]string(nil), sess.Labels...),
		Summary:    summary,
		CreatedAt:  sess.CreatedAt,
	}
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSliceMetadata(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[key]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (s *Server) liveSessionFeed(sessionID string) (chan sse.Envelope, func()) {
	if s.broker == nil {
		return nil, func() {}
	}
	ch := make(chan sse.Envelope, 16)
	cancel := s.broker.Subscribe(sessionID, ch)
	return ch, cancel
}

func (s *Server) forwardLiveSessionEvents(w http.ResponseWriter, flusher http.Flusher, ch chan sse.Envelope) bool {
	if ch == nil {
		return false
	}
	wrote := false
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return true
			}
			if env.Type != "runtime.response.delta" && env.Type != "runtime.response.completed" {
				continue
			}
			raw, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", env.EventID, strings.ReplaceAll(env.Type, "runtime.", ""), raw); err != nil {
				return true
			}
			wrote = true
		default:
			if wrote {
				flusher.Flush()
			}
			return false
		}
	}
}

func (s *Server) waitForSessionActivity(ctx context.Context, sessionID string, lastOffset int64, live chan sse.Envelope) (sse.Envelope, bool, bool) {
	ready := make(chan struct{}, 1)
	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		ok, _ := s.listener.WaitForMoreEvents(waitCtx, session.EventQuery{
			SessionID:      sessionID,
			MinOffset:      lastOffset + 1,
			ExcludeDeleted: true,
		})
		if ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return sse.Envelope{}, false, true
	case env, ok := <-live:
		if !ok {
			return sse.Envelope{}, false, false
		}
		return env, true, false
	case <-ready:
		return sse.Envelope{}, false, false
	case <-time.After(500 * time.Millisecond):
		return sse.Envelope{}, false, false
	}
}

func (s *Server) writeLiveEnvelope(w http.ResponseWriter, flusher http.Flusher, env sse.Envelope) bool {
	if env.Type != "runtime.response.delta" && env.Type != "runtime.response.completed" {
		return false
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", env.EventID, strings.ReplaceAll(env.Type, "runtime.", ""), raw); err != nil {
		return true
	}
	flusher.Flush()
	return false
}

func (s *Server) buildTraceTimeline(ctx context.Context, traceID string) (traceTimelineResponse, error) {
	records, err := s.store.ListAuditRecords(ctx)
	if err != nil {
		return traceTimelineResponse{}, err
	}
	execs, err := s.store.ListExecutions(ctx)
	if err != nil {
		return traceTimelineResponse{}, err
	}
	var targetExec execution.TurnExecution
	var sessionID string
	var entries []traceTimelineEntry
	for _, record := range records {
		if record.TraceID != traceID {
			continue
		}
		if sessionID == "" {
			sessionID = record.SessionID
		}
		if targetExec.ID == "" && record.ExecutionID != "" {
			targetExec.ID = record.ExecutionID
		}
		kind := "audit." + record.Kind
		payload := any(record)
		if strings.HasPrefix(record.Kind, "media.") {
			kind = record.Kind
			payload = s.mediaAuditTimelinePayload(ctx, record)
		}
		entries = append(entries, traceTimelineEntry{
			Kind:        kind,
			ID:          record.ID,
			SessionID:   record.SessionID,
			ExecutionID: record.ExecutionID,
			TraceID:     record.TraceID,
			When:        record.CreatedAt,
			Payload:     payload,
		})
	}
	for _, exec := range execs {
		if exec.TraceID != traceID {
			continue
		}
		if targetExec.ID == "" {
			targetExec = exec
		}
		if sessionID == "" {
			sessionID = exec.SessionID
		}
		entries = append(entries, traceTimelineEntry{
			Kind:        "execution",
			ID:          exec.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			When:        exec.CreatedAt,
			Payload:     exec,
		})
		_, steps, err := s.store.GetExecution(ctx, exec.ID)
		if err == nil {
			for _, step := range steps {
				when := step.UpdatedAt
				if when.IsZero() {
					when = step.CreatedAt
				}
				entries = append(entries, traceTimelineEntry{
					Kind:        "execution.step",
					ID:          step.ID,
					SessionID:   exec.SessionID,
					ExecutionID: exec.ID,
					TraceID:     exec.TraceID,
					When:        when,
					Payload:     step,
				})
			}
		}
	}
	if sessionID != "" {
		events, err := s.store.ListEventsFiltered(ctx, session.EventQuery{SessionID: sessionID, TraceID: traceID})
		if err == nil {
			for _, event := range events {
				entryKind := "session.event"
				if event.Source == "operator" || strings.HasPrefix(event.Kind, "operator.") {
					entryKind = event.Kind
				}
				entries = append(entries, traceTimelineEntry{
					Kind:        entryKind,
					ID:          event.ID,
					SessionID:   event.SessionID,
					ExecutionID: event.ExecutionID,
					TraceID:     event.TraceID,
					When:        event.CreatedAt,
					Payload:     acp.NormalizeEvent(event),
				})
			}
		}
		approvals, err := s.store.ListApprovalSessions(ctx, sessionID)
		if err == nil {
			for _, item := range approvals {
				if targetExec.ID != "" && item.ExecutionID != targetExec.ID {
					continue
				}
				entries = append(entries, traceTimelineEntry{
					Kind:        "approval",
					ID:          item.ID,
					SessionID:   item.SessionID,
					ExecutionID: item.ExecutionID,
					TraceID:     traceID,
					When:        item.CreatedAt,
					Payload:     item,
				})
			}
		}
	}
	if targetExec.ID != "" {
		runs, err := s.store.ListToolRuns(ctx, targetExec.ID)
		if err == nil {
			for _, run := range runs {
				entries = append(entries, traceTimelineEntry{
					Kind:        "tool.run",
					ID:          run.ID,
					SessionID:   sessionID,
					ExecutionID: run.ExecutionID,
					TraceID:     traceID,
					When:        run.CreatedAt,
					Payload:     run,
				})
			}
		}
		deliveries, err := s.store.ListDeliveryAttempts(ctx, targetExec.ID)
		if err == nil {
			for _, attempt := range deliveries {
				entries = append(entries, traceTimelineEntry{
					Kind:        "delivery.attempt",
					ID:          attempt.ID,
					SessionID:   attempt.SessionID,
					ExecutionID: attempt.ExecutionID,
					TraceID:     traceID,
					When:        attempt.CreatedAt,
					Payload:     attempt,
				})
			}
		}
	}
	if len(entries) == 0 {
		return traceTimelineResponse{}, fmt.Errorf("trace not found")
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].When.Equal(entries[j].When) {
			if entries[i].Kind == entries[j].Kind {
				return entries[i].ID < entries[j].ID
			}
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].When.Before(entries[j].When)
	})
	return traceTimelineResponse{
		TraceID:     traceID,
		SessionID:   sessionID,
		ExecutionID: targetExec.ID,
		Entries:     entries,
	}, nil
}

func (s *Server) mediaAuditTimelinePayload(ctx context.Context, record audit.Record) map[string]any {
	payload := map[string]any{
		"audit": record,
	}
	assetID := strings.TrimSpace(fmt.Sprint(record.Fields["asset_id"]))
	if assetID == "" {
		return payload
	}
	asset, err := s.findMediaAsset(ctx, record.SessionID, assetID)
	if err != nil || asset == nil {
		return payload
	}
	payload["asset"] = mediaAssetToView(*asset)
	signals, err := s.store.ListDerivedSignals(ctx, asset.SessionID)
	if err != nil {
		return payload
	}
	var filtered []media.DerivedSignal
	for _, signal := range signals {
		if signal.AssetID == asset.ID {
			filtered = append(filtered, signal)
		}
	}
	if len(filtered) > 0 {
		payload["signals"] = filtered
	}
	return payload
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
