package runner

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	knowledgeenrichment "github.com/sahal/parmesan/internal/knowledge/enrichment"
	knowledgelearning "github.com/sahal/parmesan/internal/knowledge/learning"
	"github.com/sahal/parmesan/internal/model"
	rolloutengine "github.com/sahal/parmesan/internal/rollout"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/toolruntime"
)

type Runner struct {
	repo       store.Repository
	writes     *asyncwrite.Queue
	broker     *sse.Broker
	router     *model.Router
	invoker    *toolruntime.Invoker
	sessions   *sessionsvc.Service
	leaseOwner string
	leaseTTL   time.Duration
	interval   time.Duration
}

var errApprovalRequired = errors.New("approval required")

func New(repo store.Repository, writes *asyncwrite.Queue, broker *sse.Broker, router *model.Router, leaseOwner string) *Runner {
	return &Runner{
		repo:       repo,
		writes:     writes,
		broker:     broker,
		router:     router,
		invoker:    toolruntime.New(),
		sessions:   sessionsvc.New(repo, writes),
		leaseOwner: leaseOwner,
		leaseTTL:   10 * time.Second,
		interval:   500 * time.Millisecond,
	}
}

type resolvedView = policyruntime.EngineResult

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
	r.retryFailedMediaAssets(ctx, time.Now().UTC())
	executions, err := r.repo.ListRunnableExecutions(ctx, time.Now().UTC())
	if err != nil {
		return
	}
	for _, exec := range executions {
		_ = r.processExecution(ctx, exec.ID)
	}
}

func (r *Runner) processExecution(ctx context.Context, executionID string) error {
	exec, steps, err := r.repo.GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if exec.Status == execution.StatusSucceeded || exec.Status == execution.StatusFailed {
		return nil
	}
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return err
	}
	if !hasEvent(events, exec.TriggerEventID) {
		return nil
	}

	exec.LeaseOwner = r.leaseOwner
	exec.LeaseExpiresAt = time.Now().UTC().Add(r.leaseTTL)
	exec.Status = execution.StatusRunning
	exec.UpdatedAt = time.Now().UTC()
	if err := r.repo.UpdateExecution(ctx, exec); err != nil {
		return err
	}

	for _, step := range steps {
		if step.Status == execution.StatusSucceeded {
			continue
		}
		if step.Status == execution.StatusRunning && step.LeaseExpiresAt.After(time.Now().UTC()) && step.LeaseOwner != "" && step.LeaseOwner != r.leaseOwner {
			return nil
		}

		step.Status = execution.StatusRunning
		step.Attempt++
		step.LeaseOwner = r.leaseOwner
		step.LeaseExpiresAt = time.Now().UTC().Add(r.leaseTTL)
		if step.StartedAt.IsZero() {
			step.StartedAt = time.Now().UTC()
		}
		step.UpdatedAt = time.Now().UTC()
		if err := r.repo.UpdateExecutionStep(ctx, step); err != nil {
			return err
		}

		r.publish(exec.SessionID, exec.ID, "runtime.step.started", map[string]any{"step": step.Name})
		err := r.executeStep(ctx, &exec, &step)
		if err != nil {
			if errors.Is(err, errApprovalRequired) {
				step.Status = execution.StatusBlocked
				step.LastError = err.Error()
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusBlocked
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Time{}
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				return nil
			}
			if step.Recomputable && step.Attempt < 3 && isRetryableExecutionError(err) {
				step.Status = execution.StatusPending
				step.LastError = err.Error()
				step.LeaseOwner = ""
				step.LeaseExpiresAt = time.Now().UTC().Add(1 * time.Second)
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusRunning
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Now().UTC().Add(1 * time.Second)
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				r.appendTrace(ctx, audit.Record{
					ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
					Kind:        "execution.retry_scheduled",
					SessionID:   exec.SessionID,
					ExecutionID: exec.ID,
					TraceID:     exec.TraceID,
					Message:     err.Error(),
					Fields:      map[string]any{"step": step.Name, "attempt": step.Attempt},
					CreatedAt:   time.Now().UTC(),
				})
				return nil
			}
			step.Status = execution.StatusFailed
			step.LastError = err.Error()
			step.FinishedAt = time.Now().UTC()
			step.UpdatedAt = time.Now().UTC()
			_ = r.repo.UpdateExecutionStep(ctx, step)
			exec.Status = execution.StatusFailed
			exec.UpdatedAt = time.Now().UTC()
			_ = r.repo.UpdateExecution(ctx, exec)
			r.appendTrace(ctx, audit.Record{
				ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
				Kind:        "execution.failed",
				SessionID:   exec.SessionID,
				ExecutionID: exec.ID,
				TraceID:     exec.TraceID,
				Message:     err.Error(),
				Fields:      map[string]any{"step": step.Name},
				CreatedAt:   time.Now().UTC(),
			})
			return err
		}

		step.Status = execution.StatusSucceeded
		step.FinishedAt = time.Now().UTC()
		step.UpdatedAt = time.Now().UTC()
		if err := r.repo.UpdateExecutionStep(ctx, step); err != nil {
			return err
		}
		r.publish(exec.SessionID, exec.ID, "runtime.step.completed", map[string]any{"step": step.Name})
	}

	exec.Status = execution.StatusSucceeded
	exec.UpdatedAt = time.Now().UTC()
	if err := r.repo.UpdateExecution(ctx, exec); err != nil {
		return err
	}
	return r.learnFromExecution(ctx, exec)
}

