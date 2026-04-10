package maintainer

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	maintainerdomain "github.com/sahal/parmesan/internal/domain/maintainer"
	"github.com/sahal/parmesan/internal/domain/session"
	knowledgecompiler "github.com/sahal/parmesan/internal/knowledge/compiler"
	knowledgelearning "github.com/sahal/parmesan/internal/knowledge/learning"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store"
)

type Service struct {
	repo store.Repository
}

func NewService(repo store.Repository) *Service {
	return &Service{repo: repo}
}

type Runner struct {
	repo     store.Repository
	router   *model.Router
	service  *Service
	interval time.Duration
}

func New(repo store.Repository, router *model.Router) *Runner {
	return &Runner{
		repo:     repo,
		router:   router,
		service:  NewService(repo),
		interval: time.Second,
	}
}

func (r *Runner) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Runner) loop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) {
	jobs, err := r.repo.ListRunnableKnowledgeSyncJobs(ctx)
	if err == nil {
		for _, job := range jobs {
			_ = r.processKnowledgeSyncJob(ctx, job.ID)
		}
	}
	maintainerJobs, err := r.repo.ListRunnableMaintainerJobs(ctx)
	if err == nil {
		for _, job := range maintainerJobs {
			_ = r.processMaintainerJob(ctx, job.ID)
		}
	}
}

func (s *Service) QueueBootstrap(ctx context.Context, profile agent.Profile, requestedBy string) (maintainerdomain.Job, error) {
	scopeKind, scopeID := sharedScopeForProfile(profile)
	workspace, err := s.ensureWorkspace(ctx, scopeKind, scopeID, maintainerdomain.ModeSharedWiki)
	if err != nil {
		return maintainerdomain.Job{}, err
	}
	now := time.Now().UTC()
	job := maintainerdomain.Job{
		ID:          stableID("mjob", workspace.ID, maintainerdomain.TriggerBootstrap, now.Format(time.RFC3339Nano)),
		WorkspaceID: workspace.ID,
		ScopeKind:   scopeKind,
		ScopeID:     scopeID,
		AgentID:     profile.ID,
		Mode:        maintainerdomain.ModeSharedWiki,
		Trigger:     maintainerdomain.TriggerBootstrap,
		Status:      maintainerdomain.StatusQueued,
		RequestedBy: requestedBy,
		Metadata: map[string]any{
			"profile_name":        profile.Name,
			"profile_description": profile.Description,
			"default_bundle_id":   profile.DefaultPolicyBundleID,
		},
		CreatedAt: now,
	}
	return job, s.repo.SaveMaintainerJob(ctx, job)
}

func (s *Service) QueueFeedback(ctx context.Context, sess session.Session, record feedback.Record) (maintainerdomain.Job, error) {
	if !shouldQueueMaintainerFeedback(record) {
		return maintainerdomain.Job{}, nil
	}
	mode := maintainerdomain.ModeSharedWiki
	scopeKind, scopeID := sharedScopeForSession(sess)
	if strings.TrimSpace(sess.CustomerID) != "" && !shouldUseSharedWikiFeedback(record) {
		mode = maintainerdomain.ModeCustomerMemory
		scopeKind, scopeID = customerMemoryScopeForSession(sess)
	}
	workspace, err := s.ensureWorkspace(ctx, scopeKind, scopeID, mode)
	if err != nil {
		return maintainerdomain.Job{}, err
	}
	now := time.Now().UTC()
	job := maintainerdomain.Job{
		ID:          stableID("mjob", workspace.ID, maintainerdomain.TriggerFeedback, record.ID, now.Format(time.RFC3339Nano)),
		WorkspaceID: workspace.ID,
		ScopeKind:   scopeKind,
		ScopeID:     scopeID,
		AgentID:     sess.AgentID,
		CustomerID:  sess.CustomerID,
		Mode:        mode,
		Trigger:     maintainerdomain.TriggerFeedback,
		Status:      maintainerdomain.StatusQueued,
		RequestedBy: record.OperatorID,
		SessionID:   record.SessionID,
		FeedbackID:  record.ID,
		ResponseID:  record.ResponseID,
		Metadata: map[string]any{
			"category":    record.Category,
			"labels":      append([]string(nil), record.Labels...),
			"response_id": record.ResponseID,
		},
		CreatedAt: now,
	}
	return job, s.repo.SaveMaintainerJob(ctx, job)
}

func (s *Service) QueueSessionLearning(ctx context.Context, sess session.Session, requestedBy string) (maintainerdomain.Job, error) {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return maintainerdomain.Job{}, nil
	}
	scopeKind, scopeID := customerMemoryScopeForSession(sess)
	workspace, err := s.ensureWorkspace(ctx, scopeKind, scopeID, maintainerdomain.ModeCustomerMemory)
	if err != nil {
		return maintainerdomain.Job{}, err
	}
	now := time.Now().UTC()
	job := maintainerdomain.Job{
		ID:          stableID("mjob", workspace.ID, maintainerdomain.TriggerSessionEnd, sess.ID, now.Format(time.RFC3339Nano)),
		WorkspaceID: workspace.ID,
		ScopeKind:   scopeKind,
		ScopeID:     scopeID,
		AgentID:     sess.AgentID,
		CustomerID:  sess.CustomerID,
		Mode:        maintainerdomain.ModeCustomerMemory,
		Trigger:     maintainerdomain.TriggerSessionEnd,
		Status:      maintainerdomain.StatusQueued,
		RequestedBy: requestedBy,
		SessionID:   sess.ID,
		CreatedAt:   now,
	}
	return job, s.repo.SaveMaintainerJob(ctx, job)
}

