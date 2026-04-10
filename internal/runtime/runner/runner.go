package runner

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sahal/parmesan/internal/acppeer"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/policy"
	responsedomain "github.com/sahal/parmesan/internal/domain/response"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	knowledgecompiler "github.com/sahal/parmesan/internal/knowledge/compiler"
	knowledgeenrichment "github.com/sahal/parmesan/internal/knowledge/enrichment"
	knowledgelearning "github.com/sahal/parmesan/internal/knowledge/learning"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/observability"
	"github.com/sahal/parmesan/internal/quality"
	rolloutengine "github.com/sahal/parmesan/internal/rollout"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/sessionwatch"
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
	agentPeers *acppeer.Manager
	sessions   *sessionsvc.Service
	leaseOwner string
	leaseTTL   time.Duration
	interval   time.Duration
	responseMu sync.Mutex
	responses  map[string]responsedomain.Response
}

var errApprovalRequired = errors.New("approval required")
var errResponsePreparationExhausted = errors.New("response preparation exhausted")

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
		responses:  map[string]responsedomain.Response{},
	}
}

func (r *Runner) WithAgentPeers(peers *acppeer.Manager) *Runner {
	r.agentPeers = peers
	return r
}

type resolvedView = policyruntime.EngineResult

const maxResponsePreparationIterations = 4

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
	ctx, done := observability.Current().StartSpan(ctx, "runner", "process_execution")
	defer done("ok")
	exec, steps, err := r.repo.GetExecution(ctx, executionID)
	if err != nil {
		done("error")
		return err
	}
	responseRecord, _ := r.ensureResponseRecord(ctx, exec)
	if exec.Status == execution.StatusSucceeded || exec.Status == execution.StatusFailed || exec.Status == execution.StatusAbandoned {
		return nil
	}
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		done("error")
		return err
	}
	if !hasEvent(events, exec.TriggerEventID) {
		return nil
	}

	now := time.Now().UTC()
	if exec.Status == execution.StatusWaiting && exec.LeaseExpiresAt.After(now) {
		return nil
	}
	exec.LeaseOwner = r.leaseOwner
	exec.LeaseExpiresAt = now.Add(r.leaseTTL)
	exec.Status = execution.StatusRunning
	exec.BlockedReason = ""
	exec.ResumeSignal = ""
	exec.UpdatedAt = now
	if err := r.repo.UpdateExecution(ctx, exec); err != nil {
		done("error")
		return err
	}
	_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusProcessing, "", func(record *responsedomain.Response) {
		record.StartedAt = now
	})

	for _, step := range steps {
		if step.Status == execution.StatusSucceeded {
			continue
		}
		now = time.Now().UTC()
		if (step.Status == execution.StatusWaiting || step.Status == execution.StatusPending) && step.NextAttemptAt.After(now) {
			exec.Status = execution.StatusWaiting
			exec.LeaseOwner = ""
			exec.LeaseExpiresAt = step.NextAttemptAt
			exec.UpdatedAt = now
			_ = r.repo.UpdateExecution(ctx, exec)
			_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusBlocked, step.BlockedReason, nil)
			return nil
		}
		if step.Status == execution.StatusBlocked {
			exec.Status = execution.StatusBlocked
			exec.BlockedReason = step.BlockedReason
			exec.ResumeSignal = step.ResumeSignal
			exec.LeaseOwner = ""
			exec.LeaseExpiresAt = time.Time{}
			exec.UpdatedAt = now
			_ = r.repo.UpdateExecution(ctx, exec)
			return nil
		}
		if step.Status == execution.StatusRunning && step.LeaseExpiresAt.After(time.Now().UTC()) && step.LeaseOwner != "" && step.LeaseOwner != r.leaseOwner {
			return nil
		}

		step.Status = execution.StatusRunning
		step.Attempt++
		step.LeaseOwner = r.leaseOwner
		step.LeaseExpiresAt = time.Now().UTC().Add(r.leaseTTL)
		step.NextAttemptAt = time.Time{}
		step.BlockedReason = ""
		step.ResumeSignal = ""
		if step.StartedAt.IsZero() {
			step.StartedAt = time.Now().UTC()
		}
		step.UpdatedAt = time.Now().UTC()
		if err := r.repo.UpdateExecutionStep(ctx, step); err != nil {
			return err
		}

		r.publish(exec.SessionID, exec.ID, "runtime.step.started", map[string]any{"step": step.Name})
		stepCtx := policyruntime.WithStructuredRetryAttempts(ctx, stepMaxAttempts(step))
		err := r.executeStep(stepCtx, &exec, &step)
		if err != nil {
			if errors.Is(err, errApprovalRequired) {
				step.Status = execution.StatusBlocked
				step.LastError = err.Error()
				step.BlockedReason = execution.BlockedReasonApprovalRequired
				step.ResumeSignal = execution.ResumeSignalApproval
				step.LeaseOwner = ""
				step.LeaseExpiresAt = time.Time{}
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusBlocked
				exec.BlockedReason = execution.BlockedReasonApprovalRequired
				exec.ResumeSignal = execution.ResumeSignalApproval
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Time{}
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusBlocked, execution.BlockedReasonApprovalRequired, nil)
				return nil
			}
			if errors.Is(err, errResponsePreparationExhausted) {
				step.Status = execution.StatusBlocked
				step.LastError = err.Error()
				step.BlockedReason = "response_preparation_exhausted"
				step.LeaseOwner = ""
				step.LeaseExpiresAt = time.Time{}
				step.FinishedAt = time.Now().UTC()
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusBlocked
				exec.BlockedReason = "response_preparation_exhausted"
				exec.ResumeSignal = "operator_retry"
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Time{}
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusBlocked, "response_preparation_exhausted", nil)
				return nil
			}
			if step.Recomputable && stepRetryBudgetAllows(step) && isRetryableExecutionError(err) {
				nextAttempt := time.Now().UTC().Add(stepBackoff(step))
				step.Status = execution.StatusWaiting
				step.LastError = err.Error()
				step.RetryReason = retryReason(err)
				step.LeaseOwner = ""
				step.LeaseExpiresAt = nextAttempt
				step.NextAttemptAt = nextAttempt
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusWaiting
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = nextAttempt
				exec.BlockedReason = ""
				exec.ResumeSignal = ""
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusPreparing, step.RetryReason, nil)
				r.appendTrace(ctx, audit.Record{
					ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
					Kind:        "execution.retry_scheduled",
					SessionID:   exec.SessionID,
					ExecutionID: exec.ID,
					TraceID:     exec.TraceID,
					Message:     err.Error(),
					Fields:      map[string]any{"step": step.Name, "attempt": step.Attempt, "next_attempt_at": nextAttempt, "retry_reason": step.RetryReason},
					CreatedAt:   time.Now().UTC(),
				})
				return nil
			}
			if step.Recomputable && isRetryableExecutionError(err) {
				step.Status = execution.StatusBlocked
				step.LastError = err.Error()
				step.RetryReason = retryReason(err)
				step.BlockedReason = execution.BlockedReasonRetryBudgetExhausted
				step.LeaseOwner = ""
				step.LeaseExpiresAt = time.Time{}
				step.FinishedAt = time.Now().UTC()
				step.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecutionStep(ctx, step)
				exec.Status = execution.StatusBlocked
				exec.BlockedReason = execution.BlockedReasonRetryBudgetExhausted
				exec.ResumeSignal = "operator_retry"
				exec.LeaseOwner = ""
				exec.LeaseExpiresAt = time.Time{}
				exec.UpdatedAt = time.Now().UTC()
				_ = r.repo.UpdateExecution(ctx, exec)
				_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusBlocked, execution.BlockedReasonRetryBudgetExhausted, nil)
				r.appendTrace(ctx, audit.Record{
					ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
					Kind:        "execution.blocked",
					SessionID:   exec.SessionID,
					ExecutionID: exec.ID,
					TraceID:     exec.TraceID,
					Message:     err.Error(),
					Fields:      map[string]any{"step": step.Name, "attempt": step.Attempt, "blocked_reason": exec.BlockedReason},
					CreatedAt:   time.Now().UTC(),
				})
				return nil
			}
			step.Status = execution.StatusFailed
			step.LastError = err.Error()
			step.LeaseOwner = ""
			step.LeaseExpiresAt = time.Time{}
			step.FinishedAt = time.Now().UTC()
			step.UpdatedAt = time.Now().UTC()
			_ = r.repo.UpdateExecutionStep(ctx, step)
			exec.Status = execution.StatusFailed
			exec.LeaseOwner = ""
			exec.LeaseExpiresAt = time.Time{}
			exec.UpdatedAt = time.Now().UTC()
			_ = r.repo.UpdateExecution(ctx, exec)
			_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusFailed, err.Error(), func(record *responsedomain.Response) {
				record.CompletedAt = time.Now().UTC()
			})
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
		step.LastError = ""
		step.RetryReason = ""
		step.BlockedReason = ""
		step.ResumeSignal = ""
		step.NextAttemptAt = time.Time{}
		step.FinishedAt = time.Now().UTC()
		step.UpdatedAt = time.Now().UTC()
		if err := r.repo.UpdateExecutionStep(ctx, step); err != nil {
			return err
		}
		r.publish(exec.SessionID, exec.ID, "runtime.step.completed", map[string]any{"step": step.Name})
	}

	exec.Status = execution.StatusSucceeded
	exec.BlockedReason = ""
	exec.ResumeSignal = ""
	exec.LeaseOwner = ""
	exec.LeaseExpiresAt = time.Time{}
	exec.UpdatedAt = time.Now().UTC()
	if err := r.repo.UpdateExecution(ctx, exec); err != nil {
		return err
	}
	_ = r.updateResponseState(ctx, responseRecord, responsedomain.StatusReady, "", func(record *responsedomain.Response) {
		record.CompletedAt = time.Now().UTC()
	})
	return nil
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
				"customer_context_hash": customerContextHash(view.CustomerContext),
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
			"customer_context_hash":   customerContextHash(view.CustomerContext),
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
		responseRecord, err := r.ensureResponseRecord(ctx, *exec)
		if err != nil {
			return err
		}
		view, events, toolOutput, responseRecord, err := r.prepareResponse(ctx, *exec, responseRecord)
		if err != nil {
			return err
		}
		generationMode := normalizeGenerationMode(view.CompositionMode)
		if err := r.updateResponseState(ctx, responseRecord, responseRecord.Status, responseRecord.Reason, func(record *responsedomain.Response) {
			record.GenerationMode = generationMode
		}); err != nil {
			return err
		}
		if err := r.maybeEmitPerceivedPerformance(ctx, *exec, responseRecord, view); err != nil {
			return err
		}
		if err := r.maybeEnsureRuntimeUpdateWatch(ctx, *exec, view, events, toolOutput); err != nil {
			return err
		}
		respMessages := renderResponseMessages(view, toolOutput)
		if len(respMessages) == 0 {
			prompt := composePrompt(view, events, toolOutput)
			r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
				ResponseID:  responseRecord.ID,
				SessionID:   exec.SessionID,
				ExecutionID: exec.ID,
				TraceID:     exec.TraceID,
				Kind:        "message.generate",
				Name:        generationMode,
				Status:      "started",
				StartedAt:   time.Now().UTC(),
			})
			resp, err := r.router.Generate(ctx, model.CapabilityReasoning, model.Request{Prompt: prompt})
			if err != nil {
				return err
			}
			respMessages = parseResponseEnvelope(resp.Text)
		}
		respMessages = normalizeResponseMessages(respMessages)
		respText := strings.Join(respMessages, "\n\n")
		verification := policyruntime.VerifyDraft(view, respText, toolOutput)
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  responseRecord.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "response.verify",
			Status:      verification.Status,
			Fields:      map[string]any{"reasons": verification.Reasons},
			StartedAt:   time.Now().UTC(),
			FinishedAt:  time.Now().UTC(),
		})
		switch verification.Status {
		case "revise", "block":
			if strings.TrimSpace(verification.Replacement) != "" {
				respMessages = []string{verification.Replacement}
				respText = verification.Replacement
			}
		}
		assistantEvents, err := r.createAssistantMessageSequence(ctx, *exec, respMessages)
		if err != nil {
			return err
		}
		eventIDs := eventIDs(assistantEvents)
		if err := r.updateResponseState(ctx, responseRecord, responsedomain.StatusProcessing, "", func(record *responsedomain.Response) {
			record.MessageEventIDs = append([]string(nil), eventIDs...)
		}); err != nil {
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
				"event_id":              firstString(eventIDs),
				"event_ids":             eventIDs,
				"journey_state":         journeyStateID(view.ActiveJourneyState),
				"tool_output":           toolOutput,
				"verification":          verification,
				"customer_context_hash": customerContextHash(view.CustomerContext),
			},
			CreatedAt: time.Now().UTC(),
		})
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "response.composed", "completed", exec.ID, exec.TraceID, map[string]any{
			"event_id":              firstString(eventIDs),
			"event_ids":             eventIDs,
			"customer_context_hash": customerContextHash(view.CustomerContext),
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
		assistantEvents := assistantEventsForExecution(events, exec.ID)
		if len(assistantEvents) == 0 {
			if last := latestAssistant(events); last.ID != "" {
				assistantEvents = []session.Event{last}
			}
		}
		eventIDs := eventIDs(assistantEvents)
		if len(eventIDs) > 0 {
			r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
				ResponseID:  r.responseIDForExecution(ctx, exec.ID),
				SessionID:   exec.SessionID,
				ExecutionID: exec.ID,
				TraceID:     exec.TraceID,
				Kind:        "response.deliver",
				Status:      "queued",
				Fields:      map[string]any{"event_ids": eventIDs},
				StartedAt:   time.Now().UTC(),
				FinishedAt:  time.Now().UTC(),
			})
			if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "response.delivered", "queued", exec.ID, exec.TraceID, map[string]any{
				"event_id":  firstString(eventIDs),
				"event_ids": eventIDs,
			}, nil, false); err != nil {
				return err
			}
			r.publish(exec.SessionID, exec.ID, "runtime.response.completed", map[string]any{"event_id": firstString(eventIDs), "event_ids": eventIDs, "status": "queued_for_gateway"})
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
	snapshots, err := r.repo.ListPolicySnapshots(ctx, policy.SnapshotQuery{})
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
	selectedSnapshot, selectedBundles, resolvedSnapshotID := selectPolicySnapshotBundle(snapshots, selection.BundleID, defaultBundleID)
	if len(selectedBundles) == 0 {
		return resolvedView{}, nil, fmt.Errorf("policy snapshot not found for selection bundle=%q fallback=%q", selection.BundleID, defaultBundleID)
	}
	catalog = filterCatalogForBundles(catalog, selectedBundles)
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
	view.CustomerContext = customerContextFromSession(sess)
	view.CustomerContextPromptSafeFields = customerContextPromptSafeFields(sess)
	resolvedBundleID := selection.BundleID
	if resolvedBundleID == "" && len(selectedBundles) > 0 {
		resolvedBundleID = selectedBundles[0].ID
	}
	if selectedSnapshot.ID != "" && resolvedSnapshotID == "" {
		resolvedSnapshotID = selectedSnapshot.ID
	}
	if resolvedBundleID != "" && (exec.PolicyBundleID != resolvedBundleID || exec.PolicySnapshotID != resolvedSnapshotID || exec.SelectionReason != selection.Reason || exec.ProposalID != selection.ProposalID || exec.RolloutID != selection.RolloutID) {
		exec.PolicyBundleID = resolvedBundleID
		exec.PolicySnapshotID = resolvedSnapshotID
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

func (r *Runner) prepareResponse(ctx context.Context, exec execution.TurnExecution, record responsedomain.Response) (resolvedView, []session.Event, map[string]any, responsedomain.Response, error) {
	var (
		view       resolvedView
		events     []session.Event
		toolOutput map[string]any
		err        error
	)
	record.MaxIterations = maxResponsePreparationIterations
	for iteration := 1; iteration <= maxResponsePreparationIterations; iteration++ {
		started := time.Now().UTC()
		view, events, err = r.resolveView(ctx, exec)
		if err != nil {
			return resolvedView{}, nil, nil, record, err
		}
		insights := toolInsightsFromView(view)
		record.IterationCount = iteration
		record.ToolInsights = append([]string(nil), insights...)
		record.GlossaryTerms = append([]string(nil), glossaryHitsFromView(view, events)...)
		record.UpdatedAt = started
		if err := r.updateResponseState(ctx, record, responsedomain.StatusProcessing, "", func(item *responsedomain.Response) {
			item.IterationCount = iteration
			item.MaxIterations = maxResponsePreparationIterations
			item.ToolInsights = append([]string(nil), insights...)
			item.GlossaryTerms = append([]string(nil), glossaryHitsFromView(view, events)...)
		}); err != nil {
			return resolvedView{}, nil, nil, record, err
		}
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  record.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "response.iteration",
			Iteration:   iteration,
			Status:      "started",
			Fields: map[string]any{
				"tool_insights":  insights,
				"active_journey": journeyID(view.ActiveJourney),
				"active_state":   journeyStateID(view.ActiveJourneyState),
			},
			StartedAt:  started,
			FinishedAt: time.Now().UTC(),
		})
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  record.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "guideline.match",
			Iteration:   iteration,
			Status:      "completed",
			Fields:      map[string]any{"matched_guidelines": idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines)},
			StartedAt:   started,
			FinishedAt:  time.Now().UTC(),
		})
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  record.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "journey.progress",
			Iteration:   iteration,
			Status:      "completed",
			Fields:      map[string]any{"action": view.JourneyProgressStage.Decision.Action, "next_state": view.JourneyProgressStage.Decision.NextState},
			StartedAt:   started,
			FinishedAt:  time.Now().UTC(),
		})
		if !needsCapabilityPreparation(view) {
			record.StabilityReached = true
			_ = r.updateResponseState(ctx, record, responsedomain.StatusProcessing, "", func(item *responsedomain.Response) {
				item.StabilityReached = true
			})
			return view, events, toolOutput, record, nil
		}
		toolOutput, err = r.maybeRunCapability(ctx, exec, view)
		if err != nil {
			return resolvedView{}, nil, nil, record, err
		}
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  record.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "tool.insight",
			Iteration:   iteration,
			Status:      "completed",
			Fields:      map[string]any{"tool_insights": insights, "tool_output_present": toolOutput != nil},
			StartedAt:   started,
			FinishedAt:  time.Now().UTC(),
		})
		if strings.EqualFold(view.CapabilityDecisionStage.Decision.Kind, "agent") && toolOutput != nil {
			record.StabilityReached = true
			_ = r.updateResponseState(ctx, record, responsedomain.StatusProcessing, "", func(item *responsedomain.Response) {
				item.StabilityReached = true
			})
			return view, events, toolOutput, record, nil
		}
		if toolOutput == nil || iteration == maxResponsePreparationIterations {
			if iteration == maxResponsePreparationIterations {
				record.Reason = "response_preparation_iteration_exhausted"
				record.StabilityReached = false
				_ = r.updateResponseState(ctx, record, responsedomain.StatusBlocked, record.Reason, func(item *responsedomain.Response) {
					item.StabilityReached = false
				})
				return resolvedView{}, nil, nil, record, errResponsePreparationExhausted
			}
			record.StabilityReached = true
			_ = r.updateResponseState(ctx, record, responsedomain.StatusProcessing, "", func(item *responsedomain.Response) {
				item.StabilityReached = true
			})
			return view, events, toolOutput, record, nil
		}
	}
	return resolvedView{}, nil, nil, record, errResponsePreparationExhausted
}