func (r *Runner) executeStep(ctx context.Context, exec *execution.TurnExecution, step *execution.ExecutionStep) error {
	switch step.Name {
	case "ingest":
		events, err := r.repo.ListEvents(ctx, exec.SessionID)
		if err != nil {
			return err
		}
		if err := r.ingestMediaAssets(ctx, events); err != nil {
			return err
		}
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "execution.ingest", "completed", exec.ID, exec.TraceID, map[string]any{
			"step": "ingest",
		}, nil, false); err != nil {
			return err
		}
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "execution.ingest",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "ingest checkpoint persisted",
			CreatedAt:   time.Now().UTC(),
		})
		return nil
	case "resolve_policy":
		view, _, err := r.resolveView(ctx, *exec)
		if err != nil {
			return err
		}
		journeyDecision := view.JourneyProgressStage.Decision
		toolDecision := view.ToolDecisionStage.Decision
		if view.Bundle != nil {
			exec.PolicyBundleID = view.Bundle.ID
			exec.UpdatedAt = time.Now().UTC()
			if err := r.repo.UpdateExecution(ctx, *exec); err != nil {
				return err
			}
		}
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "policy.resolved",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "resolved policy view for turn",
			Fields: map[string]any{
				"bundle_id":             exec.PolicyBundleID,
				"proposal_id":           exec.ProposalID,
				"rollout_id":            exec.RolloutID,
				"selection_reason":      exec.SelectionReason,
				"matched_observations":  idsFromObservations(view.ObservationStage.Observations),
				"matched_guidelines":    idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines),
				"suppressed_guidelines": suppressedIDs(view.SuppressedGuidelines),
				"journey_id":            journeyID(view.ActiveJourney),
				"journey_state":         journeyStateID(view.ActiveJourneyState),
				"journey_decision":      journeyDecision,
				"exposed_tools":         append([]string(nil), view.ToolExposureStage.ExposedTools...),
				"selected_tool":         toolDecision.SelectedTool,
				"tool_can_run":          toolDecision.CanRun,
				"tool_missing_args":     toolDecision.MissingArguments,
				"tool_invalid_args":     toolDecision.InvalidArguments,
				"reapply_decisions":     view.PreviouslyAppliedStage.Decisions,
				"customer_decisions":    view.CustomerDependencyStage.Decisions,
				"response_analysis":     view.ResponseAnalysisStage.Analysis,
				"composition_mode":      view.CompositionMode,
				"soul_hash":             bundleSoulHash(view.Bundle),
				"preference_hash":       preferenceHash(view.CustomerPreferences),
				"arq_results":           view.ARQResults,
			},
			CreatedAt: time.Now().UTC(),
		})
		_, _ = r.sessions.UpsertSessionMetadata(ctx, exec.SessionID, map[string]any{
			"last_trace_id":           exec.TraceID,
			"applied_guideline_ids":   idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines),
			"active_journey_id":       journeyID(view.ActiveJourney),
			"active_journey_state_id": journeyStateID(view.ActiveJourneyState),
			"composition_mode":        view.CompositionMode,
			"knowledge_snapshot_id":   view.RetrieverStage.KnowledgeSnapshotID,
			"soul_hash":               bundleSoulHash(view.Bundle),
			"preference_hash":         preferenceHash(view.CustomerPreferences),
			"retriever_result_hashes": retrieverResultHashes(view),
		})
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "policy.resolved", "completed", exec.ID, exec.TraceID, map[string]any{
			"bundle_id":          exec.PolicyBundleID,
			"composition_mode":   view.CompositionMode,
			"matched_guidelines": idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines),
		}, nil, false); err != nil {
			return err
		}
		return nil
	case "match_and_plan":
		view, _, err := r.resolveView(ctx, *exec)
		if err != nil {
			return err
		}
		journeyDecision := view.JourneyProgressStage.Decision
		toolDecision := view.ToolDecisionStage.Decision
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "runtime.plan",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "planned response and tool exposure",
			Fields: map[string]any{
				"active_journey":        journeyID(view.ActiveJourney),
				"active_state":          journeyStateID(view.ActiveJourneyState),
				"candidate_templates":   templateIDs(view.ResponseAnalysisStage.CandidateTemplates),
				"disambiguation_prompt": view.DisambiguationPrompt,
				"exposed_tools":         append([]string(nil), view.ToolExposureStage.ExposedTools...),
				"selected_tool":         toolDecision.SelectedTool,
				"tool_can_run":          toolDecision.CanRun,
				"tool_missing_args":     toolDecision.MissingArguments,
				"tool_invalid_args":     toolDecision.InvalidArguments,
				"reapply_decisions":     view.PreviouslyAppliedStage.Decisions,
				"customer_decisions":    view.CustomerDependencyStage.Decisions,
				"response_analysis":     view.ResponseAnalysisStage.Analysis,
				"journey_decision":      journeyDecision,
			},
			CreatedAt: time.Now().UTC(),
		})
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "runtime.plan", "completed", exec.ID, exec.TraceID, map[string]any{
			"candidate_templates": templateIDs(view.ResponseAnalysisStage.CandidateTemplates),
			"selected_tool":       toolDecision.SelectedTool,
		}, nil, false); err != nil {
			return err
		}
		return nil
	case "compose_response":
		view, events, err := r.resolveView(ctx, *exec)
		if err != nil {
			return err
		}
		toolOutput, err := r.maybeRunTool(ctx, *exec, view)
		if err != nil {
			return err
		}
		respText := renderResponse(view, toolOutput)
		if respText == "" {
			prompt := composePrompt(view, events, toolOutput)
			resp, err := r.router.Generate(ctx, model.CapabilityReasoning, model.Request{Prompt: prompt})
			if err != nil {
				return err
			}
			respText = resp.Text
		}
		verification := policyruntime.VerifyDraft(view, respText, toolOutput)
		switch verification.Status {
		case "revise", "block":
			if strings.TrimSpace(verification.Replacement) != "" {
				respText = verification.Replacement
			}
		}
		assistantEvent, err := r.sessions.CreateMessageEvent(ctx, exec.SessionID, "ai_agent", respText, exec.ID, exec.TraceID, map[string]any{
			"step": "compose_response",
		}, false)
		if err != nil {
			return err
		}
		journeyDecision := view.JourneyProgressStage.Decision
		if view.JourneyInstance != nil && view.ActiveJourney != nil && view.ActiveJourneyState != nil {
			next := policyruntime.AdvanceJourney(view.JourneyInstance, view.ActiveJourneyState, view.ActiveJourney, journeyDecision)
			if next != nil {
				if err := r.repo.UpsertJourneyInstance(ctx, *next); err != nil {
					return err
				}
			}
		}
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "execution.compose",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "assistant response composed",
			Fields: map[string]any{
				"event_id":      assistantEvent.ID,
				"journey_state": journeyStateID(view.ActiveJourneyState),
				"tool_output":   toolOutput,
				"verification":  verification,
			},
			CreatedAt: time.Now().UTC(),
		})
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "response.composed", "completed", exec.ID, exec.TraceID, map[string]any{
			"event_id": assistantEvent.ID,
		}, nil, false); err != nil {
			return err
		}
		r.publish(exec.SessionID, exec.ID, "runtime.response.delta", map[string]any{"text": respText})
		return nil
	case "deliver_response":
		events, err := r.repo.ListEvents(ctx, exec.SessionID)
		if err != nil {
			return err
		}
		last := latestAssistant(events)
		if last.ID != "" {
			if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "response.delivered", "queued", exec.ID, exec.TraceID, map[string]any{
				"event_id": last.ID,
			}, nil, false); err != nil {
				return err
			}
			r.publish(exec.SessionID, exec.ID, "runtime.response.completed", map[string]any{"event_id": last.ID, "status": "queued_for_gateway"})
		}
		return nil
	default:
		if strings.HasPrefix(step.Name, "run_tool:") {
			run := toolrun.Run{
				ID:             fmt.Sprintf("toolrun_%d", time.Now().UnixNano()),
				ExecutionID:    exec.ID,
				ToolID:         strings.TrimPrefix(step.Name, "run_tool:"),
				Status:         "completed",
				IdempotencyKey: exec.ID + "_" + step.Name,
				OutputJSON:     `{"status":"stubbed"}`,
				CreatedAt:      time.Now().UTC(),
			}
			return r.repo.SaveToolRun(ctx, run)
		}
		return nil
	}
}