func (s *Service) ensureWorkspace(ctx context.Context, scopeKind, scopeID, mode string) (maintainerdomain.Workspace, error) {
	items, err := s.repo.ListMaintainerWorkspaces(ctx, maintainerdomain.WorkspaceQuery{ScopeKind: scopeKind, ScopeID: scopeID, Mode: mode, Limit: 1})
	if err != nil {
		return maintainerdomain.Workspace{}, err
	}
	if len(items) > 0 {
		return items[0], nil
	}
	now := time.Now().UTC()
	workspace := maintainerdomain.Workspace{
		ID:        stableID("kwork", scopeKind, scopeID, mode),
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		Mode:      mode,
		Status:    "active",
		Schema:    defaultWorkspaceSchema(scopeKind, scopeID, mode),
		Metadata:  map[string]any{"created_by": "maintainer"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return workspace, s.repo.SaveMaintainerWorkspace(ctx, workspace)
}

func (r *Runner) processKnowledgeSyncJob(ctx context.Context, jobID string) error {
	job, err := r.repo.GetKnowledgeSyncJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == "succeeded" || job.Status == "failed" || job.Status == "skipped" {
		return nil
	}
	source, err := r.repo.GetKnowledgeSource(ctx, job.SourceID)
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		done := time.Now().UTC()
		job.FinishedAt = &done
		return r.repo.SaveKnowledgeSyncJob(ctx, job)
	}
	now := time.Now().UTC()
	job.Status = "running"
	job.StartedAt = &now
	job.OldChecksum = source.Checksum
	if err := r.repo.SaveKnowledgeSyncJob(ctx, job); err != nil {
		return err
	}
	checksum, err := knowledgeSourceChecksum(source)
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		done := time.Now().UTC()
		job.FinishedAt = &done
		return r.repo.SaveKnowledgeSyncJob(ctx, job)
	}
	job.NewChecksum = checksum
	if !job.Force && source.Checksum != "" && checksum == source.Checksum {
		job.Status = "skipped"
		job.Changed = false
		done := time.Now().UTC()
		job.FinishedAt = &done
		if job.Metadata == nil {
			job.Metadata = map[string]any{}
		}
		job.Metadata["reason"] = "unchanged_checksum"
		return r.repo.SaveKnowledgeSyncJob(ctx, job)
	}
	run, snapshot, err := r.runSourceMaintenance(ctx, source, job.RequestedBy)
	if run.ID != "" {
		job.Metadata = mergeMaps(job.Metadata, map[string]any{"maintainer_run_id": run.ID})
	}
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		done := time.Now().UTC()
		job.FinishedAt = &done
		_ = r.repo.SaveKnowledgeSyncJob(ctx, job)
		source.Status = "failed"
		source.UpdatedAt = done
		source.Metadata = mergeMaps(source.Metadata, map[string]any{"error": err.Error()})
		_ = r.repo.SaveKnowledgeSource(ctx, source)
		return err
	}
	done := time.Now().UTC()
	job.Status = "succeeded"
	job.Changed = true
	job.SnapshotID = snapshot.ID
	job.Error = ""
	job.FinishedAt = &done
	if err := r.repo.SaveKnowledgeSyncJob(ctx, job); err != nil {
		return err
	}
	return nil
}

func (r *Runner) processMaintainerJob(ctx context.Context, jobID string) error {
	job, err := r.repo.GetMaintainerJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == maintainerdomain.StatusSucceeded || job.Status == maintainerdomain.StatusFailed || job.Status == maintainerdomain.StatusSkipped {
		return nil
	}
	now := time.Now().UTC()
	run := maintainerdomain.Run{
		ID:          stableID("mrun", job.ID, now.Format(time.RFC3339Nano)),
		JobID:       job.ID,
		WorkspaceID: job.WorkspaceID,
		ScopeKind:   job.ScopeKind,
		ScopeID:     job.ScopeID,
		AgentID:     job.AgentID,
		CustomerID:  job.CustomerID,
		Mode:        job.Mode,
		Trigger:     job.Trigger,
		Status:      maintainerdomain.StatusRunning,
		CreatedAt:   now,
		StartedAt:   &now,
	}
	job.Status = maintainerdomain.StatusRunning
	job.StartedAt = &now
	job.RunID = run.ID
	if err := r.repo.SaveMaintainerRun(ctx, run); err != nil {
		return err
	}
	if err := r.repo.SaveMaintainerJob(ctx, job); err != nil {
		return err
	}
	var output map[string]any
	switch job.Trigger {
	case maintainerdomain.TriggerBootstrap:
		output, err = r.processBootstrapJob(ctx, job, &run)
	case maintainerdomain.TriggerFeedback:
		output, err = r.processFeedbackJob(ctx, job, &run)
	case maintainerdomain.TriggerSessionEnd:
		output, err = r.processSessionJob(ctx, job, &run)
	default:
		output = map[string]any{"skipped": true, "reason": "unsupported_trigger"}
	}
	done := time.Now().UTC()
	run.OutputSummary = output
	run.FinishedAt = &done
	job.FinishedAt = &done
	if err != nil {
		run.Status = maintainerdomain.StatusFailed
		job.Status = maintainerdomain.StatusFailed
		job.Error = err.Error()
		run.Metadata = mergeMaps(run.Metadata, map[string]any{"error": err.Error()})
	} else {
		run.Status = maintainerdomain.StatusSucceeded
		job.Status = maintainerdomain.StatusSucceeded
	}
	if saveErr := r.repo.SaveMaintainerRun(ctx, run); saveErr != nil {
		return saveErr
	}
	return r.repo.SaveMaintainerJob(ctx, job)
}