func needsCapabilityPreparation(view resolvedView) bool {
	if strings.EqualFold(view.CapabilityDecisionStage.Decision.Kind, "agent") {
		return strings.TrimSpace(view.AgentDecisionStage.Decision.SelectedAgent) != ""
	}
	plan := view.ToolPlanStage.Plan
	for _, candidate := range plan.Candidates {
		if candidate.AlreadySatisfied || candidate.AlreadyStaged {
			continue
		}
		if strings.EqualFold(candidate.ApprovalMode, "required") {
			return true
		}
		if len(candidate.MissingIssues) == 0 && len(candidate.InvalidIssues) == 0 {
			return true
		}
	}
	return false
}

func toolInsightsFromView(view resolvedView) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, candidate := range view.ToolPlanStage.Plan.Candidates {
		insight := "cannot_run"
		switch {
		case candidate.AlreadySatisfied:
			insight = "data_already_in_context"
		case strings.EqualFold(candidate.ApprovalMode, "required") && !candidate.AlreadySatisfied:
			insight = "blocked_on_approval"
		case !candidate.AlreadyStaged && !candidate.AlreadySatisfied && len(candidate.MissingIssues) == 0 && len(candidate.InvalidIssues) == 0:
			insight = "needs_to_run"
		}
		key := strings.TrimSpace(candidate.ToolID) + ":" + insight
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func glossaryHitsFromView(view resolvedView, events []session.Event) []string {
	if view.Bundle == nil || len(view.Bundle.Glossary) == 0 {
		return nil
	}
	text := strings.ToLower(latestText(events))
	var out []string
	for _, item := range view.Bundle.Glossary {
		if strings.Contains(text, strings.ToLower(item.Term)) {
			out = append(out, item.Term)
			continue
		}
		for _, alias := range item.Aliases {
			if strings.Contains(text, strings.ToLower(alias)) {
				out = append(out, item.Term)
				break
			}
		}
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

func normalizeGenerationMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "strict", "fluid", "canned_fluid", "canned_composited":
		return mode
	default:
		return "fluid"
	}
}

func perceivedPerformancePolicy(view resolvedView) policy.PerceivedPerformancePolicy {
	if view.Bundle == nil {
		return policy.PerceivedPerformancePolicy{}
	}
	return view.Bundle.PerceivedPerformance
}

func allowsPerceivedPerformanceRisk(perf policy.PerceivedPerformancePolicy, riskTier string) bool {
	riskTier = strings.ToLower(strings.TrimSpace(riskTier))
	if len(perf.AllowedRiskTiers) > 0 {
		for _, item := range perf.AllowedRiskTiers {
			if strings.EqualFold(strings.TrimSpace(item), riskTier) {
				return true
			}
		}
		return false
	}
	switch strings.ToLower(strings.TrimSpace(perf.Mode)) {
	case "aggressive":
		return riskTier == "low" || riskTier == "medium" || riskTier == "high"
	default:
		return riskTier == "low" || riskTier == "medium"
	}
}

func (r *Runner) maybeEmitPerceivedPerformance(ctx context.Context, exec execution.TurnExecution, record responsedomain.Response, view resolvedView) error {
	perf := perceivedPerformancePolicy(view)
	if strings.EqualFold(strings.TrimSpace(perf.Mode), "off") || strings.TrimSpace(perf.Mode) == "" {
		return nil
	}
	plan := quality.BuildResponsePlan(view)
	if !allowsPerceivedPerformanceRisk(perf, plan.RiskTier) {
		return nil
	}
	now := time.Now().UTC()
	startedAt := record.StartedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	elapsed := now.Sub(startedAt)
	if perf.ProcessingIndicator && elapsed >= time.Duration(perf.ProcessingUpdateDelayMS)*time.Millisecond {
		if _, err := r.sessions.CreateACPStatusEvent(ctx, exec.SessionID, "runtime", "response.processing", "in_progress", exec.ID, exec.TraceID, map[string]any{
			"response_id": record.ID,
			"mode":        perf.Mode,
		}, nil, false); err != nil {
			return err
		}
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  record.ID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "response.processing_indicator",
			Status:      "emitted",
			StartedAt:   now,
			FinishedAt:  now,
		})
	}
	if !perf.PreambleEnabled || strings.TrimSpace(record.PreambleEventID) != "" || elapsed < time.Duration(perf.PreambleDelayMS)*time.Millisecond {
		return nil
	}
	preamble := "I’m checking that now."
	if len(perf.Preambles) > 0 && strings.TrimSpace(perf.Preambles[0]) != "" {
		preamble = strings.TrimSpace(perf.Preambles[0])
	}
	event, err := r.sessions.CreateMessageEvent(ctx, exec.SessionID, "ai_agent", preamble, exec.ID, exec.TraceID, map[string]any{
		"step":          "compose_response",
		"preamble":      true,
		"response_id":   record.ID,
		"message_index": 0,
	}, false)
	if err != nil {
		return err
	}
	if err := r.updateResponseState(ctx, record, responsedomain.StatusProcessing, "", func(item *responsedomain.Response) {
		item.PreambleEventID = event.ID
	}); err != nil {
		return err
	}
	r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
		ResponseID:  record.ID,
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Kind:        "response.preamble",
		Status:      "emitted",
		Fields:      map[string]any{"event_id": event.ID},
		StartedAt:   now,
		FinishedAt:  now,
	})
	return nil
}