func (r *Runner) resolveView(ctx context.Context, exec execution.TurnExecution) (resolvedView, []session.Event, error) {
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return resolvedView{}, nil, err
	}
	bundles, err := r.repo.ListBundles(ctx)
	if err != nil {
		return resolvedView{}, nil, err
	}
	sess, err := r.repo.GetSession(ctx, exec.SessionID)
	if err != nil {
		return resolvedView{}, nil, err
	}
	proposals, err := r.repo.ListProposals(ctx)
	if err != nil {
		return resolvedView{}, nil, err
	}
	rollouts, err := r.repo.ListRollouts(ctx)
	if err != nil {
		return resolvedView{}, nil, err
	}
	journeys, err := r.repo.ListJourneyInstances(ctx, exec.SessionID)
	if err != nil {
		return resolvedView{}, nil, err
	}
	catalog, err := r.repo.ListCatalogEntries(ctx)
	if err != nil {
		return resolvedView{}, nil, err
	}
	profile := r.agentProfile(ctx, sess.AgentID)
	defaultBundleID := exec.PolicyBundleID
	if defaultBundleID == "" && profile.ID != "" {
		defaultBundleID = profile.DefaultPolicyBundleID
	}
	selection := rolloutengine.SelectBundle(sess, proposals, rollouts, defaultBundleID)
	selectedBundles := selectPolicyBundles(bundles, selection.BundleID, defaultBundleID)
	knowledgeSnapshot, knowledgeChunks := r.resolveKnowledgeSnapshot(ctx, sess, profile, selectedBundles)
	derivedSignals := r.derivedSignalText(ctx, exec.SessionID)
	view, err := policyruntime.ResolveWithOptions(ctx, events, selectedBundles, journeys, catalog, policyruntime.ResolveOptions{
		Router:            r.router,
		KnowledgeSearcher: r.repo,
		KnowledgeSnapshot: knowledgeSnapshot,
		KnowledgeChunks:   knowledgeChunks,
		DerivedSignals:    derivedSignals,
	})
	if err != nil {
		return resolvedView{}, nil, err
	}
	view.CustomerPreferences = r.customerPreferences(ctx, sess)
	resolvedBundleID := selection.BundleID
	if resolvedBundleID == "" && len(selectedBundles) > 0 {
		resolvedBundleID = selectedBundles[0].ID
	}
	if resolvedBundleID != "" && (exec.PolicyBundleID != resolvedBundleID || exec.SelectionReason != selection.Reason || exec.ProposalID != selection.ProposalID || exec.RolloutID != selection.RolloutID) {
		exec.PolicyBundleID = resolvedBundleID
		exec.ProposalID = selection.ProposalID
		exec.RolloutID = selection.RolloutID
		exec.SelectionReason = selection.Reason
		exec.UpdatedAt = time.Now().UTC()
		if err := r.repo.UpdateExecution(ctx, exec); err != nil {
			return resolvedView{}, nil, err
		}
	}
	if view.JourneyInstance != nil && view.JourneyInstance.SessionID == "" {
		view.JourneyInstance.SessionID = exec.SessionID
	}
	if view.JourneyInstance != nil {
		if err := r.repo.UpsertJourneyInstance(ctx, *view.JourneyInstance); err != nil {
			return resolvedView{}, nil, err
		}
	}
	return view, events, nil
}

func selectPolicyBundles(bundles []policy.Bundle, preferred string, fallback string) []policy.Bundle {
	if preferred != "" {
		for _, item := range bundles {
			if item.ID == preferred {
				return []policy.Bundle{item}
			}
		}
	}
	if fallback != "" {
		for _, item := range bundles {
			if item.ID == fallback {
				return []policy.Bundle{item}
			}
		}
	}
	if len(bundles) == 0 {
		return nil
	}
	return []policy.Bundle{bundles[0]}
}

func (r *Runner) agentProfile(ctx context.Context, agentID string) agent.Profile {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return agent.Profile{}
	}
	profile, err := r.repo.GetAgentProfile(ctx, agentID)
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