func (r *Runner) processBootstrapJob(ctx context.Context, job maintainerdomain.Job, run *maintainerdomain.Run) (map[string]any, error) {
	workspace, err := r.repo.GetMaintainerWorkspace(ctx, job.WorkspaceID)
	if err != nil {
		return nil, err
	}
	profile, _ := r.repo.GetAgentProfile(ctx, job.AgentID)
	plan, provider := r.bootstrapPlan(ctx, profile, workspace)
	run.Provider = provider
	pages, pageIDs, err := r.applyWorkspacePages(ctx, workspace, "", plan.Pages, "bootstrap")
	if err != nil {
		return nil, err
	}
	workspace.Schema = mergeMaps(workspace.Schema, plan.Schema)
	workspace.IndexPageID = firstPageIDByTitle(pages, "Knowledge Index", workspace.IndexPageID)
	workspace.LogPageID = firstPageIDByTitle(pages, "Knowledge Log", workspace.LogPageID)
	workspace.UpdatedAt = time.Now().UTC()
	if err := r.repo.SaveMaintainerWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	if _, err := r.updateIndexAndLog(ctx, workspace, "Bootstrap initialized shared wiki workspace."); err != nil {
		return nil, err
	}
	snapshot, err := r.saveScopeSnapshot(ctx, workspace.ScopeKind, workspace.ScopeID, "", map[string]any{"workspace_id": workspace.ID, "compiler": "maintainer_bootstrap_v1"})
	if err != nil {
		return nil, err
	}
	produced := append(append([]string(nil), pageIDs...), snapshot.ID)
	return map[string]any{
		"workspace_id":   workspace.ID,
		"snapshot_id":    snapshot.ID,
		"produced_ids":   produced,
		"page_count":     len(pageIDs),
		"workspace_mode": workspace.Mode,
	}, nil
}

func (r *Runner) processFeedbackJob(ctx context.Context, job maintainerdomain.Job, run *maintainerdomain.Run) (map[string]any, error) {
	workspace, err := r.repo.GetMaintainerWorkspace(ctx, job.WorkspaceID)
	if err != nil {
		return nil, err
	}
	record, err := r.repo.GetFeedbackRecord(ctx, job.FeedbackID)
	if err != nil {
		return nil, err
	}
	sessionContext := responseScopedFeedbackText(record)
	if record.IsResponseScoped() && job.SessionID != "" {
		if events, err := r.repo.ListEvents(ctx, job.SessionID); err == nil {
			sessionContext = feedbackMaintainerContext(record, events)
		}
	}
	run.ResponseID = record.ResponseID
	run.InputSummary = map[string]any{
		"feedback_id":  record.ID,
		"response_id":  record.ResponseID,
		"session_id":   record.SessionID,
		"execution_id": record.ExecutionID,
		"trace_id":     record.TraceID,
	}
	if job.Mode == maintainerdomain.ModeCustomerMemory {
		return r.applyCustomerMemory(ctx, workspace, job, sessionContext, "feedback", run)
	}
	update, provider := r.sharedFeedbackPlan(ctx, workspace, record, sessionContext)
	run.Provider = provider
	if update.NeedsReview {
		proposalID, err := r.saveKnowledgeProposal(ctx, workspace, record, update)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"proposal_id":   proposalID,
			"produced_ids":  []string{proposalID},
			"review_needed": true,
			"response_id":   record.ResponseID,
		}, nil
	}
	pages, pageIDs, err := r.applyWorkspacePages(ctx, workspace, "", update.Pages, "feedback")
	if err != nil {
		return nil, err
	}
	if _, err := r.updateIndexAndLog(ctx, workspace, "Applied low-risk knowledge edits from operator feedback."); err != nil {
		return nil, err
	}
	snapshot, err := r.saveScopeSnapshot(ctx, workspace.ScopeKind, workspace.ScopeID, "", map[string]any{"workspace_id": workspace.ID, "source": "feedback"})
	if err != nil {
		return nil, err
	}
	workspace.IndexPageID = firstPageIDByTitle(pages, "Knowledge Index", workspace.IndexPageID)
	workspace.LogPageID = firstPageIDByTitle(pages, "Knowledge Log", workspace.LogPageID)
	workspace.UpdatedAt = time.Now().UTC()
	_ = r.repo.SaveMaintainerWorkspace(ctx, workspace)
	produced := append(append([]string(nil), pageIDs...), snapshot.ID)
	return map[string]any{"snapshot_id": snapshot.ID, "produced_ids": produced, "response_id": record.ResponseID}, nil
}