func (r *Runner) maybeEnsureRuntimeUpdateWatch(ctx context.Context, exec execution.TurnExecution, view resolvedView, events []session.Event, toolOutput map[string]any) error {
	intent, ok := r.runtimeUpdateIntent(ctx, exec, view, events, toolOutput)
	if !ok {
		return nil
	}
	sess, err := r.repo.GetSession(ctx, exec.SessionID)
	if err != nil {
		return err
	}
	if sess.Status == session.StatusClosed {
		return nil
	}
	if _, _, err := sessionwatch.EnsureSessionWatch(ctx, r.repo, sess, intent, time.Now().UTC()); err != nil {
		return err
	}
	return r.markSessionKeepForWatch(ctx, sess, intent.Kind)
}

func (r *Runner) runtimeUpdateIntent(ctx context.Context, exec execution.TurnExecution, view resolvedView, events []session.Event, toolOutput map[string]any) (sessionwatch.UpdateIntent, bool) {
	if intent, ok := runtimeUpdateIntentFromArtifacts(view, time.Now().UTC()); ok {
		return intent, true
	}
	now := time.Now().UTC()
	customerText := latestCustomerText(events)
	for _, capability := range view.WatchCapabilities {
		if intent, ok := runtimeCapabilityIntentFromView(capability, view, customerText, now); ok {
			return intent, true
		}
		if intent, ok := r.runtimeCapabilityIntent(ctx, exec, capability, toolOutput, customerText, now); ok {
			return intent, true
		}
	}
	return sessionwatch.UpdateIntent{}, false
}