func (r *Runner) resolveKnowledgeSnapshot(ctx context.Context, sess session.Session, profile agent.Profile, bundles []policy.Bundle) (*knowledge.Snapshot, []knowledge.Chunk) {
	var snapshots []knowledge.Snapshot
	var chunks []knowledge.Chunk
	customerScopeKind, customerScopeID := customerKnowledgeScope(sess)
	if customerScopeID != "" {
		customerSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, Limit: 1})
		if err == nil && len(customerSnapshots) > 0 {
			snapshots = append(snapshots, customerSnapshots[0])
			customerChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, SnapshotID: customerSnapshots[0].ID})
			chunks = append(chunks, customerChunks...)
		}
	}
	scopeKind, scopeID := knowledgeScope(sess, bundles)
	if scopeID != "" {
		sharedSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1})
		if err == nil && len(sharedSnapshots) > 0 {
			snapshots = append(snapshots, sharedSnapshots[0])
			sharedChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, SnapshotID: sharedSnapshots[0].ID})
			chunks = append(chunks, sharedChunks...)
		}
	}
	if len(snapshots) == 0 && strings.TrimSpace(profile.DefaultKnowledgeScopeKind) != "" && strings.TrimSpace(profile.DefaultKnowledgeScopeID) != "" {
		profileSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: profile.DefaultKnowledgeScopeKind, ScopeID: profile.DefaultKnowledgeScopeID, Limit: 1})
		if err == nil && len(profileSnapshots) > 0 {
			snapshots = append(snapshots, profileSnapshots[0])
			profileChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: profile.DefaultKnowledgeScopeKind, ScopeID: profile.DefaultKnowledgeScopeID, SnapshotID: profileSnapshots[0].ID})
			chunks = append(chunks, profileChunks...)
		}
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	snapshot := snapshots[0]
	return &snapshot, chunks
}

func knowledgeScope(sess session.Session, bundles []policy.Bundle) (string, string) {
	if strings.TrimSpace(sess.AgentID) != "" {
		return "agent", strings.TrimSpace(sess.AgentID)
	}
	if len(bundles) > 0 && strings.TrimSpace(bundles[0].ID) != "" {
		return "bundle", strings.TrimSpace(bundles[0].ID)
	}
	return "", ""
}

func customerKnowledgeScope(sess session.Session) (string, string) {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return "", ""
	}
	return "customer_agent", strings.TrimSpace(sess.AgentID) + ":" + strings.TrimSpace(sess.CustomerID)
}

func (r *Runner) customerPreferences(ctx context.Context, sess session.Session) []customer.Preference {
	if strings.TrimSpace(sess.AgentID) == "" || strings.TrimSpace(sess.CustomerID) == "" {
		return nil
	}
	items, err := r.repo.ListCustomerPreferences(ctx, customer.PreferenceQuery{
		AgentID:    strings.TrimSpace(sess.AgentID),
		CustomerID: strings.TrimSpace(sess.CustomerID),
		Status:     "active",
	})
	if err != nil {
		return nil
	}
	return items
}

func (r *Runner) derivedSignalText(ctx context.Context, sessionID string) []string {
	signals, err := r.repo.ListDerivedSignals(ctx, sessionID)
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

func (r *Runner) ingestMediaAssets(ctx context.Context, events []session.Event) error {
	now := time.Now().UTC()
	existingAssets, err := r.repo.ListMediaAssets(ctx, "")
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(existingAssets))
	for _, asset := range existingAssets {
		seen[mediaAssetID(asset.EventID, asset.PartIndex)] = struct{}{}
	}
	for _, event := range events {
		for i, part := range event.Content {
			if part.Type == "" || part.Type == "text" {
				continue
			}
			assetID := mediaAssetID(event.ID, i)
			if _, ok := seen[assetID]; ok {
				continue
			}
			asset := media.Asset{
				ID:        assetID,
				SessionID: event.SessionID,
				EventID:   event.ID,
				PartIndex: i,
				Type:      part.Type,
				URL:       part.URL,
				MimeType:  fmt.Sprint(part.Meta["mime_type"]),
				Status:    "pending",
				Metadata:  cloneAnyMap(part.Meta),
				CreatedAt: now,
			}
			if err := r.repo.SaveMediaAsset(ctx, asset); err != nil {
				return err
			}
			if err := r.processMediaAsset(ctx, event, &asset, part, false); err != nil {
				return err
			}
			seen[assetID] = struct{}{}
		}
	}
	return nil
}

func mediaAssetID(eventID string, partIndex int) string {
	return fmt.Sprintf("media_%s_%d", eventID, partIndex)
}

func (r *Runner) retryFailedMediaAssets(ctx context.Context, now time.Time) {
	assets, err := r.repo.ListMediaAssets(ctx, "")
	if err != nil {
		return
	}
	for _, asset := range assets {
		if asset.Status != "failed" {
			continue
		}
		if !shouldRetryMediaAsset(asset, now) {
			continue
		}
		event, err := r.sessions.ReadEvent(ctx, asset.SessionID, asset.EventID)
		if err != nil {
			continue
		}
		if asset.PartIndex < 0 || asset.PartIndex >= len(event.Content) {
			continue
		}
		traceID := strings.TrimSpace(event.TraceID)
		if traceID == "" {
			traceID = traceIDForExecution(asset.EventID, event.SessionID)
		}
		r.appendTrace(ctx, audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "media.retry.started",
			SessionID: asset.SessionID,
			TraceID:   traceID,
			Message:   "media enrichment retry started",
			Fields: map[string]any{
				"asset_id":    asset.ID,
				"event_id":    asset.EventID,
				"part_index":  asset.PartIndex,
				"type":        asset.Type,
				"retry_count": mediaRetryCount(asset.Metadata),
			},
			CreatedAt: time.Now().UTC(),
		})
		_ = r.processMediaAsset(ctx, event, &asset, event.Content[asset.PartIndex], true)
	}
}