func (r *Runner) processSessionJob(ctx context.Context, job maintainerdomain.Job, run *maintainerdomain.Run) (map[string]any, error) {
	workspace, err := r.repo.GetMaintainerWorkspace(ctx, job.WorkspaceID)
	if err != nil {
		return nil, err
	}
	sess, err := r.repo.GetSession(ctx, job.SessionID)
	if err != nil {
		return nil, err
	}
	events, err := r.repo.ListEvents(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	signals, _ := r.repo.ListDerivedSignals(ctx, sess.ID)
	_ = knowledgelearning.New(r.repo).LearnFromSession(ctx, sess, execution.TurnExecution{}, events, signals)
	text := transcriptSummary(events)
	return r.applyCustomerMemory(ctx, workspace, job, text, "session", run)
}

func (r *Runner) applyCustomerMemory(ctx context.Context, workspace maintainerdomain.Workspace, job maintainerdomain.Job, sourceText, source string, run *maintainerdomain.Run) (map[string]any, error) {
	update, provider := r.customerMemoryPlan(ctx, workspace, sourceText)
	run.Provider = provider
	pages, pageIDs, err := r.applyWorkspacePages(ctx, workspace, "", update.Pages, source)
	if err != nil {
		return nil, err
	}
	if _, err := r.updateIndexAndLog(ctx, workspace, fmt.Sprintf("Updated customer memory from %s.", source)); err != nil {
		return nil, err
	}
	snapshot, err := r.saveScopeSnapshot(ctx, workspace.ScopeKind, workspace.ScopeID, "", map[string]any{"workspace_id": workspace.ID, "source": source})
	if err != nil {
		return nil, err
	}
	workspace.IndexPageID = firstPageIDByTitle(pages, "Customer Memory Index", workspace.IndexPageID)
	workspace.LogPageID = firstPageIDByTitle(pages, "Customer Memory Log", workspace.LogPageID)
	workspace.UpdatedAt = time.Now().UTC()
	_ = r.repo.SaveMaintainerWorkspace(ctx, workspace)
	var prefIDs []string
	if job.CustomerID != "" {
		prefs, _ := r.repo.ListCustomerPreferences(ctx, customer.PreferenceQuery{AgentID: job.AgentID, CustomerID: job.CustomerID, Limit: 100})
		for _, pref := range prefs {
			prefIDs = append(prefIDs, pref.ID)
		}
	}
	produced := append(append([]string(nil), pageIDs...), snapshot.ID)
	produced = append(produced, prefIDs...)
	return map[string]any{"snapshot_id": snapshot.ID, "produced_ids": produced, "preference_ids": prefIDs}, nil
}

func (r *Runner) runSourceMaintenance(ctx context.Context, source knowledge.Source, requestedBy string) (maintainerdomain.Run, knowledge.Snapshot, error) {
	service := r.service
	workspace, err := service.ensureWorkspace(ctx, source.ScopeKind, source.ScopeID, maintainerdomain.ModeSharedWiki)
	if err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	now := time.Now().UTC()
	job := maintainerdomain.Job{
		ID:          stableID("mjob", workspace.ID, maintainerdomain.TriggerSourceSync, source.ID, now.Format(time.RFC3339Nano)),
		WorkspaceID: workspace.ID,
		ScopeKind:   source.ScopeKind,
		ScopeID:     source.ScopeID,
		Mode:        maintainerdomain.ModeSharedWiki,
		Trigger:     maintainerdomain.TriggerSourceSync,
		Status:      maintainerdomain.StatusRunning,
		RequestedBy: requestedBy,
		SourceID:    source.ID,
		CreatedAt:   now,
		StartedAt:   &now,
	}
	run := maintainerdomain.Run{
		ID:          stableID("mrun", job.ID, now.Format(time.RFC3339Nano)),
		JobID:       job.ID,
		WorkspaceID: workspace.ID,
		ScopeKind:   source.ScopeKind,
		ScopeID:     source.ScopeID,
		Mode:        maintainerdomain.ModeSharedWiki,
		Trigger:     maintainerdomain.TriggerSourceSync,
		Status:      maintainerdomain.StatusRunning,
		CreatedAt:   now,
		StartedAt:   &now,
	}
	job.RunID = run.ID
	if err := r.repo.SaveMaintainerJob(ctx, job); err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	if err := r.repo.SaveMaintainerRun(ctx, run); err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	update, provider, parseOK := r.sourceMaintenancePlan(ctx, source, workspace)
	run.Provider = provider
	var snapshot knowledge.Snapshot
	if parseOK && update.NeedsReview {
		proposalID, err := r.saveSourceProposal(ctx, workspace, source, update)
		if err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		run.OutputSummary = map[string]any{"proposal_id": proposalID, "produced_ids": []string{proposalID}, "review_needed": true}
	} else if parseOK && len(update.Pages) > 0 {
		pages, pageIDs, err := r.applyWorkspacePages(ctx, workspace, source.ID, update.Pages, "source_sync")
		if err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		if _, err := r.updateIndexAndLog(ctx, workspace, "Applied shared wiki updates from source ingestion."); err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		snapshot, err = r.saveScopeSnapshot(ctx, workspace.ScopeKind, workspace.ScopeID, "", map[string]any{"workspace_id": workspace.ID, "source_id": source.ID, "compiler": "maintainer_source_v1"})
		if err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		workspace.IndexPageID = firstPageIDByTitle(pages, "Knowledge Index", workspace.IndexPageID)
		workspace.LogPageID = firstPageIDByTitle(pages, "Knowledge Log", workspace.LogPageID)
		workspace.UpdatedAt = time.Now().UTC()
		_ = r.repo.SaveMaintainerWorkspace(ctx, workspace)
		run.OutputSummary = map[string]any{"snapshot_id": snapshot.ID, "produced_ids": append(pageIDs, snapshot.ID)}
	} else {
		snapshot, err = knowledgecompiler.NewWithEmbedder(r.repo, r.router).CompileFolder(ctx, knowledgecompiler.Input{
			ScopeKind: source.ScopeKind,
			ScopeID:   source.ScopeID,
			SourceID:  source.ID,
			Root:      source.URI,
		})
		if err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		if _, err := r.updateIndexAndLog(ctx, workspace, "Compiled source into fallback source_summary pages."); err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		snapshot, err = r.saveScopeSnapshot(ctx, workspace.ScopeKind, workspace.ScopeID, "", map[string]any{"workspace_id": workspace.ID, "source_id": source.ID, "compiler": "maintainer_source_fallback_v1"})
		if err != nil {
			return maintainerdomain.Run{}, knowledge.Snapshot{}, err
		}
		run.OutputSummary = map[string]any{"snapshot_id": snapshot.ID, "produced_ids": []string{snapshot.ID}, "fallback": true}
	}
	source.Status = "ready"
	source.UpdatedAt = time.Now().UTC()
	source.Checksum, _ = knowledgeSourceChecksum(source)
	source.Metadata = mergeMaps(source.Metadata, map[string]any{"last_snapshot_id": snapshot.ID, "workspace_id": workspace.ID, "maintainer_run_id": run.ID})
	if err := r.repo.SaveKnowledgeSource(ctx, source); err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	done := time.Now().UTC()
	job.Status = maintainerdomain.StatusSucceeded
	job.FinishedAt = &done
	run.Status = maintainerdomain.StatusSucceeded
	run.FinishedAt = &done
	if err := r.repo.SaveMaintainerRun(ctx, run); err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	if err := r.repo.SaveMaintainerJob(ctx, job); err != nil {
		return maintainerdomain.Run{}, knowledge.Snapshot{}, err
	}
	return run, snapshot, nil
}

type workspacePlan struct {
	Schema map[string]any
	Pages  []pageEdit
}

type wikiUpdate struct {
	NeedsReview bool
	Rationale   string
	Pages       []pageEdit
}

type pageEdit struct {
	Title     string               `json:"title"`
	PageType  string               `json:"page_type"`
	Body      string               `json:"body"`
	Citations []knowledge.Citation `json:"citations,omitempty"`
}

func (r *Runner) bootstrapPlan(ctx context.Context, profile agent.Profile, workspace maintainerdomain.Workspace) (workspacePlan, string) {
	type structuredBootstrap struct {
		Schema map[string]any `json:"schema"`
		Pages  []pageEdit     `json:"pages"`
	}
	prompt := fmt.Sprintf(`Return strict JSON with fields schema and pages.
Create an initial knowledge workspace for this agent.
Agent name: %s
Agent description: %s
Workspace mode: %s
Default policy bundle: %s
Pages must include an overview, an index, and a log page.`, profile.Name, profile.Description, workspace.Mode, profile.DefaultPolicyBundleID)
	var out structuredBootstrap
	provider := ""
	if resp, ok := generateStructured(ctx, r.router, prompt, &out); ok {
		provider = resp.Provider
		if len(out.Pages) > 0 {
			return workspacePlan{Schema: out.Schema, Pages: out.Pages}, provider
		}
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = profile.ID
	}
	return workspacePlan{
		Schema: mergeMaps(defaultWorkspaceSchema(workspace.ScopeKind, workspace.ScopeID, workspace.Mode), map[string]any{"agent_name": name}),
		Pages: []pageEdit{
			{Title: "Overview", PageType: "workspace_overview", Body: fmt.Sprintf("# Overview\n\nThis workspace supports the %s agent.\n\n## Domain\n%s", name, strings.TrimSpace(profile.Description))},
			{Title: "Knowledge Index", PageType: "workspace_index", Body: "# Knowledge Index\n\n- Overview"},
			{Title: "Knowledge Log", PageType: "workspace_log", Body: "# Knowledge Log\n\n- Workspace initialized."},
		},
	}, provider
}

func (r *Runner) sharedFeedbackPlan(ctx context.Context, workspace maintainerdomain.Workspace, record feedback.Record, contextText string) (wikiUpdate, string) {
	type structuredFeedback struct {
		NeedsReview bool       `json:"needs_review"`
		Rationale   string     `json:"rationale"`
		Pages       []pageEdit `json:"pages"`
	}
	prompt := fmt.Sprintf(`Return strict JSON with fields needs_review, rationale, pages.
Use this operator feedback to update shared wiki pages if it is factual knowledge.
If it implies risky or unclear edits, set needs_review true.
Feedback category: %s
Feedback text: %s`, record.Category, contextText)
	var out structuredFeedback
	provider := ""
	if resp, ok := generateStructured(ctx, r.router, prompt, &out); ok {
		provider = resp.Provider
		if len(out.Pages) > 0 || out.NeedsReview {
			return wikiUpdate{NeedsReview: out.NeedsReview, Rationale: out.Rationale, Pages: out.Pages}, provider
		}
	}
	if looksSharedKnowledgeFeedback(record) {
		return wikiUpdate{
			NeedsReview: true,
			Rationale:   "feedback requires operator review before shared wiki mutation",
			Pages: []pageEdit{{
				Title:    "Operator Feedback Review",
				PageType: "review_note",
				Body:     "# Operator Feedback Review\n\n" + strings.TrimSpace(contextText),
			}},
		}, provider
	}
	return wikiUpdate{Pages: []pageEdit{{Title: "Customer Support Notes", PageType: "feedback_note", Body: "# Customer Support Notes\n\n" + strings.TrimSpace(contextText)}}}, provider
}

func (r *Runner) customerMemoryPlan(ctx context.Context, workspace maintainerdomain.Workspace, sourceText string) (wikiUpdate, string) {
	type structuredMemory struct {
		Pages []pageEdit `json:"pages"`
	}
	prompt := fmt.Sprintf(`Return strict JSON with field pages.
Summarize durable customer memory from this material.
Create or update a customer profile page, an index page, and a memory log page.
Text:
%s`, truncateForPrompt(sourceText, 12000))
	var out structuredMemory
	provider := ""
	if resp, ok := generateStructured(ctx, r.router, prompt, &out); ok {
		provider = resp.Provider
		if len(out.Pages) > 0 {
			return wikiUpdate{Pages: out.Pages}, provider
		}
	}
	return wikiUpdate{Pages: []pageEdit{
		{Title: "Customer Profile", PageType: "customer_memory_profile", Body: "# Customer Profile\n\n" + strings.TrimSpace(sourceText)},
		{Title: "Customer Memory Index", PageType: "workspace_index", Body: "# Customer Memory Index\n\n- Customer Profile"},
		{Title: "Customer Memory Log", PageType: "workspace_log", Body: "# Customer Memory Log\n\n- Customer memory updated."},
	}}, provider
}

func (r *Runner) sourceMaintenancePlan(ctx context.Context, source knowledge.Source, workspace maintainerdomain.Workspace) (wikiUpdate, string, bool) {
	type structuredSource struct {
		NeedsReview bool       `json:"needs_review"`
		Rationale   string     `json:"rationale"`
		Pages       []pageEdit `json:"pages"`
	}
	raw, err := readSourcePreview(source.URI)
	if err != nil {
		return wikiUpdate{}, "", false
	}
	prompt := fmt.Sprintf(`Return strict JSON with fields needs_review, rationale, pages.
You are maintaining a shared wiki from raw source documents.
Workspace mode: %s
Source URI: %s
Update or create curated wiki pages from the source content. Use citations if possible.
Source preview:
%s`, workspace.Mode, source.URI, truncateForPrompt(raw, 18000))
	var out structuredSource
	resp, ok := generateStructured(ctx, r.router, prompt, &out)
	if !ok {
		return wikiUpdate{}, "", false
	}
	return wikiUpdate{NeedsReview: out.NeedsReview, Rationale: out.Rationale, Pages: out.Pages}, resp.Provider, true
}

func (r *Runner) applyWorkspacePages(ctx context.Context, workspace maintainerdomain.Workspace, sourceID string, edits []pageEdit, source string) ([]knowledge.Page, []string, error) {
	now := time.Now().UTC()
	existing, err := r.repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: workspace.ScopeKind, ScopeID: workspace.ScopeID, Limit: 10000})
	if err != nil {
		return nil, nil, err
	}
	byTitle := map[string]knowledge.Page{}
	for _, item := range existing {
		byTitle[strings.ToLower(strings.TrimSpace(item.Title))] = item
	}
	var pages []knowledge.Page
	var pageIDs []string
	for _, edit := range edits {
		title := strings.TrimSpace(edit.Title)
		body := strings.TrimSpace(edit.Body)
		if title == "" || body == "" {
			continue
		}
		key := strings.ToLower(title)
		page, ok := byTitle[key]
		if !ok {
			page = knowledge.Page{
				ID:        stableID("kpage", workspace.ScopeKind, workspace.ScopeID, workspace.Mode, title),
				ScopeKind: workspace.ScopeKind,
				ScopeID:   workspace.ScopeID,
				CreatedAt: now,
			}
		}
		page.SourceID = sourceID
		page.Title = title
		page.Body = body
		page.PageType = firstNonEmpty(edit.PageType, page.PageType, "wiki_page")
		page.Citations = append([]knowledge.Citation(nil), edit.Citations...)
		page.Metadata = mergeMaps(page.Metadata, map[string]any{
			"workspace_id":      workspace.ID,
			"maintained_by":     "closed_loop_agent",
			"maintainer_source": source,
		})
		page.Checksum = checksum(body)
		page.UpdatedAt = now
		chunks, err := chunksForPage(ctx, r.router, page)
		if err != nil {
			return nil, nil, err
		}
		if err := r.repo.SaveKnowledgePage(ctx, page, chunks); err != nil {
			return nil, nil, err
		}
		pages = append(pages, page)
		pageIDs = append(pageIDs, page.ID)
	}
	return pages, pageIDs, nil
}