func runtimeUpdateIntentFromArtifacts(view resolvedView, now time.Time) (sessionwatch.UpdateIntent, bool) {
	for _, item := range view.UpdateIntents {
		capability, ok := watchCapabilityByID(view.WatchCapabilities, item.CapabilityID, item.Kind)
		if !ok {
			capability = policy.WatchCapability{
				ID:                  firstNonEmptyString(item.CapabilityID, item.Kind),
				Kind:                item.Kind,
				ScheduleStrategy:    watchScheduleFromArtifact(item),
				SubjectKeys:         []string{"order_id", "tracking_id", "shipment_id", "package_id", "id"},
				PollIntervalSeconds: item.PollIntervalSeconds,
				StopCondition:       item.StopCondition,
			}
		}
		intent, ok := sessionwatch.BuildIntentFromCapability(capability, firstNonEmptyString(item.Source, sessionwatch.SourceRuntime), item.ToolID, item.SubjectRef, cloneAnyMap(item.Arguments), now)
		if ok {
			return intent, true
		}
	}
	return sessionwatch.UpdateIntent{}, false
}

func watchCapabilityByID(items []policy.WatchCapability, capabilityID, kind string) (policy.WatchCapability, bool) {
	for _, item := range items {
		if strings.TrimSpace(capabilityID) != "" && item.ID == capabilityID {
			return item, true
		}
	}
	for _, item := range items {
		if strings.TrimSpace(kind) != "" && item.Kind == kind {
			return item, true
		}
	}
	return policy.WatchCapability{}, false
}

func watchScheduleFromArtifact(item policyruntime.UpdateIntentArtifact) string {
	if strings.TrimSpace(item.RemindAt) != "" {
		return "reminder"
	}
	return "poll"
}

func watchCapabilityToolMatches(capability policy.WatchCapability, toolID string) bool {
	lowerToolID := strings.ToLower(strings.TrimSpace(toolID))
	if lowerToolID == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(capability.ID), toolID) || strings.EqualFold(strings.TrimSpace(capability.Kind), toolID) {
		return true
	}
	for _, term := range capability.ToolMatchTerms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(lowerToolID, term) {
			return true
		}
	}
	return false
}

