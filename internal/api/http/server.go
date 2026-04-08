package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	operatordomain "github.com/sahal/parmesan/internal/domain/operator"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	knowledgecompiler "github.com/sahal/parmesan/internal/knowledge/compiler"
	knowledgeenrichment "github.com/sahal/parmesan/internal/knowledge/enrichment"
	knowledgelearning "github.com/sahal/parmesan/internal/knowledge/learning"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/policyyaml"
	"github.com/sahal/parmesan/internal/quality"
	rolloutengine "github.com/sahal/parmesan/internal/rollout"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/toolsync"
)

type Server struct {
	httpServer                 *http.Server
	store                      store.Repository
	writes                     *asyncwrite.Queue
	broker                     *sse.Broker
	router                     *model.Router
	syncer                     *toolsync.Syncer
	sessions                   *sessionsvc.Service
	listener                   *sessionsvc.Listener
	operatorAPIKey             string
	trustedOperatorIDHeader    string
	trustedOperatorRolesHeader string
}

const adminStreamID = "__admin__"

type operatorContextKey struct{}

type operatorPrincipal struct {
	ID    string
	Roles []string
}

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
	SoulHash              string   `json:"soul_hash,omitempty"`
	PreferenceHash        string   `json:"preference_hash,omitempty"`
	RetrieverResultHashes []string `json:"retriever_result_hashes,omitempty"`
}