func (r *Runner) updateIndexAndLog(ctx context.Context, workspace maintainerdomain.Workspace, logEntry string) ([]string, error) {
	pages, err := r.repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: workspace.ScopeKind, ScopeID: workspace.ScopeID, Limit: 10000})
	if err != nil {
		return nil, err
	}
	sort.Slice(pages, func(i, j int) bool { return strings.ToLower(pages[i].Title) < strings.ToLower(pages[j].Title) })
	var entries []string
	for _, page := range pages {
		if page.PageType == "workspace_log" || page.PageType == "workspace_index" {
			continue
		}
		entries = append(entries, "- "+page.Title)
	}
	indexTitle := "Knowledge Index"
	logTitle := "Knowledge Log"
	if workspace.Mode == maintainerdomain.ModeCustomerMemory {
		indexTitle = "Customer Memory Index"
		logTitle = "Customer Memory Log"
	}
	indexBody := "# " + indexTitle + "\n\n" + strings.Join(entries, "\n")
	logBody := "# " + logTitle + "\n\n- " + strings.TrimSpace(logEntry)
	_, ids, err := r.applyWorkspacePages(ctx, workspace, "", []pageEdit{
		{Title: indexTitle, PageType: "workspace_index", Body: indexBody},
		{Title: logTitle, PageType: "workspace_log", Body: logBody},
	}, "index_log")
	return ids, err
}