func runtimeCapabilityIntentFromView(capability policy.WatchCapability, view resolvedView, customerText string, now time.Time) (sessionwatch.UpdateIntent, bool) {
	for _, candidate := range view.ToolPlanStage.Plan.Candidates {
		if !watchCapabilityToolMatches(capability, candidate.ToolID) {
			continue
		}
		subjectRef := sessionwatch.ExtractSubjectRef(candidate.Arguments, capability.SubjectKeys...)
		if intent, ok := sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, candidate.ToolID, subjectRef, candidate.Arguments, now); ok {
			return intent, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(capability.ScheduleStrategy), "reminder") {
		if !runtimeSupportsCapability(view, capability) {
			return sessionwatch.UpdateIntent{}, false
		}
		if appointmentAt, ok := sessionwatch.ParseAppointmentTimeFromText(customerText, now); ok {
			args := map[string]any{"appointment_at": appointmentAt.UTC().Format(time.RFC3339)}
			subjectRef := firstNonEmptyString(sessionwatch.ExtractSubjectRef(args, capability.SubjectKeys...), appointmentAt.UTC().Format(time.RFC3339))
			return sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, capability.ID, subjectRef, args, now)
		}
	}
	return sessionwatch.UpdateIntent{}, false
}

func (r *Runner) runtimeCapabilityIntent(ctx context.Context, exec execution.TurnExecution, capability policy.WatchCapability, toolOutput map[string]any, customerText string, now time.Time) (sessionwatch.UpdateIntent, bool) {
	runs, err := r.repo.ListToolRuns(ctx, exec.ID)
	if err != nil {
		return sessionwatch.UpdateIntent{}, false
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if !watchCapabilityToolMatches(capability, run.ToolID) {
			continue
		}
		args := parseJSONMap(run.InputJSON)
		subjectRef := sessionwatch.ExtractSubjectRef(args, capability.SubjectKeys...)
		if intent, ok := sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, run.ToolID, subjectRef, args, now); ok {
			return intent, true
		}
	}
	if tools, ok := toolOutput["tools"].(map[string]any); ok {
		for key, raw := range tools {
			if !watchCapabilityToolMatches(capability, key) {
				continue
			}
			output, _ := raw.(map[string]any)
			subjectRef := sessionwatch.ExtractSubjectRef(output, capability.SubjectKeys...)
			if intent, ok := sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, key, subjectRef, output, now); ok {
				return intent, true
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(capability.ScheduleStrategy), "reminder") {
		if appointmentAt, ok := sessionwatch.ParseAppointmentTimeFromText(customerText, now); ok {
			args := map[string]any{"appointment_at": appointmentAt.UTC().Format(time.RFC3339)}
			subjectRef := firstNonEmptyString(sessionwatch.ExtractSubjectRef(args, capability.SubjectKeys...), appointmentAt.UTC().Format(time.RFC3339))
			return sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceRuntime, capability.ID, subjectRef, args, now)
		}
	}
	return sessionwatch.UpdateIntent{}, false
}

func runtimeSupportsCapability(view resolvedView, capability policy.WatchCapability) bool {
	for _, candidate := range view.ToolPlanStage.Plan.Candidates {
		if watchCapabilityToolMatches(capability, candidate.ToolID) {
			return true
		}
	}
	for _, toolID := range view.ToolPlanStage.Plan.SelectedTools {
		if watchCapabilityToolMatches(capability, toolID) {
			return true
		}
	}
	return false
}

func (r *Runner) markSessionKeepForWatch(ctx context.Context, sess session.Session, reason string) error {
	sess.Status = session.StatusSessionKeep
	sess.KeepReason = firstNonEmptyString(reason, "background_watch")
	sess.LastActivityAt = time.Now().UTC()
	sess.IdleCheckedAt = sess.LastActivityAt
	sess.AwaitingCustomerSince = time.Time{}
	sess.ClosedAt = time.Time{}
	sess.CloseReason = ""
	if err := r.repo.UpdateSession(ctx, sess); err != nil {
		return err
	}
	return knowledgelearning.New(r.repo).CompileDeferredFeedbackRecords(ctx, sess)
}