type sessionView struct {
	ID                     string         `json:"id"`
	Channel                string         `json:"channel"`
	CustomerID             string         `json:"customer_id,omitempty"`
	AgentID                string         `json:"agent_id,omitempty"`
	Mode                   string         `json:"mode,omitempty"`
	Title                  string         `json:"title,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
	Labels                 []string       `json:"labels,omitempty"`
	Summary                sessionSummary `json:"summary"`
	CreatedAt              time.Time      `json:"created_at"`
	AssignedOperatorID     string         `json:"assigned_operator_id,omitempty"`
	LastActivityAt         time.Time      `json:"last_activity_at,omitempty"`
	PendingApprovalCount   int            `json:"pending_approval_count,omitempty"`
	FailedMediaCount       int            `json:"failed_media_count,omitempty"`
	UnresolvedLintCount    int            `json:"unresolved_lint_count,omitempty"`
	PendingPreferenceCount int            `json:"pending_preference_count,omitempty"`
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
		store:                      repo,
		writes:                     writes,
		broker:                     broker,
		router:                     router,
		syncer:                     syncer,
		sessions:                   sessionsvc.New(repo, writes),
		listener:                   sessionsvc.NewListener(repo),
		operatorAPIKey:             strings.TrimSpace(os.Getenv("OPERATOR_API_KEY")),
		trustedOperatorIDHeader:    strings.TrimSpace(os.Getenv("OPERATOR_TRUSTED_ID_HEADER")),
		trustedOperatorRolesHeader: strings.TrimSpace(os.Getenv("OPERATOR_TRUSTED_ROLES_HEADER")),
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
	mux.HandleFunc("GET /v1/proposals/{id}/preview", s.getProposalPreview)
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
	mux.HandleFunc("GET /v1/operator/queue/summary", s.operatorQueueSummary)
	mux.HandleFunc("GET /v1/operator/sessions/{id}", s.operatorGetSession)
	mux.HandleFunc("GET /v1/operator/sessions/{id}/events", s.operatorListEvents)
	mux.HandleFunc("GET /v1/operator/sessions/{id}/stream", s.operatorStreamEvents)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/takeover", s.operatorTakeover)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/resume", s.operatorResume)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/messages", s.operatorCreateMessage)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/messages/on-behalf-of-agent", s.operatorCreateMessageOnBehalfOfAgent)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/notes", s.operatorCreateNote)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/process", s.operatorProcessEvent)
	mux.HandleFunc("POST /v1/operator/sessions/{id}/feedback", s.operatorCreateFeedback)
	mux.HandleFunc("GET /v1/operator/feedback", s.operatorListFeedback)
	mux.HandleFunc("GET /v1/operator/feedback/{id}", s.operatorGetFeedback)
	mux.HandleFunc("GET /v1/operator/quality/regressions", s.operatorListRegressionFixtures)
	mux.HandleFunc("POST /v1/operator/quality/regressions/{id}/state", s.operatorTransitionRegressionFixture)
	mux.HandleFunc("GET /v1/operator/quality/regressions/export", s.operatorExportRegressionFixtures)
	mux.HandleFunc("POST /v1/operator/operators", s.operatorCreateOperator)
	mux.HandleFunc("GET /v1/operator/operators", s.operatorListOperators)
	mux.HandleFunc("GET /v1/operator/operators/{id}", s.operatorGetOperator)
	mux.HandleFunc("PUT /v1/operator/operators/{id}", s.operatorUpdateOperator)
	mux.HandleFunc("POST /v1/operator/operators/{id}/tokens", s.operatorCreateOperatorToken)
	mux.HandleFunc("POST /v1/operator/operators/{id}/tokens/{token_id}/revoke", s.operatorRevokeOperatorToken)
	mux.HandleFunc("GET /v1/operator/customers/{customer_id}/preferences", s.operatorListCustomerPreferences)
	mux.HandleFunc("GET /v1/operator/customers/{customer_id}/preferences/pending", s.operatorListCustomerPreferences)
	mux.HandleFunc("PUT /v1/operator/customers/{customer_id}/preferences/{key}", s.operatorUpsertCustomerPreference)
	mux.HandleFunc("POST /v1/operator/customers/{customer_id}/preferences/{key}/confirm", s.operatorConfirmCustomerPreference)
	mux.HandleFunc("POST /v1/operator/customers/{customer_id}/preferences/{key}/reject", s.operatorRejectCustomerPreference)
	mux.HandleFunc("POST /v1/operator/customers/{customer_id}/preferences/{key}/expire", s.operatorExpireCustomerPreference)
	mux.HandleFunc("GET /v1/operator/customers/{customer_id}/preference-events", s.operatorListCustomerPreferenceEvents)
	mux.HandleFunc("POST /v1/operator/agents", s.operatorCreateAgentProfile)
	mux.HandleFunc("GET /v1/operator/agents", s.operatorListAgentProfiles)
	mux.HandleFunc("GET /v1/operator/agents/{id}", s.operatorGetAgentProfile)
	mux.HandleFunc("PUT /v1/operator/agents/{id}", s.operatorUpdateAgentProfile)
	mux.HandleFunc("POST /v1/operator/knowledge/sources", s.operatorCreateKnowledgeSource)
	mux.HandleFunc("GET /v1/operator/knowledge/sources", s.operatorListKnowledgeSources)
	mux.HandleFunc("GET /v1/operator/knowledge/sources/{id}", s.operatorGetKnowledgeSource)
	mux.HandleFunc("POST /v1/operator/knowledge/sources/{id}/compile", s.operatorCompileKnowledgeSource)
	mux.HandleFunc("POST /v1/operator/knowledge/sources/{id}/resync", s.operatorResyncKnowledgeSource)
	mux.HandleFunc("GET /v1/operator/knowledge/sources/{id}/jobs", s.operatorListKnowledgeSourceJobs)
	mux.HandleFunc("GET /v1/operator/knowledge/jobs/{id}", s.operatorGetKnowledgeSyncJob)
	mux.HandleFunc("GET /v1/operator/knowledge/snapshots/{id}", s.operatorGetKnowledgeSnapshot)
	mux.HandleFunc("GET /v1/operator/knowledge/pages", s.operatorListKnowledgePages)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals", s.operatorListKnowledgeProposals)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals/{id}", s.operatorGetKnowledgeProposal)
	mux.HandleFunc("GET /v1/operator/knowledge/proposals/{id}/preview", s.operatorPreviewKnowledgeProposal)
	mux.HandleFunc("POST /v1/operator/knowledge/proposals/{id}/state", s.operatorTransitionKnowledgeProposal)
	mux.HandleFunc("POST /v1/operator/knowledge/proposals/{id}/apply", s.operatorApplyKnowledgeProposal)
	mux.HandleFunc("POST /v1/operator/knowledge/lint/run", s.operatorRunKnowledgeLint)
	mux.HandleFunc("GET /v1/operator/knowledge/lint", s.operatorListKnowledgeLint)
	mux.HandleFunc("POST /v1/operator/knowledge/lint/{id}/resolve", s.operatorResolveKnowledgeLint)
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
	mux.HandleFunc("GET /v1/executions/{id}/quality", s.getExecutionQuality)
	mux.HandleFunc("GET /v1/executions/{id}/tool-runs", s.listToolRuns)
	mux.HandleFunc("GET /v1/executions/{id}/delivery-attempts", s.listDeliveryAttempts)
	mux.HandleFunc("POST /v1/operator/executions/{id}/retry", s.operatorRetryExecution)
	mux.HandleFunc("POST /v1/operator/executions/{id}/unblock", s.operatorUnblockExecution)
	mux.HandleFunc("POST /v1/operator/executions/{id}/abandon", s.operatorAbandonExecution)
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
		Origin:                 "manual",
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

func (s *Server) getProposalPreview(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	preview, err := s.policyProposalPreview(r.Context(), item)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, preview)
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
		"proposal":       item,
		"rollouts":       filteredRollouts,
		"eval_runs":      filteredRuns,
		"latest_quality": latestProposalQuality(r.Context(), s.store, proposalID),
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
	if req.State == rollout.StateReviewed || req.State == rollout.StateShadow {
		preview, err := s.policyProposalPreview(r.Context(), item)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if blocked, _ := preview["apply_blocked"].(bool); blocked {
			writeJSON(w, http.StatusUnprocessableEntity, preview)
			return
		}
	}
	if req.State == rollout.StateShadow || req.State == rollout.StateCanary || req.State == rollout.StateActive {
		if blocked, payload := s.policyProposalQualityBlocked(r.Context(), item); blocked && !req.ApprovedHighRisk {
			writeJSON(w, http.StatusUnprocessableEntity, payload)
			return
		}
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
		SoulHash:              summary.SoulHash,
		PreferenceHash:        summary.PreferenceHash,
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
	assignedOperatorID := strings.TrimSpace(query.Get("assigned_operator_id"))
	if assignedOperatorID == "" {
		assignedOperatorID = operatorID
	}
	activeOnly := strings.EqualFold(strings.TrimSpace(query.Get("active")), "true")
	unassignedOnly := strings.EqualFold(strings.TrimSpace(query.Get("unassigned")), "true")
	pendingApprovalOnly := strings.EqualFold(strings.TrimSpace(query.Get("pending_approval")), "true")
	failedMediaOnly := strings.EqualFold(strings.TrimSpace(query.Get("failed_media")), "true")
	unresolvedLintOnly := strings.EqualFold(strings.TrimSpace(query.Get("unresolved_lint")), "true")
	viewName := strings.TrimSpace(query.Get("view"))
	after, err := parseOptionalTime(query.Get("last_activity_after"))
	if err != nil {
		http.Error(w, "invalid last_activity_after", http.StatusBadRequest)
		return
	}
	before, err := parseOptionalTime(query.Get("last_activity_before"))
	if err != nil {
		http.Error(w, "invalid last_activity_before", http.StatusBadRequest)
		return
	}
	cursor := strings.TrimSpace(query.Get("cursor"))
	limit, err := positiveQueryInt(query.Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	views := make([]sessionView, 0, len(items))
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
		assigned := stringMetadata(sess.Metadata, "assigned_operator_id")
		if assignedOperatorID != "" && assigned != assignedOperatorID {
			continue
		}
		if unassignedOnly && assigned != "" {
			continue
		}
		if activeOnly && !(sessionMode(sess) == "manual" || assigned != "") {
			continue
		}
		view := sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess))
		view.AssignedOperatorID = assigned
		view.LastActivityAt = s.sessionLastActivityAt(r.Context(), sess)
		view.PendingApprovalCount = s.pendingApprovalCount(r.Context(), sess.ID)
		view.FailedMediaCount = s.failedMediaCount(r.Context(), sess.ID)
		view.UnresolvedLintCount = s.unresolvedLintCount(r.Context(), sess.AgentID)
		view.PendingPreferenceCount = s.pendingPreferenceCount(r.Context(), sess.AgentID, sess.CustomerID)
		if pendingApprovalOnly && view.PendingApprovalCount == 0 {
			continue
		}
		if failedMediaOnly && view.FailedMediaCount == 0 {
			continue
		}
		if unresolvedLintOnly && view.UnresolvedLintCount == 0 {
			continue
		}
		if !after.IsZero() && !view.LastActivityAt.After(after) {
			continue
		}
		if !before.IsZero() && !view.LastActivityAt.Before(before) {
			continue
		}
		if !matchesQueueView(view, viewName, operatorFromRequest(r, assignedOperatorID)) {
			continue
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].LastActivityAt.Equal(views[j].LastActivityAt) {
			return views[i].ID > views[j].ID
		}
		return views[i].LastActivityAt.After(views[j].LastActivityAt)
	})
	start := sessionCursorStart(views, cursor)
	if start > len(views) {
		start = len(views)
	}
	out := views[start:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if len(out) > 0 && start+len(out) < len(views) {
		w.Header().Set("X-Next-Cursor", encodeSessionCursor(out[len(out)-1]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorQueueSummary(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	operatorID := operatorFromRequest(r, strings.TrimSpace(r.URL.Query().Get("operator_id")))
	summary := map[string]int{
		"mine":                      0,
		"unassigned":                0,
		"manual_takeover":           0,
		"pending_approval":          0,
		"failed_media":              0,
		"pending_preference_review": 0,
		"pending_knowledge":         0,
		"pending_policy":            0,
		"needs_attention":           0,
	}
	for _, sess := range items {
		if agentID != "" && sess.AgentID != agentID {
			continue
		}
		view := sessionViewFromDomain(sess, s.sessionSummaryFor(r.Context(), sess))
		view.AssignedOperatorID = stringMetadata(sess.Metadata, "assigned_operator_id")
		view.LastActivityAt = s.sessionLastActivityAt(r.Context(), sess)
		view.PendingApprovalCount = s.pendingApprovalCount(r.Context(), sess.ID)
		view.FailedMediaCount = s.failedMediaCount(r.Context(), sess.ID)
		view.UnresolvedLintCount = s.unresolvedLintCount(r.Context(), sess.AgentID)
		view.PendingPreferenceCount = s.pendingPreferenceCount(r.Context(), sess.AgentID, sess.CustomerID)
		if operatorID != "" && view.AssignedOperatorID == operatorID {
			summary["mine"]++
		}
		if view.AssignedOperatorID == "" {
			summary["unassigned"]++
		}
		if sessionMode(sess) == "manual" || view.AssignedOperatorID != "" {
			summary["manual_takeover"]++
		}
		if view.PendingApprovalCount > 0 {
			summary["pending_approval"]++
		}
		if view.FailedMediaCount > 0 {
			summary["failed_media"]++
		}
		if view.PendingPreferenceCount > 0 {
			summary["pending_preference_review"]++
		}
		if view.PendingApprovalCount > 0 || view.FailedMediaCount > 0 || view.UnresolvedLintCount > 0 || view.PendingPreferenceCount > 0 {
			summary["needs_attention"]++
		}
	}
	kprops, _ := s.store.ListKnowledgeUpdateProposals(r.Context(), "agent", agentID)
	for _, item := range kprops {
		if item.State == "draft" || item.State == "approved" {
			summary["pending_knowledge"]++
		}
	}
	proposals, _ := s.store.ListProposals(r.Context())
	for _, item := range proposals {
		if item.State == rollout.StateProposed || item.State == rollout.StateReviewed || item.State == rollout.StateShadow || item.State == rollout.StateCanary {
			summary["pending_policy"]++
		}
	}
	writeJSON(w, http.StatusOK, summary)
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
	operatorID := requestOperatorID(r, req.OperatorID)
	sess, err := s.updateOperatorMode(r.Context(), sessionID, "manual", map[string]any{
		"assigned_operator_id": operatorID,
		"handoff_reason":       req.Reason,
		"takeover_started_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	traceID := fmt.Sprintf("trace_%d", time.Now().UnixNano())
	_, _ = s.createOperatorControlEvent(r.Context(), sess.ID, "operator.takeover.started", operatorID, req.Reason, traceID, nil)
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "operator.takeover.started",
		SessionID: sess.ID,
		TraceID:   traceID,
		Message:   "operator takeover started",
		Fields:    map[string]any{"operator_id": operatorID, "reason": req.Reason},
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
	operatorID := requestOperatorID(r, req.OperatorID)
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
	_, _ = s.createOperatorControlEvent(r.Context(), sess.ID, "operator.takeover.ended", operatorID, req.Reason, traceID, nil)
	s.appendTrace(r.Context(), audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "operator.takeover.ended",
		SessionID: sess.ID,
		TraceID:   traceID,
		Message:   "operator takeover ended",
		Fields:    map[string]any{"operator_id": operatorID, "reason": req.Reason},
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
	metadata["operator_id"] = requestOperatorID(r, req.OperatorID)
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
	exec, err := s.createExecutionForEvent(r.Context(), sessionID, event.ID, event.TraceID, time.Now().UTC(), false, time.Time{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, exec)
}

func (s *Server) operatorCreateFeedback(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	sess, err := s.store.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var record feedback.Record
	if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(record.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(record.ID) == "" {
		record.ID = fmt.Sprintf("feedback_%d", now.UnixNano())
	}
	record.SessionID = sessionID
	record.OperatorID = requestOperatorID(r, record.OperatorID)
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	if dimensions := quality.FailureLabelDimensions(record.Labels); len(dimensions) > 0 {
		var labels []string
		for label := range dimensions {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		record.Metadata["quality_failure_labels"] = labels
		record.Metadata["quality_dimensions"] = dimensions
	}
	if strings.TrimSpace(record.ExecutionID) == "" || strings.TrimSpace(record.TraceID) == "" {
		if exec, ok := s.latestSessionExecution(r.Context(), sessionID); ok {
			if strings.TrimSpace(record.ExecutionID) == "" {
				record.ExecutionID = exec.ID
			}
			if strings.TrimSpace(record.TraceID) == "" {
				record.TraceID = exec.TraceID
			}
		}
	}
	events, err := s.store.ListEvents(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if labels, ok := record.Metadata["quality_failure_labels"].([]string); ok && len(labels) > 0 {
		record.Metadata["regression_fixture_candidate"] = qualityRegressionFixtureCandidate(record, sess, events)
	}
	signals, err := s.store.ListDerivedSignals(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outputs, err := knowledgelearning.New(s.store).CompileFeedback(r.Context(), record, sess, events, signals)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	record.Outputs = outputs
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.SaveFeedbackRecord(r.Context(), record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        "operator.feedback.compiled",
		SessionID:   sessionID,
		ExecutionID: record.ExecutionID,
		TraceID:     record.TraceID,
		Message:     "operator feedback compiled",
		Fields:      map[string]any{"feedback_id": record.ID, "outputs": record.Outputs, "category": record.Category},
		CreatedAt:   now,
	})
	writeJSON(w, http.StatusCreated, record)
}

func qualityRegressionFixtureCandidate(record feedback.Record, sess session.Session, events []session.Event) map[string]any {
	var latestCustomer string
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source == "customer" && strings.TrimSpace(sessionEventText(events[i])) != "" {
			latestCustomer = sessionEventText(events[i])
			break
		}
	}
	dimensions, _ := record.Metadata["quality_dimensions"].(map[string]string)
	labels, _ := record.Metadata["quality_failure_labels"].([]string)
	scenarioID := "operator_feedback_" + sanitizeQualityFixtureID(record.ID)
	if len(labels) > 0 {
		scenarioID = "operator_feedback_" + sanitizeQualityFixtureID(labels[0])
	}
	return map[string]any{
		"source":             "operator_feedback",
		"scenario_id":        scenarioID,
		"feedback_id":        record.ID,
		"session_id":         record.SessionID,
		"execution_id":       record.ExecutionID,
		"trace_id":           record.TraceID,
		"agent_id":           sess.AgentID,
		"customer_id":        sess.CustomerID,
		"labels":             record.Metadata["quality_failure_labels"],
		"quality_dimensions": dimensions,
		"expected_behavior":  qualityFixtureExpectedBehavior(dimensions),
		"input":              latestCustomer,
		"review_status":      "candidate",
	}
}

func qualityFixtureExpectedBehavior(dimensions map[string]string) string {
	switch {
	case containsQualityDimension(dimensions, "topic_scope_compliance"):
		return "The agent should refuse or redirect instead of answering an out-of-scope request."
	case containsQualityDimension(dimensions, "knowledge_grounding"):
		return "The agent should answer only with claims supported by retrieved knowledge, policy, preferences, or tool evidence."
	case containsQualityDimension(dimensions, "customer_preference"):
		return "The agent should respect active customer preferences unless current-turn instructions or hard policy override them."
	case containsQualityDimension(dimensions, "multilingual_quality"):
		return "The agent should follow the customer's active language preference or the current-turn language instruction."
	case containsQualityDimension(dimensions, "tone_persona"):
		return "The agent should follow the active SOUL tone and persona constraints."
	case containsQualityDimension(dimensions, "refusal_escalation_quality"):
		return "The agent should refuse or escalate with the configured safe next step."
	default:
		return "The agent should satisfy the mapped response-quality dimensions."
	}
}

func containsQualityDimension(dimensions map[string]string, target string) bool {
	for _, dimension := range dimensions {
		if dimension == target {
			return true
		}
	}
	return false
}

func sanitizeQualityFixtureID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_", ",", "").Replace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func sessionEventText(event session.Event) string {
	var parts []string
	for _, part := range event.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Server) operatorListFeedback(w http.ResponseWriter, r *http.Request) {
	limit, err := positiveQueryInt(r.URL.Query().Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.store.ListFeedbackRecords(r.Context(), feedback.Query{
		SessionID:  strings.TrimSpace(r.URL.Query().Get("session_id")),
		OperatorID: strings.TrimSpace(r.URL.Query().Get("operator_id")),
		Category:   strings.TrimSpace(r.URL.Query().Get("category")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetFeedback(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetFeedbackRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type regressionFixtureView struct {
	FeedbackID        string         `json:"feedback_id"`
	SessionID         string         `json:"session_id"`
	ExecutionID       string         `json:"execution_id,omitempty"`
	TraceID           string         `json:"trace_id,omitempty"`
	OperatorID        string         `json:"operator_id,omitempty"`
	ScenarioID        string         `json:"scenario_id"`
	Input             string         `json:"input,omitempty"`
	Labels            []string       `json:"labels,omitempty"`
	QualityDimensions map[string]any `json:"quality_dimensions,omitempty"`
	ExpectedBehavior  string         `json:"expected_behavior,omitempty"`
	ReviewStatus      string         `json:"review_status"`
	ReviewedBy        string         `json:"reviewed_by,omitempty"`
	ReviewedAt        string         `json:"reviewed_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type regressionFixtureExport struct {
	ID                string         `json:"id"`
	Source            string         `json:"source"`
	FeedbackID        string         `json:"feedback_id"`
	SessionID         string         `json:"session_id"`
	ExecutionID       string         `json:"execution_id,omitempty"`
	TraceID           string         `json:"trace_id,omitempty"`
	Input             string         `json:"input"`
	ExpectedQuality   []string       `json:"expected_quality,omitempty"`
	Risk              string         `json:"risk,omitempty"`
	ExpectedBehavior  string         `json:"expected_behavior,omitempty"`
	Labels            []string       `json:"labels,omitempty"`
	QualityDimensions map[string]any `json:"quality_dimensions,omitempty"`
	ReviewStatus      string         `json:"review_status"`
	ReviewedBy        string         `json:"reviewed_by,omitempty"`
	ReviewedAt        string         `json:"reviewed_at,omitempty"`
}