func (r *Runner) processMediaAsset(ctx context.Context, event session.Event, asset *media.Asset, part session.ContentPart, isRetry bool) error {
	traceID := strings.TrimSpace(event.TraceID)
	if traceID == "" {
		traceID = traceIDForExecution(asset.EventID, event.SessionID)
	}
	signals, err := knowledgeenrichment.ForPart(part.Type).Enrich(ctx, event, *asset, part)
	if err != nil {
		asset.Status = "failed"
		asset.EnrichedAt = time.Now().UTC()
		asset.Metadata["error"] = err.Error()
		asset.Metadata["enrichment_status"] = "failed"
		retryCount := mediaRetryCount(asset.Metadata) + 1
		asset.Metadata["retry_count"] = retryCount
		asset.Metadata["next_retry_at"] = nextMediaRetryAt(time.Now().UTC(), retryCount).Format(time.RFC3339Nano)
		if isRetry {
			asset.Metadata["last_retry_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if saveErr := r.repo.SaveMediaAsset(ctx, *asset); saveErr != nil {
			return saveErr
		}
		r.appendTrace(ctx, audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "media.enrichment.failed",
			SessionID: asset.SessionID,
			TraceID:   traceID,
			Message:   "media enrichment failed",
			Fields: map[string]any{
				"asset_id":          asset.ID,
				"event_id":          asset.EventID,
				"part_index":        asset.PartIndex,
				"type":              asset.Type,
				"error":             err.Error(),
				"retry_count":       retryCount,
				"next_retry_at":     asset.Metadata["next_retry_at"],
				"enrichment_status": asset.Metadata["enrichment_status"],
				"is_retry":          isRetry,
			},
			CreatedAt: time.Now().UTC(),
		})
		return nil
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
		if err := r.repo.SaveDerivedSignal(ctx, signal); err != nil {
			return err
		}
	}
	asset.Status = "succeeded"
	asset.EnrichedAt = time.Now().UTC()
	asset.Metadata["enrichment_status"] = "succeeded"
	if len(extractors) > 0 {
		names := make([]string, 0, len(extractors))
		for name := range extractors {
			names = append(names, name)
		}
		sort.Strings(names)
		asset.Metadata["extractors"] = names
	}
	if len(providers) > 0 {
		names := make([]string, 0, len(providers))
		for name := range providers {
			names = append(names, name)
		}
		sort.Strings(names)
		asset.Metadata["providers"] = names
	}
	if len(requestIDs) > 0 {
		names := make([]string, 0, len(requestIDs))
		for name := range requestIDs {
			names = append(names, name)
		}
		sort.Strings(names)
		asset.Metadata["request_ids"] = names
	}
	if maxLatency > 0 {
		asset.Metadata["latency_ms"] = maxLatency
	}
	delete(asset.Metadata, "error")
	delete(asset.Metadata, "next_retry_at")
	if isRetry {
		asset.Metadata["last_retry_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := r.repo.SaveMediaAsset(ctx, *asset); err != nil {
		return err
	}
	r.appendTrace(ctx, audit.Record{
		ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:      "media.enrichment.succeeded",
		SessionID: asset.SessionID,
		TraceID:   traceID,
		Message:   "media enrichment succeeded",
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
			"is_retry":          isRetry,
		},
		CreatedAt: time.Now().UTC(),
	})
	return nil
}

func shouldRetryMediaAsset(asset media.Asset, now time.Time) bool {
	if asset.Status != "failed" {
		return false
	}
	if mediaRetryCount(asset.Metadata) >= 3 {
		return false
	}
	raw := strings.TrimSpace(fmt.Sprint(asset.Metadata["next_retry_at"]))
	if raw == "" {
		return true
	}
	next, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return true
	}
	return !next.After(now)
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

func nextMediaRetryAt(now time.Time, retryCount int) time.Time {
	backoff := time.Duration(retryCount*30) * time.Second
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	return now.Add(backoff)
}

func traceIDForExecution(eventID, sessionID string) string {
	sum := sha1.Sum([]byte(eventID + ":" + sessionID))
	return "trace_" + hex.EncodeToString(sum[:8])
}

func (r *Runner) learnFromExecution(ctx context.Context, exec execution.TurnExecution) error {
	sess, err := r.repo.GetSession(ctx, exec.SessionID)
	if err != nil {
		return err
	}
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return err
	}
	signals, err := r.repo.ListDerivedSignals(ctx, exec.SessionID)
	if err != nil {
		return err
	}
	return knowledgelearning.New(r.repo).LearnFromSession(ctx, sess, exec, events, signals)
}

func (r *Runner) maybeRunTool(ctx context.Context, exec execution.TurnExecution, view resolvedView) (map[string]any, error) {
	toolPlan := view.ToolPlanStage.Plan
	toolDecision := view.ToolDecisionStage.Decision
	if len(toolPlan.Calls) > 0 {
		outputs := map[string]map[string]any{}
		calls := append([]policyruntime.ToolPlannedCall(nil), toolPlan.Calls...)
		plannedByTool := map[string]struct{}{}
		for _, call := range calls {
			plannedByTool[strings.TrimSpace(call.ToolID)] = struct{}{}
		}
		for _, toolName := range dedupeStrings(toolPlan.SelectedTools) {
			if _, ok := plannedByTool[strings.TrimSpace(toolName)]; ok {
				continue
			}
			calls = append(calls, policyruntime.ToolPlannedCall{ToolID: toolName})
		}
		for _, call := range calls {
			entry, ok := findCatalogEntry(r.repo, call.ToolID)
			if !ok {
				continue
			}
			decision := decisionForPlannedCall(view, call)
			output, err := r.runSingleTool(ctx, exec, view, entry, decision)
			if err != nil {
				return nil, err
			}
			if output != nil {
				key := entry.ProviderID + ":" + entry.Name
				if _, exists := outputs[key]; !exists {
					outputs[key] = output
				} else {
					outputs[key+"#"+hashArguments(decision.Arguments)] = output
				}
			}
		}
		if len(outputs) == 0 {
			return nil, nil
		}
		if len(outputs) == 1 {
			for _, output := range outputs {
				return output, nil
			}
		}
		return map[string]any{"tools": outputs}, nil
	}
	selectedTools := dedupeStrings(toolPlan.SelectedTools)
	if len(selectedTools) == 0 {
		entry, ok := r.selectTool(view)
		if !ok {
			return nil, nil
		}
		return r.runSingleTool(ctx, exec, view, entry, toolDecision)
	}
	outputs := map[string]map[string]any{}
	for _, toolName := range selectedTools {
		entry, ok := findCatalogEntry(r.repo, toolName)
		if !ok {
			continue
		}
		decision := decisionForTool(view, entry.Name)
		output, err := r.runSingleTool(ctx, exec, view, entry, decision)
		if err != nil {
			return nil, err
		}
		if output != nil {
			outputs[entry.ProviderID+":"+entry.Name] = output
		}
	}
	if len(outputs) == 0 {
		return nil, nil
	}
	if len(outputs) == 1 {
		for _, output := range outputs {
			return output, nil
		}
	}
	return map[string]any{"tools": outputs}, nil
}