func selectPolicySnapshotBundle(snapshots []policy.Snapshot, preferred string, fallback string) (policy.Snapshot, []policy.Bundle, string) {
	snapshot, ok := policy.SelectSnapshot(snapshots, preferred, fallback)
	if !ok {
		return policy.Snapshot{}, nil, ""
	}
	bundle := policy.SnapshotBundle(snapshot)
	if strings.TrimSpace(bundle.ID) == "" {
		return snapshot, nil, snapshot.ID
	}
	return snapshot, []policy.Bundle{bundle}, snapshot.ID
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
	customerScopeKind, customerScopeID = candidateKnowledgeScope(bundles, customerScopeKind, customerScopeID)
	if customerScopeID != "" {
		customerSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, Limit: 1})
		if err == nil && len(customerSnapshots) > 0 {
			snapshots = append(snapshots, customerSnapshots[0])
			customerChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: customerScopeKind, ScopeID: customerScopeID, SnapshotID: customerSnapshots[0].ID})
			chunks = append(chunks, customerChunks...)
		}
	}
	scopeKind, scopeID := knowledgeScope(sess, bundles)
	scopeKind, scopeID = candidateKnowledgeScope(bundles, scopeKind, scopeID)
	if scopeID != "" {
		sharedSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: scopeKind, ScopeID: scopeID, Limit: 1})
		if err == nil && len(sharedSnapshots) > 0 {
			snapshots = append(snapshots, sharedSnapshots[0])
			sharedChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: scopeKind, ScopeID: scopeID, SnapshotID: sharedSnapshots[0].ID})
			chunks = append(chunks, sharedChunks...)
		}
	}
	profileScopeKind, profileScopeID := candidateKnowledgeScope(bundles, profile.DefaultKnowledgeScopeKind, profile.DefaultKnowledgeScopeID)
	if len(snapshots) == 0 && profileScopeKind != "" && profileScopeID != "" {
		profileSnapshots, err := r.repo.ListKnowledgeSnapshots(ctx, knowledge.SnapshotQuery{ScopeKind: profileScopeKind, ScopeID: profileScopeID, Limit: 1})
		if err == nil && len(profileSnapshots) > 0 {
			snapshots = append(snapshots, profileSnapshots[0])
			profileChunks, _ := r.repo.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{ScopeKind: profileScopeKind, ScopeID: profileScopeID, SnapshotID: profileSnapshots[0].ID})
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
	view, _, err := r.resolveView(ctx, exec)
	if err == nil && view.ScopeBoundaryStage.Classification == "out_of_scope" {
		return nil
	}
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
		outputs, err := r.runToolCallsParallel(ctx, exec, view, calls)
		if err != nil {
			return nil, err
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
	calls := make([]policyruntime.ToolPlannedCall, 0, len(selectedTools))
	for _, toolName := range selectedTools {
		calls = append(calls, policyruntime.ToolPlannedCall{ToolID: toolName})
	}
	var err error
	outputs, err = r.runToolCallsParallel(ctx, exec, view, calls)
	if err != nil {
		return nil, err
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

func (r *Runner) maybeRunCapability(ctx context.Context, exec execution.TurnExecution, view resolvedView) (map[string]any, error) {
	if strings.EqualFold(view.CapabilityDecisionStage.Decision.Kind, "agent") {
		return r.maybeDelegateAgent(ctx, exec, view)
	}
	return r.maybeRunTool(ctx, exec, view)
}

func (r *Runner) maybeDelegateAgent(ctx context.Context, exec execution.TurnExecution, view resolvedView) (map[string]any, error) {
	decision := view.AgentDecisionStage.Decision
	serverID := strings.TrimSpace(decision.SelectedAgent)
	if serverID == "" || !decision.CanRun {
		return nil, nil
	}
	if r.agentPeers == nil || !r.agentPeers.Has(serverID) {
		return map[string]any{
			"delegated_agent": map[string]any{
				"server_id": serverID,
				"status":    "failed",
				"error":     "delegated agent server is not configured",
			},
		}, nil
	}

	sess, err := r.repo.GetSession(ctx, exec.SessionID)
	if err != nil {
		return nil, err
	}
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return nil, err
	}
	childSessionID := stableID("delegate", exec.SessionID, exec.ID, serverID)
	req := acppeer.Request{
		SessionID: childSessionID,
		CWD:       currentWorkingDirectory(),
		Prompt:    delegatedAgentPrompt(exec, view, events),
		Metadata: map[string]any{
			"delegation_parent_session_id":   exec.SessionID,
			"delegation_parent_execution_id": exec.ID,
			"delegation_parent_agent_id":     sess.AgentID,
			"delegation_server_id":           serverID,
			"delegation_mode":                "sync",
		},
	}
	result, err := r.agentPeers.Delegate(ctx, serverID, req)
	status := result.Status
	if status == "" {
		status = "failed"
	}
	fields := map[string]any{
		"server_id":  serverID,
		"session_id": result.SessionID,
		"status":     status,
		"error":      result.Error,
	}
	if strings.TrimSpace(result.Text) != "" {
		fields["result_text"] = result.Text
	}
	kind := "agent.completed"
	if err != nil {
		kind = "agent.failed"
	}
	_, _ = r.sessions.CreateEvent(ctx, sessionsvc.CreateEventParams{
		SessionID:   exec.SessionID,
		Source:      "runtime",
		Kind:        kind,
		Data:        fields,
		Metadata:    map[string]any{"internal_only": true},
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		CreatedAt:   time.Now().UTC(),
		Async:       false,
	})
	r.appendTrace(ctx, audit.Record{
		ID:          fmt.Sprintf("trace_%d", time.Now().UnixNano()),
		Kind:        kind,
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Message:     delegatedAgentTraceMessage(kind),
		Fields:      fields,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return map[string]any{"delegated_agent": fields}, nil
	}
	return map[string]any{
		"delegated_agent": map[string]any{
			"server_id":   serverID,
			"session_id":  result.SessionID,
			"status":      status,
			"result_text": result.Text,
		},
	}, nil
}

type toolBatchItem struct {
	key    string
	output map[string]any
	err    error
}

func (r *Runner) runToolCallsParallel(ctx context.Context, exec execution.TurnExecution, view resolvedView, calls []policyruntime.ToolPlannedCall) (map[string]map[string]any, error) {
	type runnable struct {
		entry    tool.CatalogEntry
		decision policyruntime.ToolDecision
		key      string
	}
	var tasks []runnable
	seen := map[string]struct{}{}
	for _, call := range calls {
		entry, ok := findCatalogEntry(r.repo, call.ToolID)
		if !ok {
			continue
		}
		decision := decisionForPlannedCall(view, call)
		key := entry.ProviderID + ":" + entry.Name
		if len(decision.Arguments) > 0 {
			key += "#" + hashArguments(decision.Arguments)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tasks = append(tasks, runnable{entry: entry, decision: decision, key: key})
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	results := make(chan toolBatchItem, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, err := r.runSingleTool(ctx, exec, view, task.entry, task.decision)
			results <- toolBatchItem{key: task.key, output: output, err: err}
		}()
	}
	wg.Wait()
	close(results)

	outputs := map[string]map[string]any{}
	var retryableErr error
	var approvalErr error
	var firstErr error
	var errorsOut []map[string]any
	for result := range results {
		if result.err != nil {
			if errors.Is(result.err, errApprovalRequired) {
				approvalErr = result.err
				continue
			}
			if isRetryableExecutionError(result.err) && retryableErr == nil {
				retryableErr = result.err
				continue
			}
			if firstErr == nil {
				firstErr = result.err
			}
			errorsOut = append(errorsOut, map[string]any{"tool": result.key, "error": result.err.Error(), "retryable": false})
			continue
		}
		if result.output != nil {
			outputs[result.key] = result.output
		}
	}
	if approvalErr != nil {
		return nil, approvalErr
	}
	if retryableErr != nil {
		return nil, retryableErr
	}
	if firstErr != nil && len(outputs) == 0 {
		return nil, firstErr
	}
	if len(errorsOut) > 0 {
		outputs["tool_errors"] = map[string]any{"errors": errorsOut}
	}
	return outputs, nil
}

func (r *Runner) runSingleTool(ctx context.Context, exec execution.TurnExecution, view resolvedView, entry tool.CatalogEntry, decision policyruntime.ToolDecision) (map[string]any, error) {
	responseID := r.responseIDForExecution(ctx, exec.ID)
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
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  responseID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "tool.run",
			Name:        entry.Name,
			Status:      "cannot_run",
			Fields:      payload,
			StartedAt:   time.Now().UTC(),
			FinishedAt:  time.Now().UTC(),
		})
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
	toolRunStarted := time.Now().UTC()
	r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
		ResponseID:  responseID,
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Kind:        "tool.run",
		Name:        entry.Name,
		Status:      "started",
		Fields:      map[string]any{"arguments": cloneAnyMap(decision.Arguments)},
		StartedAt:   toolRunStarted,
	})
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
		r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
			ResponseID:  responseID,
			SessionID:   exec.SessionID,
			ExecutionID: exec.ID,
			TraceID:     exec.TraceID,
			Kind:        "tool.run",
			Name:        entry.Name,
			Status:      "failed",
			Fields:      toolErrorPayload(err),
			StartedAt:   toolRunStarted,
			FinishedAt:  time.Now().UTC(),
		})
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
	r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
		ResponseID:  responseID,
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Kind:        "tool.run",
		Name:        entry.Name,
		Status:      "completed",
		Fields:      map[string]any{"output": output},
		StartedAt:   toolRunStarted,
		FinishedAt:  time.Now().UTC(),
	})
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

func stepMaxAttempts(step execution.ExecutionStep) int {
	if step.MaxAttempts > 0 {
		return step.MaxAttempts
	}
	return execution.DefaultRetryPolicy(step.Recomputable).MaxAttempts
}

func stepRetryBudgetAllows(step execution.ExecutionStep) bool {
	if step.Attempt >= stepMaxAttempts(step) {
		return false
	}
	if step.MaxElapsedSeconds <= 0 || step.StartedAt.IsZero() {
		return true
	}
	return time.Since(step.StartedAt) < time.Duration(step.MaxElapsedSeconds)*time.Second
}

func stepBackoff(step execution.ExecutionStep) time.Duration {
	seconds := step.BackoffSeconds
	if seconds <= 0 {
		seconds = execution.DefaultRetryPolicy(step.Recomputable).BackoffSeconds
	}
	if seconds <= 0 {
		seconds = 1
	}
	// Exponential backoff keeps retry-until-valid loops durable without hot-spinning.
	delay := time.Duration(seconds) * time.Second
	for i := 1; i < step.Attempt; i++ {
		delay *= 2
		if delay >= time.Minute {
			return time.Minute
		}
	}
	return delay
}