func (s *Server) operatorListRegressionFixtures(w http.ResponseWriter, r *http.Request) {
	limit, err := positiveQueryInt(r.URL.Query().Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	items, err := s.store.ListFeedbackRecords(r.Context(), feedback.Query{
		SessionID:  strings.TrimSpace(r.URL.Query().Get("session_id")),
		OperatorID: strings.TrimSpace(r.URL.Query().Get("operator_id")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []regressionFixtureView
	for _, item := range items {
		view, ok := regressionFixtureViewFromFeedback(item)
		if !ok {
			continue
		}
		if status != "" && view.ReviewStatus != status {
			continue
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorExportRegressionFixtures(w http.ResponseWriter, r *http.Request) {
	limit, err := positiveQueryInt(r.URL.Query().Get("limit"), "limit")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "accepted"
	}
	items, err := s.store.ListFeedbackRecords(r.Context(), feedback.Query{
		SessionID:  strings.TrimSpace(r.URL.Query().Get("session_id")),
		OperatorID: strings.TrimSpace(r.URL.Query().Get("operator_id")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []regressionFixtureExport
	for _, item := range items {
		view, ok := regressionFixtureViewFromFeedback(item)
		if !ok || view.ReviewStatus != status {
			continue
		}
		out = append(out, regressionFixtureExportFromView(view))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorTransitionRegressionFixture(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetFeedbackRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		State      string `json:"state"`
		OperatorID string `json:"operator_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(req.State)
	if state != "accepted" && state != "rejected" && state != "candidate" {
		http.Error(w, "state must be one of candidate, accepted, rejected", http.StatusBadRequest)
		return
	}
	fixture, ok := regressionFixtureMetadata(item)
	if !ok {
		http.Error(w, "feedback record does not contain a regression fixture candidate", http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	fixture["review_status"] = state
	fixture["reviewed_by"] = requestOperatorID(r, req.OperatorID)
	fixture["reviewed_at"] = now.Format(time.RFC3339Nano)
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	item.Metadata["regression_fixture_candidate"] = fixture
	item.UpdatedAt = now
	if err := s.store.SaveFeedbackRecord(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, _ := regressionFixtureViewFromFeedback(item)
	writeJSON(w, http.StatusOK, view)
}

func regressionFixtureViewFromFeedback(item feedback.Record) (regressionFixtureView, bool) {
	fixture, ok := regressionFixtureMetadata(item)
	if !ok {
		return regressionFixtureView{}, false
	}
	view := regressionFixtureView{
		FeedbackID:  item.ID,
		SessionID:   item.SessionID,
		ExecutionID: item.ExecutionID,
		TraceID:     item.TraceID,
		OperatorID:  item.OperatorID,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
	}
	if value, _ := fixture["scenario_id"].(string); value != "" {
		view.ScenarioID = value
	}
	if value, _ := fixture["input"].(string); value != "" {
		view.Input = value
	}
	view.Labels = anyStringSlice(fixture["labels"])
	if value, ok := fixture["quality_dimensions"].(map[string]any); ok {
		view.QualityDimensions = value
	} else if value, ok := fixture["quality_dimensions"].(map[string]string); ok {
		view.QualityDimensions = map[string]any{}
		for key, item := range value {
			view.QualityDimensions[key] = item
		}
	}
	if value, _ := fixture["expected_behavior"].(string); value != "" {
		view.ExpectedBehavior = value
	}
	if value, _ := fixture["review_status"].(string); value != "" {
		view.ReviewStatus = value
	}
	if value, _ := fixture["reviewed_by"].(string); value != "" {
		view.ReviewedBy = value
	}
	if value, _ := fixture["reviewed_at"].(string); value != "" {
		view.ReviewedAt = value
	}
	if view.ReviewStatus == "" {
		view.ReviewStatus = "candidate"
	}
	return view, true
}

func regressionFixtureMetadata(item feedback.Record) (map[string]any, bool) {
	if item.Metadata == nil {
		return nil, false
	}
	fixture, ok := item.Metadata["regression_fixture_candidate"].(map[string]any)
	return fixture, ok
}

func regressionFixtureExportFromView(view regressionFixtureView) regressionFixtureExport {
	expectedQuality := uniqueSortedQualityDimensions(view.QualityDimensions)
	return regressionFixtureExport{
		ID:                view.ScenarioID,
		Source:            "operator_feedback",
		FeedbackID:        view.FeedbackID,
		SessionID:         view.SessionID,
		ExecutionID:       view.ExecutionID,
		TraceID:           view.TraceID,
		Input:             view.Input,
		ExpectedQuality:   expectedQuality,
		Risk:              regressionFixtureRisk(expectedQuality),
		ExpectedBehavior:  view.ExpectedBehavior,
		Labels:            append([]string(nil), view.Labels...),
		QualityDimensions: view.QualityDimensions,
		ReviewStatus:      view.ReviewStatus,
		ReviewedBy:        view.ReviewedBy,
		ReviewedAt:        view.ReviewedAt,
	}
}

func uniqueSortedQualityDimensions(dimensions map[string]any) []string {
	seen := map[string]struct{}{}
	for _, value := range dimensions {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			seen[strings.TrimSpace(text)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for dimension := range seen {
		out = append(out, dimension)
	}
	sort.Strings(out)
	return out
}

func regressionFixtureRisk(expectedQuality []string) string {
	for _, dimension := range expectedQuality {
		switch dimension {
		case "policy_adherence", "topic_scope_compliance", "knowledge_grounding", "refusal_escalation_quality", "hallucination_risk":
			return "high"
		case "customer_preference", "multilingual_quality", "journey_adherence":
			return "medium"
		}
	}
	return "low"
}

func anyStringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		var out []string
		for _, item := range items {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (s *Server) operatorCreateOperator(w http.ResponseWriter, r *http.Request) {
	var item operatordomain.Operator
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(item.ID) == "" {
		item.ID = fmt.Sprintf("op_%d", now.UnixNano())
	}
	if strings.TrimSpace(item.DisplayName) == "" {
		http.Error(w, "display_name is required", http.StatusBadRequest)
		return
	}
	item.Roles = normalizeOperatorRoles(item.Roles)
	if len(item.Roles) == 0 {
		item.Roles = []string{operatordomain.RoleViewer}
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = operatordomain.StatusActive
	}
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if err := s.store.SaveOperator(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) operatorListOperators(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListOperators(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetOperator(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	tokens, _ := s.store.ListOperatorAPITokens(r.Context(), item.ID)
	writeJSON(w, http.StatusOK, map[string]any{"operator": item, "tokens": tokens})
}

func (s *Server) operatorUpdateOperator(w http.ResponseWriter, r *http.Request) {
	existing, err := s.store.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var item operatordomain.Operator
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	item.ID = existing.ID
	item.CreatedAt = existing.CreatedAt
	item.UpdatedAt = time.Now().UTC()
	if strings.TrimSpace(item.Email) == "" {
		item.Email = existing.Email
	}
	if strings.TrimSpace(item.DisplayName) == "" {
		item.DisplayName = existing.DisplayName
	}
	item.Roles = normalizeOperatorRoles(item.Roles)
	if len(item.Roles) == 0 {
		item.Roles = existing.Roles
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = existing.Status
	}
	if item.Metadata == nil {
		item.Metadata = existing.Metadata
	}
	if err := s.store.SaveOperator(r.Context(), item); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) operatorCreateOperatorToken(w http.ResponseWriter, r *http.Request) {
	op, err := s.store.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		Name      string         `json:"name"`
		ExpiresAt *time.Time     `json:"expires_at"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	plain, err := newOperatorToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := operatordomain.APIToken{
		ID:         fmt.Sprintf("optok_%d", now.UnixNano()),
		OperatorID: op.ID,
		Name:       strings.TrimSpace(req.Name),
		TokenHash:  hashOperatorToken(plain),
		Status:     operatordomain.StatusActive,
		ExpiresAt:  req.ExpiresAt,
		Metadata:   cloneMap(req.Metadata),
		CreatedAt:  now,
		Plaintext:  plain,
	}
	if err := s.store.SaveOperatorAPIToken(r.Context(), token); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) operatorRevokeOperatorToken(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListOperatorAPITokens(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tokenID := r.PathValue("token_id")
	for _, token := range tokens {
		if token.ID != tokenID {
			continue
		}
		now := time.Now().UTC()
		token.Status = operatordomain.StatusRevoked
		token.RevokedAt = &now
		if err := s.store.SaveOperatorAPIToken(r.Context(), token); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, token)
		return
	}
	http.Error(w, "operator api token not found", http.StatusNotFound)
}

func (s *Server) operatorListCustomerPreferences(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	limit, ok := positiveIntQuery(w, r, "limit")
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if strings.HasSuffix(r.URL.Path, "/preferences/pending") && status == "" {
		status = customer.PreferenceStatusPending
	}
	items, err := s.store.ListCustomerPreferences(r.Context(), customer.PreferenceQuery{
		AgentID:        agentID,
		CustomerID:     r.PathValue("customer_id"),
		Status:         status,
		Key:            strings.TrimSpace(r.URL.Query().Get("key")),
		Source:         strings.TrimSpace(r.URL.Query().Get("source")),
		IncludeExpired: strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_expired")), "true"),
		Limit:          limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, preferenceView(item))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorUpsertCustomerPreference(w http.ResponseWriter, r *http.Request) {
	customerID := r.PathValue("customer_id")
	key := r.PathValue("key")
	var req struct {
		AgentID      string         `json:"agent_id"`
		Value        string         `json:"value"`
		Status       string         `json:"status"`
		Confidence   float64        `json:"confidence"`
		OperatorID   string         `json:"operator_id"`
		Metadata     map[string]any `json:"metadata"`
		EvidenceRefs []string       `json:"evidence_refs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.AgentID) == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(req.Value) == "" {
		http.Error(w, "agent_id, key, and value are required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if req.Status == "" {
		req.Status = customer.PreferenceStatusActive
	}
	if req.Confidence == 0 {
		req.Confidence = 1
	}
	prefID := stableServerID("cpref", req.AgentID, customerID, key)
	pref := customer.Preference{
		ID:              prefID,
		AgentID:         req.AgentID,
		CustomerID:      customerID,
		Key:             key,
		Value:           req.Value,
		Source:          "operator",
		Confidence:      req.Confidence,
		Status:          req.Status,
		EvidenceRefs:    req.EvidenceRefs,
		Metadata:        cloneMap(req.Metadata),
		LastConfirmedAt: &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	event := customer.PreferenceEvent{
		ID:           fmt.Sprintf("cpevt_%d", now.UnixNano()),
		PreferenceID: prefID,
		AgentID:      req.AgentID,
		CustomerID:   customerID,
		Key:          key,
		Value:        req.Value,
		Action:       "operator_upsert",
		Source:       "operator",
		Confidence:   req.Confidence,
		EvidenceRefs: req.EvidenceRefs,
		Metadata:     map[string]any{"operator_id": requestOperatorID(r, req.OperatorID)},
		CreatedAt:    now,
	}
	if err := s.store.SaveCustomerPreference(r.Context(), pref, event); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

func (s *Server) operatorConfirmCustomerPreference(w http.ResponseWriter, r *http.Request) {
	s.operatorTransitionCustomerPreference(w, r, customer.PreferenceStatusActive, "confirm")
}

func (s *Server) operatorRejectCustomerPreference(w http.ResponseWriter, r *http.Request) {
	s.operatorTransitionCustomerPreference(w, r, customer.PreferenceStatusRejected, "reject")
}

func (s *Server) operatorExpireCustomerPreference(w http.ResponseWriter, r *http.Request) {
	s.operatorTransitionCustomerPreference(w, r, customer.PreferenceStatusExpired, "expire")
}

func (s *Server) operatorTransitionCustomerPreference(w http.ResponseWriter, r *http.Request, status string, action string) {
	customerID := r.PathValue("customer_id")
	key := r.PathValue("key")
	var req struct {
		AgentID    string         `json:"agent_id"`
		Value      string         `json:"value"`
		OperatorID string         `json:"operator_id"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.AgentID == "" || strings.TrimSpace(customerID) == "" || strings.TrimSpace(key) == "" {
		http.Error(w, "agent_id, customer_id, and key are required", http.StatusBadRequest)
		return
	}
	pref, err := s.store.GetCustomerPreference(r.Context(), req.AgentID, customerID, key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(req.Value) != "" {
		pref.Value = strings.TrimSpace(req.Value)
	}
	pref.Status = status
	pref.UpdatedAt = now
	pref.Metadata = mergeMaps(pref.Metadata, req.Metadata)
	if status == customer.PreferenceStatusActive {
		pref.LastConfirmedAt = &now
		pref.ExpiresAt = nil
		if pref.Confidence < 1 {
			pref.Confidence = 1
		}
	} else if status == customer.PreferenceStatusExpired {
		pref.ExpiresAt = &now
	}
	event := customer.PreferenceEvent{
		ID:           fmt.Sprintf("cpevt_%d", now.UnixNano()),
		PreferenceID: pref.ID,
		AgentID:      pref.AgentID,
		CustomerID:   pref.CustomerID,
		Key:          pref.Key,
		Value:        pref.Value,
		Action:       action,
		Source:       "operator",
		Confidence:   pref.Confidence,
		EvidenceRefs: append([]string(nil), pref.EvidenceRefs...),
		Metadata:     map[string]any{"operator_id": requestOperatorID(r, req.OperatorID)},
		CreatedAt:    now,
	}
	if err := s.store.SaveCustomerPreference(r.Context(), pref, event); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

func (s *Server) operatorListCustomerPreferenceEvents(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	limit, ok := positiveIntQuery(w, r, "limit")
	if !ok {
		return
	}
	items, err := s.store.ListCustomerPreferenceEvents(r.Context(), customer.PreferenceQuery{
		AgentID:    agentID,
		CustomerID: r.PathValue("customer_id"),
		Key:        strings.TrimSpace(r.URL.Query().Get("key")),
		Source:     strings.TrimSpace(r.URL.Query().Get("source")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) latestSessionExecution(ctx context.Context, sessionID string) (execution.TurnExecution, bool) {
	execs, err := s.store.ListExecutions(ctx)
	if err != nil {
		return execution.TurnExecution{}, false
	}
	var out execution.TurnExecution
	for _, item := range execs {
		if item.SessionID != sessionID {
			continue
		}
		if out.ID == "" || item.CreatedAt.After(out.CreatedAt) {
			out = item
		}
	}
	return out, out.ID != ""
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
	metadata["operator_id"] = requestOperatorID(r, req.OperatorID)
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

func (s *Server) operatorCreateAgentProfile(w http.ResponseWriter, r *http.Request) {
	var profile agent.Profile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(profile.ID) == "" {
		profile.ID = fmt.Sprintf("agent_%d", now.UnixNano())
	}
	if strings.TrimSpace(profile.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(profile.Status) == "" {
		profile.Status = "active"
	}
	if profile.Metadata == nil {
		profile.Metadata = map[string]any{}
	}
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	if err := s.store.SaveAgentProfile(r.Context(), profile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, s.agentProfileView(r.Context(), profile))
}

func (s *Server) operatorListAgentProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAgentProfiles(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]agent.Profile, 0, len(items))
	for _, item := range items {
		out = append(out, s.agentProfileView(r.Context(), item))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) operatorGetAgentProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := s.store.GetAgentProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.agentProfileView(r.Context(), profile))
}

func (s *Server) operatorUpdateAgentProfile(w http.ResponseWriter, r *http.Request) {
	existing, err := s.store.GetAgentProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var profile agent.Profile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	profile.ID = existing.ID
	if strings.TrimSpace(profile.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(profile.Status) == "" {
		profile.Status = existing.Status
	}
	if profile.Metadata == nil {
		profile.Metadata = map[string]any{}
	}
	profile.CreatedAt = existing.CreatedAt
	profile.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveAgentProfile(r.Context(), profile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.agentProfileView(r.Context(), profile))
}

func (s *Server) agentProfileView(ctx context.Context, profile agent.Profile) agent.Profile {
	profile.SoulHash = s.policyBundleSoulHash(ctx, profile.DefaultPolicyBundleID)
	profile.ActiveSessionCount = s.activeSessionCount(ctx, profile.ID)
	return profile
}

func (s *Server) activeSessionCount(ctx context.Context, agentID string) int {
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return 0
	}
	var count int
	for _, sess := range sessions {
		if sess.AgentID == agentID && sessionMode(sess) != "closed" {
			count++
		}
	}
	return count
}

func (s *Server) policyBundleSoulHash(ctx context.Context, bundleID string) string {
	if strings.TrimSpace(bundleID) == "" {
		return ""
	}
	bundles, err := s.store.ListBundles(ctx)
	if err != nil {
		return ""
	}
	for _, bundle := range bundles {
		if bundle.ID == bundleID {
			return serverSoulHash(bundle.Soul)
		}
	}
	return ""
}

func serverSoulHash(soul policy.Soul) string {
	raw, err := json.Marshal(soul)
	if err != nil || string(raw) == "{}" {
		return ""
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:8])
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

func (s *Server) operatorListKnowledgeSources(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	items, err := s.store.ListKnowledgeSources(r.Context(), strings.TrimSpace(query.Get("scope_kind")), strings.TrimSpace(query.Get("scope_id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.store.GetKnowledgeSource(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, source)
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
	snapshot, err := s.compileKnowledgeSource(r.Context(), source, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, snapshot)
}

func (s *Server) operatorResyncKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.store.GetKnowledgeSource(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if source.Kind != "folder" {
		http.Error(w, "only folder knowledge sources are supported", http.StatusBadRequest)
		return
	}
	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")
	now := time.Now().UTC()
	job := knowledge.SyncJob{
		ID:          fmt.Sprintf("ksync_%d", now.UnixNano()),
		SourceID:    source.ID,
		Status:      "queued",
		Force:       force,
		RequestedBy: requestOperatorID(r, ""),
		CreatedAt:   now,
	}
	if err := s.store.SaveKnowledgeSyncJob(r.Context(), job); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) operatorListKnowledgeSourceJobs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListKnowledgeSyncJobs(r.Context(), knowledge.SyncJobQuery{
		SourceID: r.PathValue("id"),
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:    1000,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) operatorGetKnowledgeSyncJob(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetKnowledgeSyncJob(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) compileKnowledgeSource(ctx context.Context, source knowledge.Source, updateSource bool) (knowledge.Snapshot, error) {
	if source.Kind != "folder" {
		return knowledge.Snapshot{}, fmt.Errorf("only folder knowledge sources are supported")
	}
	root, err := validatedKnowledgePath(source.URI)
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	snapshot, err := knowledgecompiler.NewWithEmbedder(s.store, s.router).CompileFolder(ctx, knowledgecompiler.Input{
		ScopeKind: source.ScopeKind,
		ScopeID:   source.ScopeID,
		SourceID:  source.ID,
		Root:      root,
	})
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	if updateSource {
		source.Status = "compiled"
		source.UpdatedAt = time.Now().UTC()
		if checksum, err := knowledgeSourceChecksum(source); err == nil {
			source.Checksum = checksum
		}
		if source.Metadata == nil {
			source.Metadata = map[string]any{}
		}
		source.Metadata["last_snapshot_id"] = snapshot.ID
		if err := s.store.SaveKnowledgeSource(ctx, source); err != nil {
			return knowledge.Snapshot{}, err
		}
	}
	return snapshot, nil
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
	findings, err := s.lintKnowledgeProposal(r.Context(), item, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	blocked, warnings := lintApplyState(findings)
	payload := map[string]any{
		"proposal":        item,
		"lint_findings":   findings,
		"apply_blocked":   blocked,
		"apply_warnings":  warnings,
		"blocked_reasons": lintMessages(findings, true),
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
	if sections := s.proposalSectionPreviews(r.Context(), item); len(sections) > 0 {
		payload["sections"] = sections
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

func (s *Server) operatorRunKnowledgeLint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProposalID string `json:"proposal_id"`
		ScopeKind  string `json:"scope_kind"`
		ScopeID    string `json:"scope_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ProposalID) != "" {
		item, err := s.store.GetKnowledgeUpdateProposal(r.Context(), strings.TrimSpace(req.ProposalID))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		findings, err := s.lintKnowledgeProposal(r.Context(), item, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, findings)
		return
	}
	findings, err := s.lintKnowledgeScope(r.Context(), strings.TrimSpace(req.ScopeKind), strings.TrimSpace(req.ScopeID), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

func (s *Server) operatorListKnowledgeLint(w http.ResponseWriter, r *http.Request) {
	limit, ok := positiveIntQuery(w, r, "limit")
	if !ok {
		return
	}
	query := r.URL.Query()
	findings, err := s.store.ListKnowledgeLintFindings(r.Context(), knowledge.LintQuery{
		ScopeKind:  strings.TrimSpace(query.Get("scope_kind")),
		ScopeID:    strings.TrimSpace(query.Get("scope_id")),
		ProposalID: strings.TrimSpace(query.Get("proposal_id")),
		PageID:     strings.TrimSpace(query.Get("page_id")),
		Kind:       strings.TrimSpace(query.Get("kind")),
		Severity:   strings.TrimSpace(query.Get("severity")),
		Status:     strings.TrimSpace(query.Get("status")),
		Limit:      limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

func (s *Server) operatorResolveKnowledgeLint(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		Resolution string         `json:"resolution"`
		OperatorID string         `json:"operator_id"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.store.ListKnowledgeLintFindings(r.Context(), knowledge.LintQuery{Limit: 10000})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		item.Status = "resolved"
		item.UpdatedAt = time.Now().UTC()
		item.Metadata = mergeMaps(item.Metadata, req.Metadata)
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		item.Metadata["resolution"] = strings.TrimSpace(req.Resolution)
		item.Metadata["operator_id"] = requestOperatorID(r, req.OperatorID)
		if err := s.store.SaveKnowledgeLintFinding(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	http.Error(w, "knowledge lint finding not found", http.StatusNotFound)
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
	if len(s.proposalSectionPreviews(r.Context(), item)) > 0 {
		for _, section := range s.proposalSectionPreviews(r.Context(), item) {
			if conflict, _ := section["conflict"].(bool); conflict {
				http.Error(w, "proposal is stale relative to current page checksum", http.StatusConflict)
				return
			}
		}
	} else if _, ok := item.Payload["page"].(map[string]any); ok {
		currentPage, proposedPage := s.proposalPages(r.Context(), item)
		if proposalConflict(currentPage, proposedPage) {
			http.Error(w, "proposal is stale relative to current page checksum", http.StatusConflict)
			return
		}
	}
	findings, err := s.lintKnowledgeProposal(r.Context(), item, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if blocked, _ := lintApplyState(findings); blocked {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":         "knowledge proposal has blocking lint findings",
			"lint_findings": findings,
		})
		return
	}
	now := time.Now().UTC()
	if sections := s.proposalSectionPreviews(r.Context(), item); len(sections) > 0 {
		for _, section := range sections {
			currentRaw, _ := section["current"].(map[string]any)
			proposedRaw, _ := section["proposed"].(map[string]any)
			page := knowledge.Page{
				ID:        strings.TrimSpace(fmt.Sprint(proposedRaw["id"])),
				ScopeKind: item.ScopeKind,
				ScopeID:   item.ScopeID,
				Title:     strings.TrimSpace(fmt.Sprint(proposedRaw["title"])),
				Body:      strings.TrimSpace(fmt.Sprint(proposedRaw["final_body"])),
				PageType:  "proposal_applied",
				Citations: mergeCitations(item.Evidence, citationsFromPayload(proposedRaw)),
				Metadata:  map[string]any{"proposal_id": item.ID},
				CreatedAt: now,
				UpdatedAt: now,
			}
			if currentRaw != nil {
				page.ID = strings.TrimSpace(fmt.Sprint(currentRaw["id"]))
				page.Citations = mergeCitations(citationsFromAny(currentRaw["citations"]), page.Citations)
				page.Metadata = mergeMaps(map[string]any{}, page.Metadata)
			}
			if page.ID == "" {
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
		}
		if err := s.saveKnowledgeSnapshotForScope(r.Context(), item.ScopeKind, item.ScopeID, item.ID, now); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if pagePayload, ok := item.Payload["page"].(map[string]any); ok {
		currentPage, proposedPage := s.proposalPages(r.Context(), item)
		if proposalConflict(currentPage, proposedPage) {
			http.Error(w, "proposal is stale relative to current page checksum", http.StatusConflict)
			return
		}
		pageCitations := mergeCitations(item.Evidence, citationsFromPayload(pagePayload))
		page := knowledge.Page{
			ID:        strings.TrimSpace(fmt.Sprint(proposedPage["id"])),
			ScopeKind: item.ScopeKind,
			ScopeID:   item.ScopeID,
			Title:     strings.TrimSpace(fmt.Sprint(proposedPage["title"])),
			Body:      strings.TrimSpace(fmt.Sprint(proposedPage["final_body"])),
			PageType:  "proposal_applied",
			Citations: append([]knowledge.Citation(nil), pageCitations...),
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
		if err := s.saveKnowledgeSnapshotForScope(r.Context(), item.ScopeKind, item.ScopeID, item.ID, now); err != nil {
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

func (s *Server) lintKnowledgeProposal(ctx context.Context, item knowledge.UpdateProposal, persist bool) ([]knowledge.LintFinding, error) {
	now := time.Now().UTC()
	var findings []knowledge.LintFinding
	for _, section := range s.proposalSectionPreviews(ctx, item) {
		proposed, _ := section["proposed"].(map[string]any)
		if conflict, _ := section["conflict"].(bool); conflict {
			findings = append(findings, knowledge.LintFinding{
				ID:         stableServerID("klint", item.ID, "section_stale", fmt.Sprint(proposed["id"]), fmt.Sprint(proposed["anchor"])),
				ScopeKind:  item.ScopeKind,
				ScopeID:    item.ScopeID,
				ProposalID: item.ID,
				PageID:     strings.TrimSpace(fmt.Sprint(proposed["id"])),
				Kind:       "stale_base_checksum",
				Severity:   "high",
				Status:     "open",
				Message:    "Section proposal base checksum does not match the current page.",
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		if len(item.Evidence) == 0 && len(citationsFromPayload(proposed)) == 0 {
			findings = append(findings, knowledge.LintFinding{
				ID:         stableServerID("klint", item.ID, "section_missing_citation", fmt.Sprint(proposed["id"]), fmt.Sprint(proposed["anchor"])),
				ScopeKind:  item.ScopeKind,
				ScopeID:    item.ScopeID,
				ProposalID: item.ID,
				PageID:     strings.TrimSpace(fmt.Sprint(proposed["id"])),
				Kind:       "missing_citation",
				Severity:   "high",
				Status:     "open",
				Message:    "Section knowledge proposal has no evidence citations.",
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
	}
	if pagePayload, ok := item.Payload["page"].(map[string]any); ok {
		currentPage, proposedPage := s.proposalPages(ctx, item)
		if proposalConflict(currentPage, proposedPage) {
			findings = append(findings, knowledge.LintFinding{
				ID:         stableServerID("klint", item.ID, "stale_base"),
				ScopeKind:  item.ScopeKind,
				ScopeID:    item.ScopeID,
				ProposalID: item.ID,
				PageID:     strings.TrimSpace(fmt.Sprint(proposedPage["id"])),
				Kind:       "stale_base_checksum",
				Severity:   "high",
				Status:     "open",
				Message:    "Proposal base checksum does not match the current page.",
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		if len(item.Evidence) == 0 && len(citationsFromPayload(pagePayload)) == 0 {
			findings = append(findings, knowledge.LintFinding{
				ID:         stableServerID("klint", item.ID, "missing_citation"),
				ScopeKind:  item.ScopeKind,
				ScopeID:    item.ScopeID,
				ProposalID: item.ID,
				PageID:     strings.TrimSpace(fmt.Sprint(proposedPage["id"])),
				Kind:       "missing_citation",
				Severity:   "high",
				Status:     "open",
				Message:    "Shared knowledge proposal has no evidence citations.",
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		title := strings.TrimSpace(fmt.Sprint(proposedPage["title"]))
		if currentPage == nil && title != "" {
			pages, err := s.store.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: item.ScopeKind, ScopeID: item.ScopeID, Limit: 1000})
			if err != nil {
				return nil, err
			}
			for _, page := range pages {
				if strings.EqualFold(strings.TrimSpace(page.Title), title) && page.Checksum != knowledgeChecksum(strings.TrimSpace(fmt.Sprint(proposedPage["final_body"]))) {
					findings = append(findings, knowledge.LintFinding{
						ID:         stableServerID("klint", item.ID, "contradiction", page.ID),
						ScopeKind:  item.ScopeKind,
						ScopeID:    item.ScopeID,
						ProposalID: item.ID,
						PageID:     page.ID,
						Kind:       "possible_contradiction",
						Severity:   "high",
						Status:     "open",
						Message:    "Proposal may conflict with an existing same-title knowledge page.",
						CreatedAt:  now,
						UpdatedAt:  now,
					})
					break
				}
			}
		}
	}
	findings = s.preserveLintStatuses(ctx, findings)
	for _, finding := range findings {
		if persist {
			if err := s.store.SaveKnowledgeLintFinding(ctx, finding); err != nil {
				return nil, err
			}
		}
	}
	return findings, nil
}

func (s *Server) lintKnowledgeScope(ctx context.Context, scopeKind, scopeID string, persist bool) ([]knowledge.LintFinding, error) {
	if scopeKind == "" || scopeID == "" {
		return nil, nil
	}
	now := time.Now().UTC()
	pages, err := s.store.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 10000})
	if err != nil {
		return nil, err
	}
	chunks, err := s.store.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 10000})
	if err != nil {
		return nil, err
	}
	sources, err := s.store.ListKnowledgeSources(ctx, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	pageIDs := map[string]knowledge.Page{}
	titleIndex := map[string]knowledge.Page{}
	var findings []knowledge.LintFinding
	for _, page := range pages {
		pageIDs[page.ID] = page
		if len(page.Citations) == 0 {
			findings = append(findings, knowledge.LintFinding{
				ID:        stableServerID("klint", scopeKind, scopeID, page.ID, "missing_citation"),
				ScopeKind: scopeKind, ScopeID: scopeID, PageID: page.ID,
				Kind: "missing_citation", Severity: "medium", Status: "open",
				Message: "Knowledge page has no citations.", CreatedAt: now, UpdatedAt: now,
			})
		}
		titleKey := strings.ToLower(strings.TrimSpace(page.Title))
		if titleKey != "" {
			if existing, ok := titleIndex[titleKey]; ok && existing.Checksum != page.Checksum {
				findings = append(findings, knowledge.LintFinding{
					ID:        stableServerID("klint", scopeKind, scopeID, page.ID, existing.ID, "duplicate_title"),
					ScopeKind: scopeKind, ScopeID: scopeID, PageID: page.ID,
					Kind: "possible_contradiction", Severity: "high", Status: "open",
					Message: "Same-title knowledge pages have different checksums.", CreatedAt: now, UpdatedAt: now,
				})
			} else {
				titleIndex[titleKey] = page
			}
		}
	}
	for _, chunk := range chunks {
		if _, ok := pageIDs[chunk.PageID]; !ok {
			findings = append(findings, knowledge.LintFinding{
				ID:        stableServerID("klint", scopeKind, scopeID, chunk.ID, "orphan_chunk"),
				ScopeKind: scopeKind, ScopeID: scopeID,
				Kind: "orphan_chunk", Severity: "low", Status: "open",
				Message: "Knowledge chunk points to a missing page.", Metadata: map[string]any{"chunk_id": chunk.ID}, CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	for _, source := range sources {
		currentChecksum := stringMetadata(source.Metadata, "current_checksum")
		if source.Checksum != "" && currentChecksum != "" && source.Checksum != currentChecksum {
			findings = append(findings, knowledge.LintFinding{
				ID:        stableServerID("klint", scopeKind, scopeID, source.ID, "stale_source"),
				ScopeKind: scopeKind, ScopeID: scopeID, SourceID: source.ID,
				Kind: "stale_source_checksum", Severity: "medium", Status: "open",
				Message: "Knowledge source checksum differs from the latest observed checksum.", CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	findings = s.preserveLintStatuses(ctx, findings)
	for _, finding := range findings {
		if persist {
			if err := s.store.SaveKnowledgeLintFinding(ctx, finding); err != nil {
				return nil, err
			}
		}
	}
	return findings, nil
}

func (s *Server) preserveLintStatuses(ctx context.Context, findings []knowledge.LintFinding) []knowledge.LintFinding {
	if len(findings) == 0 {
		return findings
	}
	existing, err := s.store.ListKnowledgeLintFindings(ctx, knowledge.LintQuery{Limit: 10000})
	if err != nil {
		return findings
	}
	byID := map[string]knowledge.LintFinding{}
	for _, item := range existing {
		byID[item.ID] = item
	}
	for i := range findings {
		if prev, ok := byID[findings[i].ID]; ok && prev.Status == "resolved" {
			findings[i].Status = prev.Status
			findings[i].Metadata = mergeMaps(prev.Metadata, findings[i].Metadata)
			findings[i].CreatedAt = prev.CreatedAt
		}
	}
	return findings
}

func lintApplyState(findings []knowledge.LintFinding) (bool, []knowledge.LintFinding) {
	var warnings []knowledge.LintFinding
	for _, finding := range findings {
		if finding.Status != "" && finding.Status != "open" {
			continue
		}
		if finding.Severity == "high" {
			return true, warnings
		}
		warnings = append(warnings, finding)
	}
	return false, warnings
}

func lintMessages(findings []knowledge.LintFinding, blockedOnly bool) []string {
	var out []string
	for _, finding := range findings {
		if finding.Status != "" && finding.Status != "open" {
			continue
		}
		if blockedOnly && finding.Severity != "high" {
			continue
		}
		out = append(out, finding.Message)
	}
	return out
}

func citationsFromPayload(payload map[string]any) []knowledge.Citation {
	raw, ok := payload["citations"].([]any)
	if !ok {
		return nil
	}
	out := make([]knowledge.Citation, 0, len(raw))
	for _, item := range raw {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, knowledge.Citation{
			SourceID: strings.TrimSpace(fmt.Sprint(data["source_id"])),
			URI:      strings.TrimSpace(fmt.Sprint(data["uri"])),
			Title:    strings.TrimSpace(fmt.Sprint(data["title"])),
			Anchor:   strings.TrimSpace(fmt.Sprint(data["anchor"])),
		})
	}
	return out
}

func citationsFromAny(value any) []knowledge.Citation {
	items, ok := value.([]knowledge.Citation)
	if ok {
		return items
	}
	return nil
}

func (s *Server) proposalSectionPreviews(ctx context.Context, item knowledge.UpdateProposal) []map[string]any {
	raw, ok := item.Payload["sections"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	pages, _ := s.store.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: item.ScopeKind, ScopeID: item.ScopeID, Limit: 1000})
	var out []map[string]any
	for _, entry := range raw {
		section, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		pageID := strings.TrimSpace(fmt.Sprint(section["page_id"]))
		title := strings.TrimSpace(fmt.Sprint(section["title"]))
		var current *knowledge.Page
		for i := range pages {
			if pageID != "" && pages[i].ID == pageID {
				current = &pages[i]
				break
			}
			if title != "" && strings.EqualFold(strings.TrimSpace(pages[i].Title), title) {
				current = &pages[i]
				break
			}
		}
		currentBody := ""
		currentChecksum := ""
		currentID := pageID
		currentTitle := title
		var currentPayload map[string]any
		if current != nil {
			currentBody = current.Body
			currentChecksum = current.Checksum
			currentID = current.ID
			currentTitle = current.Title
			currentPayload = map[string]any{
				"id":        current.ID,
				"title":     current.Title,
				"body":      current.Body,
				"checksum":  current.Checksum,
				"citations": current.Citations,
			}
		}
		operation := normalizeProposalOperation(fmt.Sprint(section["operation"]))
		body := strings.TrimSpace(fmt.Sprint(section["body"]))
		anchor := strings.TrimSpace(fmt.Sprint(section["anchor"]))
		finalBody := mergeSectionBody(currentBody, anchor, body, operation)
		proposed := map[string]any{
			"id":            currentID,
			"title":         currentTitle,
			"anchor":        anchor,
			"body":          body,
			"operation":     operation,
			"base_checksum": strings.TrimSpace(fmt.Sprint(section["base_checksum"])),
			"final_body":    finalBody,
			"citations":     section["citations"],
		}
		conflict := proposed["base_checksum"] != "" && currentChecksum != "" && proposed["base_checksum"] != currentChecksum
		out = append(out, map[string]any{"current": currentPayload, "proposed": proposed, "conflict": conflict})
	}
	return out
}

func mergeSectionBody(current, anchor, proposed, operation string) string {
	current = strings.TrimSpace(current)
	anchor = strings.TrimSpace(anchor)
	proposed = strings.TrimSpace(proposed)
	if anchor == "" || current == "" {
		return mergeProposalBody(current, proposed, operation)
	}
	idx := strings.Index(strings.ToLower(current), strings.ToLower(anchor))
	if idx < 0 {
		return mergeProposalBody(current, proposed, "append")
	}
	end := strings.Index(current[idx:], "\n#")
	if end < 0 {
		end = len(current)
	} else {
		end = idx + end
	}
	before := strings.TrimSpace(current[:idx])
	existing := strings.TrimSpace(current[idx:end])
	after := strings.TrimSpace(current[end:])
	replacement := mergeProposalBody(existing, proposed, operation)
	parts := []string{}
	if before != "" {
		parts = append(parts, before)
	}
	if replacement != "" {
		parts = append(parts, replacement)
	}
	if after != "" {
		parts = append(parts, after)
	}
	return strings.Join(parts, "\n\n")
}

func (s *Server) saveKnowledgeSnapshotForScope(ctx context.Context, scopeKind, scopeID, proposalID string, now time.Time) error {
	pages, err := s.store.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1000})
	if err != nil {
		return err
	}
	chunks, err := s.store.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1000})
	if err != nil {
		return err
	}
	pageIDs := make([]string, 0, len(pages))
	chunkIDs := make([]string, 0, len(chunks))
	for _, page := range pages {
		pageIDs = append(pageIDs, page.ID)
	}
	for _, chunk := range chunks {
		chunkIDs = append(chunkIDs, chunk.ID)
	}
	return s.store.SaveKnowledgeSnapshot(ctx, knowledge.Snapshot{
		ID:        fmt.Sprintf("ksnap_%d", now.UnixNano()),
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		PageIDs:   pageIDs,
		ChunkIDs:  chunkIDs,
		Metadata:  map[string]any{"proposal_id": proposalID, "source": "proposal_apply"},
		CreatedAt: now,
	})
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

func knowledgeSourceChecksum(source knowledge.Source) (string, error) {
	if source.Kind != "folder" {
		return source.Checksum, nil
	}
	root, err := validatedKnowledgePath(source.URI)
	if err != nil {
		return "", err
	}
	var parts []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		parts = append(parts, rel+"\x00"+knowledgeChecksum(string(raw)))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(parts)
	return knowledgeChecksum(strings.Join(parts, "\x00")), nil
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

func (s *Server) operatorRetryExecution(w http.ResponseWriter, r *http.Request) {
	exec, steps, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	exec.Status = execution.StatusPending
	exec.LeaseOwner = ""
	exec.LeaseExpiresAt = time.Time{}
	exec.BlockedReason = ""
	exec.ResumeSignal = ""
	exec.UpdatedAt = now
	for _, step := range steps {
		if step.Status == execution.StatusSucceeded {
			continue
		}
		step.Status = execution.StatusPending
		step.Attempt = 0
		step.LeaseOwner = ""
		step.LeaseExpiresAt = time.Time{}
		step.NextAttemptAt = time.Time{}
		step.BlockedReason = ""
		step.ResumeSignal = ""
		step.RetryReason = ""
		step.StartedAt = time.Time{}
		step.FinishedAt = time.Time{}
		step.UpdatedAt = now
		if err := s.store.UpdateExecutionStep(r.Context(), step); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.store.UpdateExecution(r.Context(), exec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:          fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:        "execution.operator_retry",
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     "execution retry requested by operator",
		Fields:      map[string]any{"operator_id": requestOperatorID(r, "")},
		CreatedAt:   now,
	})
	writeJSON(w, http.StatusOK, exec)
}

func (s *Server) operatorUnblockExecution(w http.ResponseWriter, r *http.Request) {
	exec, steps, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	for _, step := range steps {
		if step.Status != execution.StatusBlocked {
			continue
		}
		step.Status = execution.StatusPending
		step.LeaseOwner = ""
		step.LeaseExpiresAt = time.Time{}
		step.NextAttemptAt = time.Time{}
		step.BlockedReason = ""
		step.ResumeSignal = ""
		step.UpdatedAt = now
		if err := s.store.UpdateExecutionStep(r.Context(), step); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		break
	}
	exec.Status = execution.StatusPending
	exec.LeaseOwner = ""
	exec.LeaseExpiresAt = time.Time{}
	exec.BlockedReason = ""
	exec.ResumeSignal = ""
	exec.UpdatedAt = now
	if err := s.store.UpdateExecution(r.Context(), exec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:          fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:        "execution.operator_unblocked",
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     "execution unblocked by operator",
		Fields:      map[string]any{"operator_id": requestOperatorID(r, "")},
		CreatedAt:   now,
	})
	writeJSON(w, http.StatusOK, exec)
}

func (s *Server) operatorAbandonExecution(w http.ResponseWriter, r *http.Request) {
	exec, steps, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	exec.Status = execution.StatusAbandoned
	exec.LeaseOwner = ""
	exec.LeaseExpiresAt = time.Time{}
	exec.BlockedReason = "operator_abandoned"
	exec.ResumeSignal = ""
	exec.UpdatedAt = now
	for _, step := range steps {
		if step.Status == execution.StatusSucceeded {
			continue
		}
		step.Status = execution.StatusAbandoned
		step.LeaseOwner = ""
		step.LeaseExpiresAt = time.Time{}
		step.NextAttemptAt = time.Time{}
		step.BlockedReason = "operator_abandoned"
		step.ResumeSignal = ""
		step.FinishedAt = now
		step.UpdatedAt = now
		if err := s.store.UpdateExecutionStep(r.Context(), step); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.store.UpdateExecution(r.Context(), exec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.appendTrace(r.Context(), audit.Record{
		ID:          fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:        "execution.operator_abandoned",
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     "execution abandoned by operator",
		Fields:      map[string]any{"operator_id": requestOperatorID(r, "")},
		CreatedAt:   now,
	})
	writeJSON(w, http.StatusOK, exec)
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

func (s *Server) getExecutionQuality(w http.ResponseWriter, r *http.Request) {
	exec, _, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, err := s.executionQualityPayload(r.Context(), exec, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, payload)
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

func (s *Server) policyProposalPreview(ctx context.Context, item rollout.Proposal) (map[string]any, error) {
	source, candidate, err := s.proposalBundles(ctx, item)
	if err != nil {
		return nil, err
	}
	changes := map[string]any{
		"guidelines":    diffPolicyIDs(guidelineIDs(candidate.Guidelines), guidelineIDs(source.Guidelines)),
		"journeys":      diffPolicyIDs(journeyIDs(candidate.Journeys), journeyIDs(source.Journeys)),
		"templates":     diffPolicyIDs(templateIDsForAPI(candidate.Templates), templateIDsForAPI(source.Templates)),
		"tool_policies": diffPolicyIDs(toolPolicyIDs(candidate.ToolPolicies), toolPolicyIDs(source.ToolPolicies)),
		"soul":          soulFieldDiff(source.Soul, candidate.Soul),
	}
	findings := s.policyProposalFindings(item, source, candidate, changes)
	blocked := false
	var warnings []map[string]any
	for _, finding := range findings {
		if severity, _ := finding["severity"].(string); severity == "high" {
			blocked = true
		} else {
			warnings = append(warnings, finding)
		}
	}
	return map[string]any{
		"proposal":         item,
		"source_bundle":    map[string]any{"id": source.ID, "version": source.Version},
		"candidate_bundle": map[string]any{"id": candidate.ID, "version": candidate.Version},
		"origin":           firstNonEmpty(item.Origin, proposalOrigin(item)),
		"changes":          changes,
		"lint_findings":    findings,
		"apply_blocked":    blocked,
		"apply_warnings":   warnings,
		"blocked_reasons":  lintLikeMessages(findings, true),
		"quality":          latestProposalQuality(ctx, s.store, item.ID),
	}, nil
}

func (s *Server) policyProposalQualityBlocked(ctx context.Context, item rollout.Proposal) (bool, map[string]any) {
	qualityPayload := latestProposalQuality(ctx, s.store, item.ID)
	if qualityPayload == nil {
		return false, nil
	}
	if blocked, _ := qualityPayload["hard_failed"].(bool); !blocked {
		return false, nil
	}
	return true, map[string]any{
		"proposal":        item,
		"quality_blocked": true,
		"quality":         qualityPayload,
		"message":         "latest quality eval has hard failures",
	}
}

func latestProposalQuality(ctx context.Context, repo store.Repository, proposalID string) map[string]any {
	runs, err := repo.ListEvalRuns(ctx)
	if err != nil {
		return nil
	}
	var latest replay.Run
	var latestCard quality.Scorecard
	for _, run := range runs {
		if run.ProposalID != proposalID || run.Status != replay.StatusSucceeded {
			continue
		}
		card, ok := shadowQualityScorecard(run.ResultJSON)
		if !ok {
			continue
		}
		if latest.ID == "" || run.UpdatedAt.After(latest.UpdatedAt) || (run.UpdatedAt.Equal(latest.UpdatedAt) && run.CreatedAt.After(latest.CreatedAt)) {
			latest = run
			latestCard = card
		}
	}
	if latest.ID == "" {
		return nil
	}
	return map[string]any{
		"eval_run_id":   latest.ID,
		"type":          latest.Type,
		"scorecard":     latestCard,
		"overall":       latestCard.Overall,
		"hard_failed":   quality.HardFailed(latestCard),
		"hard_failures": latestCard.HardFailures,
		"warnings":      latestCard.Warnings,
	}
}

func shadowQualityScorecard(resultJSON string) (quality.Scorecard, bool) {
	var result struct {
		Quality map[string]quality.Scorecard `json:"quality"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil || len(result.Quality) == 0 {
		return quality.Scorecard{}, false
	}
	card := result.Quality["shadow"]
	if card.Dimensions == nil {
		return quality.Scorecard{}, false
	}
	return card, true
}

func (s *Server) proposalBundles(ctx context.Context, item rollout.Proposal) (policy.Bundle, policy.Bundle, error) {
	bundles, err := s.store.ListBundles(ctx)
	if err != nil {
		return policy.Bundle{}, policy.Bundle{}, err
	}
	var source, candidate policy.Bundle
	for _, bundle := range bundles {
		if bundle.ID == item.SourceBundleID {
			source = bundle
		}
		if bundle.ID == item.CandidateBundleID {
			candidate = bundle
		}
	}
	if source.ID == "" {
		return policy.Bundle{}, policy.Bundle{}, errors.New("source bundle not found")
	}
	if candidate.ID == "" {
		return policy.Bundle{}, policy.Bundle{}, errors.New("candidate bundle not found")
	}
	return source, candidate, nil
}

func (s *Server) policyProposalFindings(item rollout.Proposal, source, candidate policy.Bundle, changes map[string]any) []map[string]any {
	var out []map[string]any
	if len(item.EvidenceRefs) == 0 {
		out = append(out, map[string]any{"kind": "missing_evidence", "severity": "high", "message": "Policy proposal has no evidence refs."})
	}
	if !proposalHasDelta(changes) {
		out = append(out, map[string]any{"kind": "empty_delta", "severity": "high", "message": "Candidate bundle has no effective change from the source bundle."})
	}
	if ids := duplicateIDs(candidate.Guidelines); len(ids) > 0 {
		out = append(out, map[string]any{"kind": "duplicate_guideline_ids", "severity": "high", "message": "Candidate bundle has duplicate guideline IDs.", "ids": ids})
	}
	if source.Soul.DefaultLanguage != candidate.Soul.DefaultLanguage && strings.TrimSpace(candidate.Soul.DefaultLanguage) != "" {
		out = append(out, map[string]any{"kind": "soul_language_change", "severity": "high", "message": "SOUL default language change requires explicit review."})
	}
	if source.Soul.EscalationStyle != candidate.Soul.EscalationStyle && strings.TrimSpace(candidate.Soul.EscalationStyle) != "" {
		out = append(out, map[string]any{"kind": "soul_escalation_change", "severity": "high", "message": "SOUL escalation style change requires explicit review."})
	}
	if len(candidate.ToolPolicies) > len(source.ToolPolicies) {
		out = append(out, map[string]any{"kind": "tool_policy_change", "severity": "medium", "message": "Candidate adds or changes tool policy exposure/approval behavior."})
	}
	if len(candidate.Journeys) > len(source.Journeys) {
		out = append(out, map[string]any{"kind": "journey_change", "severity": "medium", "message": "Candidate adds or changes journeys."})
	}
	return out
}

func proposalHasDelta(changes map[string]any) bool {
	for _, key := range []string{"guidelines", "journeys", "templates", "tool_policies"} {
		if section, ok := changes[key].(map[string]any); ok {
			if added, _ := section["added"].([]string); len(added) > 0 {
				return true
			}
			if removed, _ := section["removed"].([]string); len(removed) > 0 {
				return true
			}
		}
	}
	if soul, ok := changes["soul"].(map[string]any); ok && len(soul) > 0 {
		return true
	}
	return false
}

func proposalOrigin(item rollout.Proposal) string {
	if item.Origin != "" {
		return item.Origin
	}
	if strings.Contains(strings.ToLower(item.Rationale), "feedback") {
		return "feedback"
	}
	return "manual"
}

func lintLikeMessages(findings []map[string]any, blockedOnly bool) []string {
	var out []string
	for _, finding := range findings {
		severity, _ := finding["severity"].(string)
		if blockedOnly && severity != "high" {
			continue
		}
		if msg, _ := finding["message"].(string); msg != "" {
			out = append(out, msg)
		}
	}
	return out
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
	var qualityPayload any
	if strings.TrimSpace(run.ResultJSON) != "" {
		if err := json.Unmarshal([]byte(run.ResultJSON), &result); err != nil {
			result = map[string]any{"raw": run.ResultJSON}
		}
		qualityPayload = replayQualityPayload(run.ResultJSON)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      run.ID,
		"type":    run.Type,
		"status":  run.Status,
		"result":  result,
		"diff":    diff,
		"quality": qualityPayload,
		"error":   run.LastError,
	})
}

func replayQualityPayload(resultJSON string) map[string]quality.Scorecard {
	var result struct {
		Quality map[string]quality.Scorecard `json:"quality"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil || len(result.Quality) == 0 {
		return nil
	}
	return result.Quality
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
		if !strings.HasPrefix(r.URL.Path, "/v1/operator/") && r.URL.Path != "/v1/operator" && !strings.HasPrefix(r.URL.Path, "/v1/proposals") && !strings.HasPrefix(r.URL.Path, "/v1/rollouts") {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := s.authenticateOperator(r)
		if !ok {
			http.Error(w, "operator authorization required", http.StatusUnauthorized)
			return
		}
		permission := operatorPermission(r.Method, r.URL.Path)
		if permission != "" && !operatorHasPermission(principal.Roles, permission) {
			http.Error(w, "operator permission denied", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, principal)))
	})
}

func (s *Server) authenticateOperator(r *http.Request) (operatorPrincipal, bool) {
	if s.trustedOperatorIDHeader != "" {
		if id := strings.TrimSpace(r.Header.Get(s.trustedOperatorIDHeader)); id != "" {
			roles := splitHeaderRoles(r.Header.Get(s.trustedOperatorRolesHeader))
			if len(roles) == 0 {
				roles = []string{operatordomain.RoleViewer}
			}
			return operatorPrincipal{ID: id, Roles: roles}, true
		}
	}
	if s.operatorAPIKey != "" && operatorTokenMatches(r, s.operatorAPIKey) {
		return operatorPrincipal{ID: "bootstrap_admin", Roles: []string{operatordomain.RoleAdmin}}, true
	}
	if token := operatorBearerToken(r); token != "" {
		tokenHash := hashOperatorToken(token)
		item, err := s.store.GetOperatorAPITokenByHash(r.Context(), tokenHash)
		if err == nil && item.Status == operatordomain.StatusActive && (item.ExpiresAt == nil || item.ExpiresAt.After(time.Now().UTC())) {
			op, err := s.store.GetOperator(r.Context(), item.OperatorID)
			if err == nil && op.Status == operatordomain.StatusActive {
				now := time.Now().UTC()
				item.LastUsedAt = &now
				_ = s.store.SaveOperatorAPIToken(r.Context(), item)
				return operatorPrincipal{ID: op.ID, Roles: op.Roles}, true
			}
		}
	}
	if s.operatorAPIKey == "" && s.trustedOperatorIDHeader == "" {
		return operatorPrincipal{ID: "dev_operator", Roles: []string{operatordomain.RoleAdmin}}, true
	}
	return operatorPrincipal{}, false
}

func operatorBearerToken(r *http.Request) string {
	if got := strings.TrimSpace(r.Header.Get("X-Operator-Token")); got != "" {
		return got
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}
	return ""
}

func operatorTokenMatches(r *http.Request, want string) bool {
	got := operatorBearerToken(r)
	if got == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func splitHeaderRoles(raw string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func operatorFromContext(ctx context.Context) (operatorPrincipal, bool) {
	item, ok := ctx.Value(operatorContextKey{}).(operatorPrincipal)
	return item, ok
}

func requestOperatorID(r *http.Request, fallback string) string {
	if principal, ok := operatorFromContext(r.Context()); ok && principal.ID != "" {
		if principal.ID == "dev_operator" && strings.TrimSpace(fallback) != "" {
			return strings.TrimSpace(fallback)
		}
		return principal.ID
	}
	return strings.TrimSpace(fallback)
}

func operatorPermission(method, path string) string {
	if strings.HasPrefix(path, "/v1/proposals") || strings.HasPrefix(path, "/v1/rollouts") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "policy.manage"
	}
	if strings.HasPrefix(path, "/v1/operator/operators") {
		return "operator.manage"
	}
	if strings.Contains(path, "/takeover") || strings.Contains(path, "/resume") || strings.Contains(path, "/messages") || strings.Contains(path, "/notes") || strings.Contains(path, "/process") || strings.Contains(path, "/approvals") || strings.Contains(path, "/feedback") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "session.operate"
	}
	if strings.Contains(path, "/executions/") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "session.operate"
	}
	if strings.Contains(path, "/knowledge/") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "knowledge.manage"
	}
	if strings.Contains(path, "/media/") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "knowledge.manage"
	}
	if strings.Contains(path, "/customers/") && strings.Contains(path, "/preferences") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "session.operate"
	}
	if strings.Contains(path, "/agents") {
		if method == http.MethodGet {
			return "operator.view"
		}
		return "operator.manage"
	}
	return "operator.view"
}

func operatorHasPermission(roles []string, permission string) bool {
	for _, role := range roles {
		switch role {
		case operatordomain.RoleAdmin:
			return true
		case operatordomain.RoleViewer:
			if permission == "operator.view" {
				return true
			}
		case operatordomain.RoleOperator:
			if permission == "operator.view" || permission == "session.operate" {
				return true
			}
		case operatordomain.RoleKnowledgeManager:
			if permission == "operator.view" || permission == "knowledge.manage" {
				return true
			}
		case operatordomain.RolePolicyManager:
			if permission == "operator.view" || permission == "policy.manage" {
				return true
			}
		}
	}
	return false
}

func normalizeOperatorRoles(roles []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, role := range roles {
		role = strings.TrimSpace(role)
		switch role {
		case operatordomain.RoleViewer, operatordomain.RoleOperator, operatordomain.RoleKnowledgeManager, operatordomain.RolePolicyManager, operatordomain.RoleAdmin:
		default:
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	sort.Strings(out)
	return out
}

func newOperatorToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "opt_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashOperatorToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newStep(execID, name string, recomputable bool) execution.ExecutionStep {
	now := time.Now().UTC()
	retry := execution.DefaultRetryPolicy(recomputable)
	return execution.ExecutionStep{
		ID:                fmt.Sprintf("%s_%s", execID, name),
		ExecutionID:       execID,
		Name:              name,
		Status:            execution.StatusPending,
		Attempt:           0,
		Recomputable:      recomputable,
		MaxAttempts:       retry.MaxAttempts,
		MaxElapsedSeconds: retry.MaxElapsedSeconds,
		BackoffSeconds:    retry.BackoffSeconds,
		IdempotencyKey:    fmt.Sprintf("%s_%s", execID, name),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func (s *Server) enqueueSessionTurn(ctx context.Context, sessionID, eventID, source, kind string, content []session.ContentPart, data, metadata map[string]any) (session.Event, string, string, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(eventID) == "" {
		eventID = fmt.Sprintf("evt_%d", now.UnixNano())
	}
	traceID := fmt.Sprintf("trace_%d", now.UnixNano())
	window := responseCoalesceWindow()
	coalesce := source == "customer" && kind == acp.EventKindMessage && window > 0
	exec, err := s.createExecutionForEvent(ctx, sessionID, eventID, traceID, now, coalesce, now.Add(window))
	if err != nil {
		return session.Event{}, "", "", err
	}
	traceID = exec.TraceID
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

func (s *Server) createExecutionForEvent(ctx context.Context, sessionID, eventID, traceID string, now time.Time, coalesce bool, coalesceUntil time.Time) (execution.TurnExecution, error) {
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
		TriggerEventIDs: []string{
			eventID,
		},
		TraceID:   traceID,
		Status:    execution.StatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	steps := []execution.ExecutionStep{
		newStep(execID, "ingest", false),
		newStep(execID, "resolve_policy", true),
		newStep(execID, "match_and_plan", true),
		newStep(execID, "compose_response", true),
		newStep(execID, "deliver_response", false),
	}
	if coalesce {
		exec.Status = execution.StatusWaiting
		exec.LeaseExpiresAt = coalesceUntil
		if len(steps) > 0 {
			steps[0].Status = execution.StatusWaiting
			steps[0].NextAttemptAt = coalesceUntil
			steps[0].LeaseExpiresAt = coalesceUntil
			steps[0].UpdatedAt = now
		}
		created, _, _, err := s.store.CreateOrCoalesceExecution(ctx, exec, steps, eventID, coalesceUntil)
		if err != nil {
			return execution.TurnExecution{}, err
		}
		return created, nil
	}
	if err := s.store.CreateExecution(ctx, exec, steps); err != nil {
		return execution.TurnExecution{}, err
	}
	return exec, nil
}

func responseCoalesceWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv("ACP_RESPONSE_COALESCE_MS"))
	if raw == "" {
		return 1500 * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 {
		return 1500 * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
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

func parseOptionalTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func (s *Server) sessionLastActivityAt(ctx context.Context, sess session.Session) time.Time {
	last := sess.CreatedAt
	events, err := s.store.ListEvents(ctx, sess.ID)
	if err == nil {
		for _, event := range events {
			if event.CreatedAt.After(last) {
				last = event.CreatedAt
			}
		}
	}
	execs, err := s.store.ListExecutions(ctx)
	if err == nil {
		for _, exec := range execs {
			if exec.SessionID == sess.ID && exec.UpdatedAt.After(last) {
				last = exec.UpdatedAt
			}
		}
	}
	return last
}

func (s *Server) pendingApprovalCount(ctx context.Context, sessionID string) int {
	items, err := s.store.ListApprovalSessions(ctx, sessionID)
	if err != nil {
		return 0
	}
	var count int
	for _, item := range items {
		if item.Status == approval.StatusPending {
			count++
		}
	}
	return count
}

func (s *Server) failedMediaCount(ctx context.Context, sessionID string) int {
	items, err := s.store.ListMediaAssets(ctx, sessionID)
	if err != nil {
		return 0
	}
	var count int
	for _, item := range items {
		if item.Status == "failed" || stringMetadata(item.Metadata, "enrichment_status") == "failed" {
			count++
		}
	}
	return count
}

func (s *Server) unresolvedLintCount(ctx context.Context, agentID string) int {
	if strings.TrimSpace(agentID) == "" {
		return 0
	}
	items, err := s.store.ListKnowledgeLintFindings(ctx, knowledge.LintQuery{ScopeKind: "agent", ScopeID: agentID, Status: "open", Severity: "high", Limit: 1000})
	if err != nil {
		return 0
	}
	return len(items)
}

func cloneMap(src map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range src {
		out[key] = value
	}
	return out
}

func stableServerID(prefix string, parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return prefix + "_" + hex.EncodeToString(sum[:8])
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
				exec.BlockedReason = ""
				exec.ResumeSignal = ""
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Time{}
				exec.UpdatedAt = time.Now().UTC()
				_ = s.store.UpdateExecution(ctx, exec)
				if _, steps, err := s.store.GetExecution(ctx, exec.ID); err == nil {
					for _, step := range steps {
						if step.Status != execution.StatusBlocked {
							continue
						}
						if step.ResumeSignal != "" && step.ResumeSignal != execution.ResumeSignalApproval {
							continue
						}
						if step.BlockedReason != "" && step.BlockedReason != execution.BlockedReasonApprovalRequired {
							continue
						}
						step.Status = execution.StatusPending
						step.BlockedReason = ""
						step.ResumeSignal = ""
						step.LeaseOwner = ""
						step.LeaseExpiresAt = time.Time{}
						step.UpdatedAt = time.Now().UTC()
						_ = s.store.UpdateExecutionStep(ctx, step)
						break
					}
				}
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
		summary.SoulHash = stringMetadata(sess.Metadata, "soul_hash")
		summary.PreferenceHash = stringMetadata(sess.Metadata, "preference_hash")
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

func positiveIntQuery(w http.ResponseWriter, r *http.Request, key string) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		http.Error(w, key+" must be a non-negative integer", http.StatusBadRequest)
		return 0, false
	}
	return value, true
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
	ScopeClassification  string                            `json:"scope_classification,omitempty"`
	ScopeAction          string                            `json:"scope_action,omitempty"`
	ScopeReasons         []string                          `json:"scope_reasons,omitempty"`
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

func (s *Server) executionQualityPayload(ctx context.Context, exec execution.TurnExecution, bundleID string) (map[string]any, error) {
	events, err := s.store.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return nil, err
	}
	sess, err := s.store.GetSession(ctx, exec.SessionID)
	if err != nil {
		return nil, err
	}
	journeyInstances, err := s.store.ListJourneyInstances(ctx, exec.SessionID)
	if err != nil {
		return nil, err
	}
	catalog, err := s.store.ListCatalogEntries(ctx)
	if err != nil {
		return nil, err
	}
	bundles, err := s.store.ListBundles(ctx)
	if err != nil {
		return nil, err
	}
	profile := s.qualityAgentProfile(ctx, sess.AgentID)
	defaultBundleID := exec.PolicyBundleID
	if defaultBundleID == "" && profile.ID != "" {
		defaultBundleID = profile.DefaultPolicyBundleID
	}
	selectedBundleID := strings.TrimSpace(bundleID)
	if selectedBundleID == "" {
		selectedBundleID = defaultBundleID
	}
	if selectedBundleID == "" {
		proposals, err := s.store.ListProposals(ctx)
		if err != nil {
			return nil, err
		}
		rollouts, err := s.store.ListRollouts(ctx)
		if err != nil {
			return nil, err
		}
		selection := rolloutengine.SelectBundle(sess, proposals, rollouts, defaultBundleID)
		selectedBundleID = selection.BundleID
	}
	selected := selectBundles(bundles, selectedBundleID, defaultBundleID)
	knowledgeSnapshot, knowledgeChunks := s.qualityKnowledgeSnapshot(ctx, sess, profile, selected)
	view, err := policyruntime.ResolveWithOptions(ctx, eventsForExecutionResolve(events, exec), selected, journeyInstances, catalog, policyruntime.ResolveOptions{
		Router:            s.router,
		KnowledgeSearcher: s.store,
		KnowledgeSnapshot: knowledgeSnapshot,
		KnowledgeChunks:   knowledgeChunks,
		DerivedSignals:    s.qualityDerivedSignals(ctx, exec.SessionID),
	})
	if err != nil {
		return nil, err
	}
	view.CustomerPreferences = s.qualityCustomerPreferences(ctx, sess)
	response := assistantTextForExecution(events, exec.ID)
	if response == "" {
		response = quality.ResponseFromView(view)
	}
	card := quality.GradeWithLLM(ctx, s.router, view, response, nil)
	return map[string]any{
		"execution_id":     exec.ID,
		"session_id":       exec.SessionID,
		"bundle_id":        exec.PolicyBundleID,
		"response":         response,
		"plan":             quality.BuildResponsePlan(view),
		"claims":           card.Claims,
		"evidence_matches": card.EvidenceMatches,
		"scorecard":        card,
		"hard_failed":      quality.HardFailed(card),
	}, nil
}

func (s *Server) qualityAgentProfile(ctx context.Context, agentID string) agent.Profile {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return agent.Profile{}
	}
	profile, err := s.store.GetAgentProfile(ctx, agentID)
	if err != nil {
		return agent.Profile{}
	}
	switch strings.TrimSpace(profile.Status) {
	case "disabled", "retired":
		return agent.Profile{}
	default:
		return profile
	}
}

func (s *Server) qualityKnowledgeSnapshot(ctx context.Context, sess session.Session, profile agent.Profile, bundles []policy.Bundle) (*knowledge.Snapshot, []knowledge.Chunk) {
	var snapshots []knowledge.Snapshot
	var chunks []knowledge.Chunk
	customerScopeKind, customerScopeID := qualityCustomerKnowledgeScope(sess)
	if customerScopeID != "" {
		customerSnapshots, err := s.store.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, Limit: 1})
		if err == nil && len(customerSnapshots) > 0 {
			snapshots = append(snapshots, customerSnapshots[0])
			customerChunks, _ := s.store.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, SnapshotID: customerSnapshots[0].ID})
			chunks = append(chunks, customerChunks...)
		}
	}
	scopeKind, scopeID := qualityKnowledgeScope(sess, bundles)
	if scopeID != "" {
		sharedSnapshots, err := s.store.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1})
		if err == nil && len(sharedSnapshots) > 0 {
			snapshots = append(snapshots, sharedSnapshots[0])
			sharedChunks, _ := s.store.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, SnapshotID: sharedSnapshots[0].ID})
			chunks = append(chunks, sharedChunks...)
		}
	}
	if len(snapshots) == 0 && strings.TrimSpace(profile.DefaultKnowledgeScopeKind) != "" && strings.TrimSpace(profile.DefaultKnowledgeScopeID) != "" {
		profileSnapshots, err := s.store.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: profile.DefaultKnowledgeScopeKind, ScopeID: profile.DefaultKnowledgeScopeID, Limit: 1})
		if err == nil && len(profileSnapshots) > 0 {
			snapshots = append(snapshots, profileSnapshots[0])
			profileChunks, _ := s.store.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: profile.DefaultKnowledgeScopeKind, ScopeID: profile.DefaultKnowledgeScopeID, SnapshotID: profileSnapshots[0].ID})
			chunks = append(chunks, profileChunks...)
		}
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	snapshot := snapshots[0]
	return &snapshot, chunks
}

func qualityKnowledgeScope(sess session.Session, bundles []policy.Bundle) (string, string) {
	if strings.TrimSpace(sess.AgentID) != "" {
		return "agent", strings.TrimSpace(sess.AgentID)
	}
	if len(bundles) > 0 && strings.TrimSpace(bundles[0].ID) != "" {
		return "bundle", strings.TrimSpace(bundles[0].ID)
	}
	return "", ""
}

func qualityCustomerKnowledgeScope(sess session.Session) (string, string) {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return "", ""
	}
	return "customer_agent", strings.TrimSpace(sess.AgentID) + ":" + strings.TrimSpace(sess.CustomerID)
}

func (s *Server) qualityCustomerPreferences(ctx context.Context, sess session.Session) []customer.Preference {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return nil
	}
	items, err := s.store.ListCustomerPreferences(ctx, customer.PreferenceQuery{
		AgentID:       strings.TrimSpace(sess.AgentID),
		CustomerID:    strings.TrimSpace(sess.CustomerID),
		Status:        customer.PreferenceStatusActive,
		MinConfidence: 0.5,
	})
	if err != nil {
		return nil
	}
	return items
}

func (s *Server) qualityDerivedSignals(ctx context.Context, sessionID string) []string {
	signals, err := s.store.ListDerivedSignals(ctx, sessionID)
	if err != nil {
		return nil
	}
	var out []string
	for _, signal := range signals {
		if strings.TrimSpace(signal.Value) == "" {
			continue
		}
		out = append(out, signal.Kind+": "+signal.Value)
	}
	return out
}

func eventsForExecutionResolve(events []session.Event, exec execution.TurnExecution) []session.Event {
	if exec.TriggerEventID == "" {
		return events
	}
	var trigger session.Event
	for _, event := range events {
		if event.ID == exec.TriggerEventID {
			trigger = event
			break
		}
	}
	if trigger.ID == "" {
		return events
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		if event.ID == trigger.ID || event.CreatedAt.Before(trigger.CreatedAt) || event.CreatedAt.Equal(trigger.CreatedAt) {
			if event.Source != "ai_agent" || event.ID == trigger.ID {
				out = append(out, event)
			}
		}
	}
	return out
}

func assistantTextForExecution(events []session.Event, executionID string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "ai_agent" || (executionID != "" && events[i].ExecutionID != executionID) {
			continue
		}
		var parts []string
		for _, part := range events[i].Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
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
		ScopeClassification:  view.ScopeBoundaryStage.Classification,
		ScopeAction:          view.ScopeBoundaryStage.Action,
		ScopeReasons:         append([]string(nil), view.ScopeBoundaryStage.Reasons...),
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

func journeyIDs(items []policy.Journey) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func toolPolicyIDs(items []policy.ToolPolicy) []string {
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

func diffPolicyIDs(candidate, source []string) map[string]any {
	sourceSet := map[string]struct{}{}
	candidateSet := map[string]struct{}{}
	for _, id := range source {
		sourceSet[id] = struct{}{}
	}
	for _, id := range candidate {
		candidateSet[id] = struct{}{}
	}
	var added, removed []string
	for _, id := range candidate {
		if _, ok := sourceSet[id]; !ok {
			added = append(added, id)
		}
	}
	for _, id := range source {
		if _, ok := candidateSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	return map[string]any{"added": added, "removed": removed}
}

func soulFieldDiff(source, candidate policy.Soul) map[string]any {
	var diff = map[string]any{}
	add := func(key, oldValue, newValue string) {
		if strings.TrimSpace(oldValue) != strings.TrimSpace(newValue) {
			diff[key] = map[string]string{"from": oldValue, "to": newValue}
		}
	}
	add("identity", source.Identity, candidate.Identity)
	add("role", source.Role, candidate.Role)
	add("brand", source.Brand, candidate.Brand)
	add("default_language", source.DefaultLanguage, candidate.DefaultLanguage)
	add("language_matching", source.LanguageMatching, candidate.LanguageMatching)
	add("tone", source.Tone, candidate.Tone)
	add("formality", source.Formality, candidate.Formality)
	add("verbosity", source.Verbosity, candidate.Verbosity)
	add("escalation_style", source.EscalationStyle, candidate.EscalationStyle)
	if !equalStringSlices(source.SupportedLanguages, candidate.SupportedLanguages) {
		diff["supported_languages"] = map[string][]string{"from": source.SupportedLanguages, "to": candidate.SupportedLanguages}
	}
	if !equalStringSlices(source.StyleRules, candidate.StyleRules) {
		diff["style_rules"] = map[string][]string{"from": source.StyleRules, "to": candidate.StyleRules}
	}
	if !equalStringSlices(source.AvoidRules, candidate.AvoidRules) {
		diff["avoid_rules"] = map[string][]string{"from": source.AvoidRules, "to": candidate.AvoidRules}
	}
	if !equalStringSlices(source.FormattingRules, candidate.FormattingRules) {
		diff["formatting_rules"] = map[string][]string{"from": source.FormattingRules, "to": candidate.FormattingRules}
	}
	return diff
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func duplicateIDs(items []policy.Guideline) []string {
	seen := map[string]int{}
	var out []string
	for _, item := range items {
		seen[item.ID]++
		if seen[item.ID] == 2 {
			out = append(out, item.ID)
		}
	}
	return out
}

func operatorFromRequest(r *http.Request, fallback string) string {
	return requestOperatorID(r, fallback)
}

func (s *Server) pendingPreferenceCount(ctx context.Context, agentID string, customerID string) int {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(customerID) == "" {
		return 0
	}
	items, err := s.store.ListCustomerPreferences(ctx, customer.PreferenceQuery{
		AgentID:        agentID,
		CustomerID:     customerID,
		Status:         customer.PreferenceStatusPending,
		IncludeExpired: true,
		Limit:          1000,
	})
	if err != nil {
		return 0
	}
	return len(items)
}

func matchesQueueView(view sessionView, viewName, operatorID string) bool {
	switch viewName {
	case "", "all":
		return true
	case "mine":
		return operatorID != "" && view.AssignedOperatorID == operatorID
	case "unassigned":
		return view.AssignedOperatorID == ""
	case "manual_takeover":
		return view.Mode == "manual" || view.AssignedOperatorID != ""
	case "pending_approval":
		return view.PendingApprovalCount > 0
	case "failed_media":
		return view.FailedMediaCount > 0
	case "pending_preference_review":
		return view.PendingPreferenceCount > 0
	case "needs_attention":
		return view.PendingApprovalCount > 0 || view.FailedMediaCount > 0 || view.UnresolvedLintCount > 0 || view.PendingPreferenceCount > 0
	default:
		return true
	}
}

type sessionCursor struct {
	LastActivityAt string `json:"last_activity_at"`
	ID             string `json:"id"`
}

func encodeSessionCursor(view sessionView) string {
	raw, _ := json.Marshal(sessionCursor{LastActivityAt: view.LastActivityAt.UTC().Format(time.RFC3339Nano), ID: view.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func sessionCursorStart(items []sessionView, cursor string) int {
	if cursor == "" {
		return 0
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(cursor); err == nil {
		var item sessionCursor
		if json.Unmarshal(decoded, &item) == nil {
			for i, view := range items {
				if view.ID == item.ID && view.LastActivityAt.UTC().Format(time.RFC3339Nano) == item.LastActivityAt {
					return i + 1
				}
			}
			return len(items)
		}
	}
	for i, view := range items {
		if view.ID == cursor {
			return i + 1
		}
	}
	return len(items)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func preferenceView(item customer.Preference) map[string]any {
	view := map[string]any{
		"id":                item.ID,
		"agent_id":          item.AgentID,
		"customer_id":       item.CustomerID,
		"key":               item.Key,
		"value":             item.Value,
		"source":            item.Source,
		"confidence":        item.Confidence,
		"status":            item.Status,
		"evidence_refs":     item.EvidenceRefs,
		"metadata":          item.Metadata,
		"last_confirmed_at": item.LastConfirmedAt,
		"expires_at":        item.ExpiresAt,
		"created_at":        item.CreatedAt,
		"updated_at":        item.UpdatedAt,
	}
	if item.Status == customer.PreferenceStatusPending {
		view["review_reason"] = stringMetadata(item.Metadata, "review_reason")
		view["confirmation_prompt"] = firstNonEmpty(stringMetadata(item.Metadata, "confirmation_prompt"), "Confirm pending preference "+item.Key+" for this customer.")
	}
	return view
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