func (r *Runner) runSingleTool(ctx context.Context, exec execution.TurnExecution, view resolvedView, entry tool.CatalogEntry, decision policyruntime.ToolDecision) (map[string]any, error) {
	if !decision.CanRun {
		payload := map[string]any{
			"tool":              entry.Name,
			"status":            "cannot_run",
			"missing_arguments": append([]string(nil), decision.MissingArguments...),
			"invalid_arguments": append([]string(nil), decision.InvalidArguments...),
			"missing_issues":    append([]policyruntime.ToolArgumentIssue(nil), decision.MissingIssues...),
			"invalid_issues":    append([]policyruntime.ToolArgumentIssue(nil), decision.InvalidIssues...),
		}
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "tool.run.blocked",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "tool selected but cannot run with current arguments",
			Fields:      payload,
			CreatedAt:   time.Now().UTC(),
		})
		_, _ = r.sessions.CreateToolEvent(ctx, exec.SessionID, "runtime", "tool.blocked", exec.ID, exec.TraceID, map[string]any{
			"tool_id":           entry.ID,
			"reason":            "cannot_run",
			"missing_arguments": append([]string(nil), decision.MissingArguments...),
			"invalid_arguments": append([]string(nil), decision.InvalidArguments...),
			"missing_issues":    append([]policyruntime.ToolArgumentIssue(nil), decision.MissingIssues...),
			"invalid_issues":    append([]policyruntime.ToolArgumentIssue(nil), decision.InvalidIssues...),
		}, map[string]any{"provider_id": entry.ProviderID}, false)
		r.publish(exec.SessionID, exec.ID, "runtime.tool.blocked", payload)
		return payload, nil
	}
	status, err := r.approvalStatus(ctx, exec, entry, view)
	if err != nil {
		return nil, err
	}
	switch status {
	case string(approval.StatusPending):
		return nil, errApprovalRequired
	case string(approval.StatusRejected):
		return map[string]any{"approval": "rejected"}, nil
	}
	binding, err := r.repo.GetProvider(ctx, entry.ProviderID)
	if err != nil {
		return nil, err
	}
	auth, err := r.repo.GetProviderAuthBinding(ctx, entry.ProviderID)
	if err != nil {
		auth = tool.AuthBinding{}
	}
	idempotencyKey := fmt.Sprintf("%s_%s_%s", exec.ID, entry.ID, hashArguments(decision.Arguments))
	if output, ok := r.reuseToolRun(ctx, exec.ID, entry.ID, idempotencyKey); ok {
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "tool.run.reused",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     "reused previously completed tool run",
			Fields:      map[string]any{"tool": entry.Name, "provider_id": entry.ProviderID},
			CreatedAt:   time.Now().UTC(),
		})
		_, _ = r.sessions.CreateToolEvent(ctx, exec.SessionID, "runtime", "tool.completed", exec.ID, exec.TraceID, map[string]any{
			"tool_id":     entry.ID,
			"provider_id": entry.ProviderID,
			"output":      output,
			"reused":      true,
		}, nil, false)
		r.publish(exec.SessionID, exec.ID, "runtime.tool.completed", map[string]any{"tool": entry.Name, "output": output, "reused": true})
		return output, nil
	}
	run := toolrun.Run{
		ID:             fmt.Sprintf("toolrun_%d", time.Now().UnixNano()),
		ExecutionID:    exec.ID,
		ToolID:         entry.ID,
		Status:         "running",
		IdempotencyKey: idempotencyKey,
		InputJSON:      mustJSON(decision.Arguments),
		CreatedAt:      time.Now().UTC(),
	}
	if err := r.repo.SaveToolRun(ctx, run); err != nil {
		return nil, err
	}
	_, _ = r.sessions.CreateToolEvent(ctx, exec.SessionID, "runtime", "tool.started", exec.ID, exec.TraceID, map[string]any{
		"tool_id":     entry.ID,
		"provider_id": entry.ProviderID,
		"arguments":   cloneAnyMap(decision.Arguments),
	}, nil, false)
	r.publish(exec.SessionID, exec.ID, "runtime.tool.started", map[string]any{"tool": entry.Name})
	output, err := r.invoker.Invoke(ctx, binding, auth, entry, decision.Arguments)
	if err != nil {
		run.Status = "failed"
		run.OutputJSON = mustJSON(toolErrorPayload(err))
		_ = r.repo.SaveToolRun(ctx, run)
		r.appendTrace(ctx, audit.Record{
			ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:        "tool.run.failed",
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Message:     err.Error(),
			Fields:      toolErrorPayload(err),
			CreatedAt:   time.Now().UTC(),
		})
		_, _ = r.sessions.CreateToolEvent(ctx, exec.SessionID, "runtime", "tool.failed", exec.ID, exec.TraceID, map[string]any{
			"tool_id":     entry.ID,
			"provider_id": entry.ProviderID,
			"error":       err.Error(),
			"error_class": toolErrorPayload(err)["error_class"],
			"retryable":   toolErrorPayload(err)["retryable"],
			"status":      toolErrorPayload(err)["status"],
		}, nil, false)
		return nil, err
	}
	run.Status = "succeeded"
	run.OutputJSON = mustJSON(output)
	if err := r.repo.SaveToolRun(ctx, run); err != nil {
		return nil, err
	}
	r.appendTrace(ctx, audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        "tool.run",
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     "tool executed for turn",
		Fields:      map[string]any{"tool": entry.Name, "provider_id": entry.ProviderID},
		CreatedAt:   time.Now().UTC(),
	})
	_, _ = r.sessions.CreateToolEvent(ctx, exec.SessionID, "runtime", "tool.completed", exec.ID, exec.TraceID, map[string]any{
		"tool_id":     entry.ID,
		"provider_id": entry.ProviderID,
		"output":      output,
	}, nil, false)
	r.publish(exec.SessionID, exec.ID, "runtime.tool.completed", map[string]any{"tool": entry.Name, "output": output})
	return output, nil
}