func retryReason(err error) string {
	var invokeErr *toolruntime.InvokeError
	if errors.As(err, &invokeErr) {
		if invokeErr.Class != "" {
			return string(invokeErr.Class)
		}
		if invokeErr.Retryable {
			return "tool_retryable"
		}
		return "tool_error"
	}
	return "runtime_retryable"
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
	responseID := r.responseIDForExecution(ctx, exec.ID)
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
	r.appendResponseTraceSpan(ctx, responsedomain.TraceSpan{
		ResponseID:  responseID,
		SessionID:   exec.SessionID,
		ExecutionID: exec.ID,
		TraceID:     exec.TraceID,
		Kind:        "tool.insight",
		Name:        entry.Name,
		Status:      "blocked_on_approval",
		Fields:      map[string]any{"approval_id": item.ID},
		StartedAt:   now,
		FinishedAt:  now,
	})
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
	responsePlan := quality.BuildResponsePlan(view)
	if plan := quality.FormatResponsePlan(responsePlan); plan != "" {
		parts = append(parts, "Response quality plan: "+plan)
	}
	if len(responsePlan.DesiredStructure) > 0 {
		lines := make([]string, 0, len(responsePlan.DesiredStructure))
		for i, item := range responsePlan.DesiredStructure {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, item))
		}
		parts = append(parts, "High-risk response blueprint:\n"+strings.Join(lines, "\n"))
	}
	if strings.EqualFold(responsePlan.RiskTier, "high") {
		var contract []string
		contract = append(contract, "Do not promise refunds, replacements, approvals, or eligibility unless the answer explicitly stays within verified evidence and required review steps.")
		if len(responsePlan.RequiredVerificationSteps) > 0 {
			contract = append(contract, "If verification is still required, ask for or confirm the missing requirement before making a commitment.")
		}
		if responsePlan.RetrievalRequired {
			contract = append(contract, "When you rely on retrieved knowledge, cite the supporting source identifier or URI in the answer.")
		}
		if len(responsePlan.AllowedCommitments) > 0 {
			contract = append(contract, "Allowed commitment envelope: "+strings.Join(responsePlan.AllowedCommitments, "; "))
		}
		parts = append(parts, "High-risk response contract:\n"+strings.Join(contract, "\n"))
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
	if ctx := customerContextPromptText(view.CustomerContext, view.CustomerContextPromptSafeFields); ctx != "" {
		parts = append(parts, "Customer context:\n"+ctx)
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
	if agentDecision := view.AgentDecisionStage.Decision; agentDecision.SelectedAgent != "" {
		parts = append(parts, "Selected delegated agent: "+agentDecision.SelectedAgent)
	}
	if len(toolOutput) > 0 {
		parts = append(parts, "Tool output: "+mustJSON(toolOutput))
	}
	parts = append(parts, `Return either plain text or JSON with this schema: {"messages":["first assistant message","optional follow-up message"]}. Use 1 to 3 messages. Split into multiple messages only when that makes the conversation more natural or when a template/policy calls for a sequence.`)
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

func customerContextFromSession(sess session.Session) map[string]any {
	raw, _ := sess.Metadata["customer_context"].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		out[key] = value
	}
	return out
}

func customerContextPromptSafeFields(sess session.Session) []string {
	raw := sess.Metadata["customer_context_prompt_safe_fields"]
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		var out []string
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return dedupeStrings(out)
	default:
		return nil
	}
}

func customerContextPromptText(ctx map[string]any, safeFields []string) string {
	if len(ctx) == 0 || len(safeFields) == 0 {
		return ""
	}
	var parts []string
	for _, key := range dedupeStrings(safeFields) {
		value, ok := ctx[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" {
			continue
		}
		parts = append(parts, key+": "+text)
	}
	return strings.Join(parts, "\n")
}

func customerContextHash(ctx map[string]any) string {
	if len(ctx) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ctx))
	for key := range ctx {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]any, len(keys))
	for _, key := range keys {
		ordered[key] = ctx[key]
	}
	raw, err := json.Marshal(ordered)
	if err != nil {
		return ""
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:8])
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

func delegatedAgentPrompt(exec execution.TurnExecution, view resolvedView, events []session.Event) string {
	var parts []string
	if latest := latestText(events); latest != "" {
		parts = append(parts, "Customer request: "+latest)
	}
	if history := compactConversationExcerpt(events); history != "" {
		parts = append(parts, "Conversation excerpt:\n"+history)
	}
	if len(view.MatchFinalizeStage.MatchedGuidelines) > 0 {
		parts = append(parts, "Matched guideline IDs: "+strings.Join(idsFromGuidelines(view.MatchFinalizeStage.MatchedGuidelines), ", "))
	}
	if view.ActiveJourney != nil {
		parts = append(parts, "Active journey: "+view.ActiveJourney.ID)
	}
	if view.ActiveJourneyState != nil {
		parts = append(parts, "Active journey state: "+view.ActiveJourneyState.ID)
		if strings.TrimSpace(view.ActiveJourneyState.Instruction) != "" {
			parts = append(parts, "Journey instruction: "+view.ActiveJourneyState.Instruction)
		}
	}
	parts = append(parts, "Parent execution ID: "+exec.ID)
	parts = append(parts, "Solve the task and return a concise final answer for the parent agent. Do not ask the parent agent to inspect your internal process.")
	return strings.Join(parts, "\n")
}

func compactConversationExcerpt(events []session.Event) string {
	var parts []string
	for _, item := range events {
		text := strings.TrimSpace(eventText(item))
		if text == "" {
			continue
		}
		parts = append(parts, item.Source+": "+text)
	}
	if len(parts) > 6 {
		parts = parts[len(parts)-6:]
	}
	return strings.Join(parts, "\n")
}

func eventText(item session.Event) string {
	var parts []string
	for _, part := range item.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, " ")
}

func delegatedAgentTraceMessage(kind string) string {
	if kind == "agent.failed" {
		return "delegated agent failed"
	}
	return "delegated agent completed"
}

func currentWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return "."
	}
	return cwd
}

func stableID(prefix string, parts ...string) string {
	h := sha1.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strings.TrimSpace(part)))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:16]
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(raw)
}

func parseJSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
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

func (r *Runner) appendResponseTraceSpan(ctx context.Context, span responsedomain.TraceSpan) {
	if span.ID == "" {
		span.ID = fmt.Sprintf("span_%d", time.Now().UnixNano())
	}
	if r.writes != nil {
		_ = r.writes.SaveResponseTraceSpan(ctx, span)
		return
	}
	_ = r.repo.SaveResponseTraceSpan(ctx, span)
}

func (r *Runner) cachedResponse(executionID string) (responsedomain.Response, bool) {
	r.responseMu.Lock()
	defer r.responseMu.Unlock()
	record, ok := r.responses[executionID]
	return record, ok
}

func (r *Runner) cacheResponse(record responsedomain.Response) {
	if record.ExecutionID == "" {
		return
	}
	r.responseMu.Lock()
	defer r.responseMu.Unlock()
	r.responses[record.ExecutionID] = record
}