func (r *Runner) saveScopeSnapshot(ctx context.Context, scopeKind, scopeID, proposalID string, metadata map[string]any) (knowledge.Snapshot, error) {
	pages, err := r.repo.ListKnowledgePages(ctx, knowledge.PageQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 10000})
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	chunks, err := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 10000})
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	now := time.Now().UTC()
	pageIDs := make([]string, 0, len(pages))
	for _, page := range pages {
		pageIDs = append(pageIDs, page.ID)
	}
	chunkIDs := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		chunkIDs = append(chunkIDs, chunk.ID)
	}
	sort.Strings(pageIDs)
	sort.Strings(chunkIDs)
	meta := mergeMaps(metadata, map[string]any{
		"proposal_id":   proposalID,
		"snapshot_kind": "maintainer",
	})
	snapshot := knowledge.Snapshot{
		ID:        stableID("ksnap", scopeKind, scopeID, strings.Join(pageIDs, ","), strings.Join(chunkIDs, ","), now.Format(time.RFC3339Nano)),
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		PageIDs:   pageIDs,
		ChunkIDs:  chunkIDs,
		Metadata:  meta,
		CreatedAt: now,
	}
	return snapshot, r.repo.SaveKnowledgeSnapshot(ctx, snapshot)
}

func (r *Runner) saveKnowledgeProposal(ctx context.Context, workspace maintainerdomain.Workspace, record feedback.Record, update wikiUpdate) (string, error) {
	now := time.Now().UTC()
	proposal := knowledge.UpdateProposal{
		ID:        stableID("kprop", workspace.ID, record.ID, now.Format(time.RFC3339Nano)),
		ScopeKind: workspace.ScopeKind,
		ScopeID:   workspace.ScopeID,
		Kind:      "workspace_page_update",
		State:     "draft",
		Rationale: firstNonEmpty(update.Rationale, "maintainer requested review"),
		Payload: map[string]any{
			"pages":       update.Pages,
			"feedback_id": record.ID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return proposal.ID, r.repo.SaveKnowledgeUpdateProposal(ctx, proposal)
}

func (r *Runner) saveSourceProposal(ctx context.Context, workspace maintainerdomain.Workspace, source knowledge.Source, update wikiUpdate) (string, error) {
	now := time.Now().UTC()
	proposal := knowledge.UpdateProposal{
		ID:        stableID("kprop", workspace.ID, source.ID, now.Format(time.RFC3339Nano)),
		ScopeKind: workspace.ScopeKind,
		ScopeID:   workspace.ScopeID,
		Kind:      "workspace_page_update",
		State:     "draft",
		Rationale: firstNonEmpty(update.Rationale, "maintainer requested review"),
		Payload: map[string]any{
			"pages":     update.Pages,
			"source_id": source.ID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return proposal.ID, r.repo.SaveKnowledgeUpdateProposal(ctx, proposal)
}

func generateStructured[T any](ctx context.Context, router *model.Router, prompt string, out *T) (model.Response, bool) {
	if router == nil {
		return model.Response{}, false
	}
	resp, err := router.Generate(ctx, model.CapabilityStructured, model.Request{Prompt: prompt})
	if err != nil {
		return model.Response{}, false
	}
	raw := strings.TrimSpace(resp.Text)
	raw = strings.TrimPrefix(raw, "provider stub:")
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}
	if raw == "" {
		return resp, false
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return resp, false
	}
	return resp, true
}

func chunksForPage(ctx context.Context, router *model.Router, page knowledge.Page) ([]knowledge.Chunk, error) {
	citation := knowledge.Citation{SourceID: page.SourceID, Title: page.Title}
	chunks := splitChunks(page, citation, page.UpdatedAt)
	if router == nil || len(chunks) == 0 {
		return chunks, nil
	}
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.Text)
	}
	embedded, err := router.Embed(ctx, texts)
	if err != nil {
		return chunks, nil
	}
	for i := range chunks {
		if i < len(embedded.Vectors) {
			chunks[i].Vector = append([]float32(nil), embedded.Vectors[i]...)
		}
	}
	return chunks, nil
}

func splitChunks(page knowledge.Page, citation knowledge.Citation, now time.Time) []knowledge.Chunk {
	paragraphs := strings.Split(page.Body, "\n\n")
	var out []knowledge.Chunk
	for i, para := range paragraphs {
		text := strings.TrimSpace(para)
		if text == "" {
			continue
		}
		out = append(out, knowledge.Chunk{
			ID:        stableID("kchunk", page.ID, fmt.Sprint(i), checksum(text)),
			PageID:    page.ID,
			ScopeKind: page.ScopeKind,
			ScopeID:   page.ScopeID,
			Text:      text,
			Citations: []knowledge.Citation{citation},
			Metadata:  map[string]any{"page_title": page.Title},
			CreatedAt: now,
		})
	}
	if len(out) == 0 {
		out = append(out, knowledge.Chunk{
			ID:        stableID("kchunk", page.ID, "0", page.Checksum),
			PageID:    page.ID,
			ScopeKind: page.ScopeKind,
			ScopeID:   page.ScopeID,
			Text:      page.Body,
			Citations: []knowledge.Citation{citation},
			Metadata:  map[string]any{"page_title": page.Title},
			CreatedAt: now,
		})
	}
	return out
}

func transcriptSummary(events []session.Event) string {
	var parts []string
	for _, event := range events {
		text := strings.TrimSpace(sessionEventText(event))
		if text == "" {
			continue
		}
		parts = append(parts, strings.ToUpper(event.Source)+": "+text)
	}
	return strings.Join(parts, "\n\n")
}

func defaultWorkspaceSchema(scopeKind, scopeID, mode string) map[string]any {
	return map[string]any{
		"scope_kind": scopeKind,
		"scope_id":   scopeID,
		"mode":       mode,
		"page_types": []string{"workspace_overview", "workspace_index", "workspace_log", "wiki_page"},
	}
}

func sharedScopeForProfile(profile agent.Profile) (string, string) {
	if strings.TrimSpace(profile.DefaultKnowledgeScopeKind) != "" && strings.TrimSpace(profile.DefaultKnowledgeScopeID) != "" {
		return profile.DefaultKnowledgeScopeKind, profile.DefaultKnowledgeScopeID
	}
	return "agent", profile.ID
}

func sharedScopeForSession(sess session.Session) (string, string) {
	return "agent", sess.AgentID
}

func customerMemoryScopeForSession(sess session.Session) (string, string) {
	return "customer_agent", strings.TrimSpace(sess.AgentID) + ":" + strings.TrimSpace(sess.CustomerID)
}

func looksSharedKnowledgeFeedback(record feedback.Record) bool {
	text := strings.ToLower(strings.TrimSpace(record.Category + " " + responseScopedFeedbackText(record) + " " + strings.Join(record.Labels, " ")))
	return strings.Contains(text, "knowledge") || strings.Contains(text, "factual") || strings.Contains(text, "policy")
}

func shouldUseSharedWikiFeedback(record feedback.Record) bool {
	if looksSharedKnowledgeFeedback(record) {
		return true
	}
	if record.IsResponseScoped() && strings.TrimSpace(record.Correction) != "" && !hasPreferenceSignal(record) {
		return true
	}
	return false
}

func shouldQueueMaintainerFeedback(record feedback.Record) bool {
	if !record.IsResponseScoped() {
		return strings.TrimSpace(record.LearningText()) != ""
	}
	if strings.TrimSpace(record.Text) != "" || strings.TrimSpace(record.Comment) != "" || strings.TrimSpace(record.Correction) != "" {
		return true
	}
	return false
}

func hasPreferenceSignal(record feedback.Record) bool {
	text := strings.ToLower(strings.TrimSpace(record.LearningText()))
	return strings.Contains(text, "i prefer ") ||
		strings.Contains(text, "call me ") ||
		strings.Contains(text, "my name is ") ||
		strings.Contains(text, "reply in ") ||
		strings.Contains(text, "respond in ") ||
		strings.Contains(text, "send me updates") ||
		strings.Contains(text, "contact me") ||
		strings.Contains(text, "reach me")
}

func responseScopedFeedbackText(record feedback.Record) string {
	text := strings.TrimSpace(record.LearningText())
	if !record.IsResponseScoped() {
		return text
	}
	var parts []string
	if text != "" {
		parts = append(parts, text)
	}
	if record.ResponseID != "" {
		parts = append(parts, "Target response: "+record.ResponseID)
	}
	if record.Score != nil {
		parts = append(parts, fmt.Sprintf("Score: %d", *record.Score))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func feedbackMaintainerContext(record feedback.Record, events []session.Event) string {
	base := responseScopedFeedbackText(record)
	if !record.IsResponseScoped() || len(record.TargetEventIDs) == 0 {
		return base
	}
	byID := map[string]session.Event{}
	for _, item := range events {
		byID[item.ID] = item
	}
	var snippets []string
	for _, eventID := range record.TargetEventIDs {
		item, ok := byID[eventID]
		if !ok {
			continue
		}
		text := strings.TrimSpace(sessionEventText(item))
		if text == "" {
			continue
		}
		snippets = append(snippets, fmt.Sprintf("%s event %s: %s", item.Source, item.ID, text))
	}
	if len(snippets) == 0 {
		return base
	}
	if base == "" {
		return "Targeted response context:\n" + strings.Join(snippets, "\n")
	}
	return base + "\n\nTargeted response context:\n" + strings.Join(snippets, "\n")
}

func truncateForPrompt(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max]
}

func readSourcePreview(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(root)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	var parts []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return walkErr
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
		parts = append(parts, "FILE: "+rel+"\n"+string(raw))
		return nil
	})
	return strings.Join(parts, "\n\n"), err
}

func knowledgeSourceChecksum(source knowledge.Source) (string, error) {
	path := strings.TrimSpace(source.URI)
	if path == "" {
		return "", fmt.Errorf("knowledge source uri is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		return checksum(string(raw)), nil
	}
	var parts []string
	if err := filepath.Walk(abs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return walkErr
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, path)
		parts = append(parts, rel+"\x00"+checksum(string(raw)))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(parts)
	return checksum(strings.Join(parts, "\x00")), nil
}

func checksum(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func stableID(prefix string, parts ...string) string {
	return prefix + "_" + checksum(strings.Join(parts, "\x00"))[:16]
}

func firstNonEmpty(values ...string) string {
	for _, item := range values {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func mergeMaps(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func firstPageIDByTitle(pages []knowledge.Page, title, fallback string) string {
	for _, page := range pages {
		if strings.EqualFold(strings.TrimSpace(page.Title), title) {
			return page.ID
		}
	}
	return fallback
}

func sessionEventText(event session.Event) string {
	var parts []string
	for _, part := range event.Content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}