func decisionForTool(view resolvedView, toolName string) policyruntime.ToolDecision {
	toolDecision := view.ToolDecisionStage.Decision
	toolPlan := view.ToolPlanStage.Plan
	if strings.TrimSpace(toolDecision.SelectedTool) == strings.TrimSpace(toolName) {
		return toolDecision
	}
	for _, candidate := range toolPlan.Candidates {
		if strings.TrimSpace(candidate.ToolID) != strings.TrimSpace(toolName) {
			continue
		}
		decision := policyruntime.ToolDecision{
			SelectedTool: toolName,
			Arguments:    cloneAnyMap(candidate.Arguments),
			CanRun:       !candidate.AlreadyStaged && !candidate.AlreadySatisfied && len(candidate.MissingIssues) == 0 && len(candidate.InvalidIssues) == 0,
			MissingIssues: append([]policyruntime.ToolArgumentIssue(nil),
				candidate.MissingIssues...),
			InvalidIssues: append([]policyruntime.ToolArgumentIssue(nil),
				candidate.InvalidIssues...),
			Rationale: firstNonEmptyString(
				candidate.SelectionRationale,
				candidate.PreparationRationale,
				candidate.Rationale,
			),
			Grounded:         candidate.Grounded,
			ApprovalRequired: strings.EqualFold(candidate.ApprovalMode, "required"),
		}
		for _, issue := range decision.MissingIssues {
			decision.MissingArguments = append(decision.MissingArguments, issue.Parameter)
		}
		for _, issue := range decision.InvalidIssues {
			decision.InvalidArguments = append(decision.InvalidArguments, issue.Parameter)
		}
		if !decision.CanRun {
			decision.SelectedTool = ""
		}
		return decision
	}
	return policyruntime.ToolDecision{}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func decisionForPlannedCall(view resolvedView, call policyruntime.ToolPlannedCall) policyruntime.ToolDecision {
	decision := decisionForTool(view, call.ToolID)
	if len(decision.Arguments) == 0 && len(call.Arguments) > 0 {
		decision.Arguments = cloneAnyMap(call.Arguments)
	}
	if strings.TrimSpace(decision.SelectedTool) == "" {
		decision.SelectedTool = call.ToolID
	}
	if strings.TrimSpace(decision.Rationale) == "" {
		decision.Rationale = call.Rationale
	}
	return decision
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hashArguments(args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		raw = []byte(fmt.Sprint(args))
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:8])
}

func isRetryableExecutionError(err error) bool {
	var invokeErr *toolruntime.InvokeError
	if errors.As(err, &invokeErr) {
		return invokeErr.Retryable
	}
	return true
}

func toolErrorPayload(err error) map[string]any {
	payload := map[string]any{"error": err.Error()}
	var invokeErr *toolruntime.InvokeError
	if errors.As(err, &invokeErr) {
		payload["error_class"] = invokeErr.Class
		payload["retryable"] = invokeErr.Retryable
		if invokeErr.Status != 0 {
			payload["status"] = invokeErr.Status
		}
	}
	return payload
}

func (r *Runner) reuseToolRun(ctx context.Context, executionID string, toolID string, idempotencyKey string) (map[string]any, bool) {
	runs, err := r.repo.ListToolRuns(ctx, executionID)
	if err != nil {
		return nil, false
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.ToolID != toolID || run.IdempotencyKey != idempotencyKey || run.Status != "succeeded" {
			continue
		}
		if strings.TrimSpace(run.OutputJSON) == "" {
			return map[string]any{}, true
		}
		var output map[string]any
		if err := json.Unmarshal([]byte(run.OutputJSON), &output); err != nil {
			return map[string]any{"raw": run.OutputJSON}, true
		}
		return output, true
	}
	return nil, false
}

func (r *Runner) approvalStatus(ctx context.Context, exec execution.TurnExecution, entry tool.CatalogEntry, view resolvedView) (string, error) {
	toolApprovals := view.ToolExposureStage.ToolApprovals
	approvalMode := toolApprovals[entry.ID]
	if approvalMode == "" {
		approvalMode = toolApprovals[entry.Name]
	}
	if approvalMode == "" {
		approvalMode = toolApprovals[entry.ProviderID+"."+entry.Name]
	}
	if !strings.EqualFold(approvalMode, "required") {
		return "", nil
	}
	approvals, err := r.repo.ListApprovalSessions(ctx, exec.SessionID)
	if err != nil {
		return "", err
	}
	for _, item := range approvals {
		if item.ExecutionID == exec.ID && item.ToolID == entry.ID {
			if item.Status == approval.StatusPending && item.ExpiresAt.Before(time.Now().UTC()) {
				item.Status = approval.StatusExpired
				item.UpdatedAt = time.Now().UTC()
				_ = r.repo.SaveApprovalSession(ctx, item)
				return string(approval.StatusExpired), nil
			}
			return string(item.Status), nil
		}
	}
	now := time.Now().UTC()
	item := approval.Session{
		ID:          fmt.Sprintf("approval_%d", now.UnixNano()),
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		ToolID:      entry.ID,
		Status:      approval.StatusPending,
		RequestText: "Approval required before running " + entry.Name,
		ExpiresAt:   now.Add(15 * time.Minute),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := r.repo.SaveApprovalSession(ctx, item); err != nil {
		return "", err
	}
	r.appendTrace(ctx, audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        "approval.requested",
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     item.RequestText,
		Fields:      map[string]any{"approval_id": item.ID, "tool": entry.Name},
		CreatedAt:   time.Now().UTC(),
	})
	_, _ = r.sessions.CreateApprovalRequestedEvent(ctx, exec.SessionID, "runtime", exec.ID, exec.TraceID, item.ID, entry.ID, item.RequestText, item.ExpiresAt, map[string]any{
		"tool_name": entry.Name,
	}, false)
	r.publish(exec.SessionID, exec.ID, "approval.requested", map[string]any{"approval_id": item.ID, "tool": entry.Name, "message": item.RequestText})
	return string(approval.StatusPending), nil
}

func (r *Runner) selectTool(view resolvedView) (tool.CatalogEntry, bool) {
	name := strings.TrimSpace(view.ToolDecisionStage.Decision.SelectedTool)
	if name == "" || view.Bundle == nil {
		return tool.CatalogEntry{}, false
	}
	// resolveView already filtered catalog for exposure; re-list to map names.
	return findCatalogEntry(r.repo, name)
}

func findCatalogEntry(repo store.Repository, name string) (tool.CatalogEntry, bool) {
	entries, err := repo.ListCatalogEntries(context.Background())
	if err != nil {
		return tool.CatalogEntry{}, false
	}
	for _, entry := range entries {
		if entry.Name == name || entry.ProviderID+"."+entry.Name == name {
			return entry, true
		}
	}
	return tool.CatalogEntry{}, false
}