func (r *Runner) ensureResponseRecord(ctx context.Context, exec execution.TurnExecution) (responsedomain.Response, error) {
	if record, ok := r.cachedResponse(exec.ID); ok {
		if record.PolicySnapshotID == "" && exec.PolicySnapshotID != "" {
			record.PolicySnapshotID = exec.PolicySnapshotID
			r.cacheResponse(record)
		}
		return record, nil
	}
	items, err := r.repo.ListResponses(ctx, responsedomain.Query{ExecutionID: exec.ID, Limit: 1})
	if err == nil && len(items) > 0 {
		if items[0].PolicySnapshotID == "" && exec.PolicySnapshotID != "" {
			items[0].PolicySnapshotID = exec.PolicySnapshotID
			if saveErr := r.repo.SaveResponse(ctx, items[0]); saveErr != nil {
				return responsedomain.Response{}, saveErr
			}
		}
		r.cacheResponse(items[0])
		return items[0], nil
	}
	now := time.Now().UTC()
	record := responsedomain.Response{
		ID:               fmt.Sprintf("resp_%d", now.UnixNano()),
		SessionID:        exec.SessionID,
		ExecutionID:      exec.ID,
		PolicySnapshotID: exec.PolicySnapshotID,
		TraceID:          exec.TraceID,
		TriggerEventIDs:  append([]string(nil), exec.TriggerEventIDs...),
		Status:           responsedomain.StatusPreparing,
		MaxIterations:    maxResponsePreparationIterations,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if r.writes != nil {
		if err := r.writes.SaveResponse(ctx, record); err != nil {
			return responsedomain.Response{}, err
		}
	} else if err := r.repo.SaveResponse(ctx, record); err != nil {
		return responsedomain.Response{}, err
	}
	r.cacheResponse(record)
	return record, nil
}

func (r *Runner) updateResponseState(ctx context.Context, record responsedomain.Response, status responsedomain.Status, reason string, mutate func(*responsedomain.Response)) error {
	current := record
	if cached, ok := r.cachedResponse(record.ExecutionID); ok {
		current = cached
	} else if record.ID != "" {
		stored, err := r.repo.GetResponse(ctx, record.ID)
		if err == nil {
			current = stored
		}
	}
	current.Status = status
	current.Reason = strings.TrimSpace(reason)
	current.UpdatedAt = time.Now().UTC()
	if mutate != nil {
		mutate(&current)
	}
	r.cacheResponse(current)
	if r.writes != nil {
		return r.writes.SaveResponse(ctx, current)
	}
	return r.repo.SaveResponse(ctx, current)
}

func (r *Runner) responseIDForExecution(ctx context.Context, executionID string) string {
	if cached, ok := r.cachedResponse(executionID); ok {
		return cached.ID
	}
	items, err := r.repo.ListResponses(ctx, responsedomain.Query{ExecutionID: executionID, Limit: 1})
	if err != nil || len(items) == 0 {
		return ""
	}
	r.cacheResponse(items[0])
	return items[0].ID
}

func (r *Runner) createAssistantMessageSequence(ctx context.Context, exec execution.TurnExecution, messages []string) ([]session.Event, error) {
	messages = normalizeResponseMessages(messages)
	batchID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	out := make([]session.Event, 0, len(messages))
	for i, message := range messages {
		event, err := r.sessions.CreateMessageEvent(ctx, exec.SessionID, "ai_agent", message, exec.ID, exec.TraceID, map[string]any{
			"step":              "compose_response",
			"response_batch_id": batchID,
			"message_index":     i,
			"message_count":     len(messages),
		}, false)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

type responseEnvelope struct {
	Messages []string `json:"messages"`
	Text     string   `json:"text"`
}

func parseResponseEnvelope(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var env responseEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err == nil {
		if len(env.Messages) > 0 {
			return env.Messages
		}
		if strings.TrimSpace(env.Text) != "" {
			return []string{env.Text}
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &env); err == nil {
			if len(env.Messages) > 0 {
				return env.Messages
			}
			if strings.TrimSpace(env.Text) != "" {
				return []string{env.Text}
			}
		}
	}
	return []string{trimmed}
}

func normalizeResponseMessages(messages []string) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		out = append(out, message)
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return []string{"Not sure I understand. Could you please say that another way?"}
	}
	return out
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

func latestCustomerText(events []session.Event) string {
	return latestText(events)
}

func wantsDeliveryUpdates(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return text != "" &&
		stringContainsAny(text, "update me", "keep me updated", "notify me", "let me know") &&
		stringContainsAny(text, "delivery", "shipping", "order status", "package")
}

func wantsAppointmentReminder(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return text != "" && stringContainsAny(text, "remind me", "appointment reminder", "reminder for my appointment", "notify me about my appointment")
}

func stringContainsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(part))) {
			return true
		}
	}
	return false
}

func latestAssistant(events []session.Event) session.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source == "ai_agent" {
			return events[i]
		}
	}
	return session.Event{}
}

func assistantEventsForExecution(events []session.Event, executionID string) []session.Event {
	var out []session.Event
	for _, event := range events {
		if event.ExecutionID == executionID && event.Source == "ai_agent" {
			out = append(out, event)
		}
	}
	return out
}

func eventIDs(events []session.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.ID) != "" {
			out = append(out, event.ID)
		}
	}
	return out
}

func firstString(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

func hasEvent(events []session.Event, eventID string) bool {
	for _, event := range events {
		if event.ID == eventID {
			return true
		}
	}
	return false
}

func mergeMaps(base map[string]any, extra map[string]any) map[string]any {
	if base == nil && extra == nil {
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

func stableChecksum(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (r *Runner) compileKnowledgeSource(ctx context.Context, source knowledge.Source, updateSource bool) (knowledge.Snapshot, error) {
	if source.Kind != "folder" {
		return knowledge.Snapshot{}, fmt.Errorf("only folder knowledge sources are supported")
	}
	root, err := validatedKnowledgePath(source.URI)
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	snapshot, err := knowledgecompiler.NewWithEmbedder(r.repo, r.router).CompileFolder(ctx, knowledgecompiler.Input{
		ScopeKind: source.ScopeKind,
		ScopeID:   source.ScopeID,
		SourceID:  source.ID,
		Root:      root,
	})
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	if updateSource {
		source.Status = "ready"
		source.UpdatedAt = time.Now().UTC()
		source.Checksum, _ = knowledgeSourceChecksum(source)
		source.Metadata = mergeMaps(source.Metadata, map[string]any{"current_checksum": source.Checksum, "snapshot_id": snapshot.ID})
		if err := r.repo.SaveKnowledgeSource(ctx, source); err != nil {
			return knowledge.Snapshot{}, err
		}
	}
	return snapshot, nil
}

func validatedKnowledgePath(uri string) (string, error) {
	path := strings.TrimSpace(uri)
	if path == "" {
		return "", fmt.Errorf("knowledge source uri is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(os.Getenv("KNOWLEDGE_SOURCE_ROOT"))
	if root == "" {
		return abs, nil
	}
	allowedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(allowedRoot, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("knowledge source path must stay within KNOWLEDGE_SOURCE_ROOT")
	}
	return abs, nil
}

func knowledgeSourceChecksum(source knowledge.Source) (string, error) {
	root, err := validatedKnowledgePath(source.URI)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return stableChecksum(root + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano)), nil
	}
	var parts []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") && !strings.HasSuffix(strings.ToLower(info.Name()), ".txt") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts = append(parts, rel+"\x00"+info.ModTime().UTC().Format(time.RFC3339Nano)+"\x00"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(parts)
	return stableChecksum(strings.Join(parts, "\n")), nil
}