func composePrompt(view resolvedView, events []session.Event, toolOutput map[string]any) string {
	var parts []string
	if latest := latestText(events); latest != "" {
		parts = append(parts, "Customer message: "+latest)
	}
	guidelines := view.MatchFinalizeStage.MatchedGuidelines
	if len(guidelines) > 0 {
		instructions := make([]string, 0, len(guidelines))
		for _, item := range guidelines {
			if strings.TrimSpace(item.Then) != "" {
				instructions = append(instructions, item.Then)
			}
		}
		if len(instructions) > 0 {
			parts = append(parts, "Follow these guidelines: "+strings.Join(instructions, " "))
		}
	}
	if len(view.Attention.CriticalInstructionIDs) > 0 {
		parts = append(parts, "Critical policy IDs: "+strings.Join(view.Attention.CriticalInstructionIDs, ", "))
	}
	if prefs := customerPreferenceText(view.CustomerPreferences); prefs != "" {
		parts = append(parts, "Customer preferences (soft constraints):\n"+prefs)
	}
	if soul := soulPrompt(bundleSoul(view.Bundle)); soul != "" {
		parts = append(parts, "Agent SOUL style and brand rules:\n"+soul)
	}
	if view.ActiveJourneyState != nil && strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
		parts = append(parts, "Current journey instruction: "+view.ActiveJourneyState.Instruction)
	}
	if knowledge := retrievedKnowledgeText(view); knowledge != "" {
		parts = append(parts, "Retrieved knowledge:\n"+knowledge)
	}
	if toolDecision := view.ToolDecisionStage.Decision; toolDecision.SelectedTool != "" {
		parts = append(parts, "Selected tool: "+toolDecision.SelectedTool)
	}
	if len(toolOutput) > 0 {
		parts = append(parts, "Tool output: "+mustJSON(toolOutput))
	}
	return strings.Join(parts, "\n")
}

func customerPreferenceText(items []customer.Preference) string {
	var parts []string
	for _, item := range items {
		if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		parts = append(parts, item.Key+": "+item.Value)
	}
	return strings.Join(parts, "\n")
}

func preferenceHash(items []customer.Preference) string {
	if len(items) == 0 {
		return ""
	}
	type prefHashItem struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	values := make([]prefHashItem, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		values = append(values, prefHashItem{Key: item.Key, Value: item.Value})
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Key < values[j].Key
	})
	raw, err := json.Marshal(values)
	if err != nil || len(values) == 0 {
		return ""
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:8])
}

func soulPrompt(soul policy.Soul) string {
	var parts []string
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, label+": "+value)
		}
	}
	add("Identity", soul.Identity)
	add("Role", soul.Role)
	add("Brand", soul.Brand)
	add("Default language", soul.DefaultLanguage)
	if len(soul.SupportedLanguages) > 0 {
		parts = append(parts, "Supported languages: "+strings.Join(soul.SupportedLanguages, ", "))
	}
	add("Language matching", soul.LanguageMatching)
	add("Tone", soul.Tone)
	add("Formality", soul.Formality)
	add("Verbosity", soul.Verbosity)
	if len(soul.StyleRules) > 0 {
		parts = append(parts, "Style rules: "+strings.Join(soul.StyleRules, "; "))
	}
	if len(soul.AvoidRules) > 0 {
		parts = append(parts, "Avoid rules: "+strings.Join(soul.AvoidRules, "; "))
	}
	add("Escalation style", soul.EscalationStyle)
	if len(soul.FormattingRules) > 0 {
		parts = append(parts, "Formatting rules: "+strings.Join(soul.FormattingRules, "; "))
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, "Apply these as strong style guidance unless hard policy, approval requirements, strict templates, or explicit customer constraints conflict.")
	return strings.Join(parts, "\n")
}

func bundleSoul(bundle *policy.Bundle) policy.Soul {
	if bundle == nil {
		return policy.Soul{}
	}
	return bundle.Soul
}

func bundleSoulHash(bundle *policy.Bundle) string {
	return soulHash(bundleSoul(bundle))
}

func soulHash(soul policy.Soul) string {
	raw, err := json.Marshal(soul)
	if err != nil || string(raw) == "{}" {
		return ""
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:8])
}

func retrievedKnowledgeText(view resolvedView) string {
	var parts []string
	for _, item := range view.RetrieverStage.Results {
		if strings.TrimSpace(item.Data) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(item.Data))
	}
	return strings.Join(parts, "\n\n")
}

func retrieverResultHashes(view resolvedView) []string {
	var out []string
	for _, item := range view.RetrieverStage.Results {
		if strings.TrimSpace(item.ResultHash) != "" {
			out = append(out, item.ResultHash)
		}
	}
	return out
}

func journeyID(item *policy.Journey) string {
	if item == nil {
		return ""
	}
	return item.ID
}

func journeyStateID(item *policy.JourneyNode) string {
	if item == nil {
		return ""
	}
	return item.ID
}

func idsFromGuidelines(items []policy.Guideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func idsFromObservations(items []policy.Observation) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func templateIDs(items []policy.Template) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func suppressedIDs(items []policyruntime.SuppressedGuideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(raw)
}

func (r *Runner) publish(sessionID, executionID, typ string, payload any) {
	if r.broker == nil {
		return
	}
	r.broker.Publish(sessionID, sse.Envelope{
		EventID:     fmt.Sprintf("stream_%d", time.Now().UnixNano()),
		SessionID:   sessionID,
		ExecutionID: executionID,
		Type:        typ,
		Payload:     payload,
		CreatedAt:   time.Now().UTC(),
	})
}

func (r *Runner) appendTrace(ctx context.Context, record audit.Record) {
	if r.writes != nil {
		_ = r.writes.AppendAuditRecord(ctx, record)
		return
	}
	_ = r.repo.AppendAuditRecord(ctx, record)
}

func latestText(events []session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "customer" {
			continue
		}
		for _, part := range events[i].Content {
			if part.Type == "text" && part.Text != "" {
				return part.Text
			}
		}
	}
	return "hello"
}

func latestAssistant(events []session.Event) session.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source == "ai_agent" {
			return events[i]
		}
	}
	return session.Event{}
}

func hasEvent(events []session.Event, eventID string) bool {
	for _, event := range events {
		if event.ID == eventID {
			return true
		}
	}
	return false
}
