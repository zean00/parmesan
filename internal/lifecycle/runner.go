package lifecycle

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	knowledgelearning "github.com/sahal/parmesan/internal/knowledge/learning"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/observability"
	"github.com/sahal/parmesan/internal/sessionsvc"
	"github.com/sahal/parmesan/internal/sessionwatch"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/toolruntime"
)

type Runner struct {
	repo     store.Repository
	writes   *asyncwrite.Queue
	router   *model.Router
	sessions *sessionsvc.Service
	invoker  *toolruntime.Invoker
	interval time.Duration
}

func New(repo store.Repository, writes *asyncwrite.Queue, router *model.Router) *Runner {
	return &Runner{
		repo:     repo,
		writes:   writes,
		router:   router,
		sessions: sessionsvc.New(repo, writes),
		invoker:  toolruntime.New(),
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
	now := time.Now().UTC()
	r.processIdleSessions(ctx, now)
	r.processRunnableWatches(ctx, now)
}

func (r *Runner) processIdleSessions(ctx context.Context, now time.Time) {
	ctx, done := observability.Current().StartSpan(ctx, "lifecycle", "process_idle_sessions")
	defer done("ok")
	sessions, err := r.repo.ListSessions(ctx)
	if err != nil {
		done("error")
		return
	}
	execs, _ := r.repo.ListExecutions(ctx)
	for _, sess := range sessions {
		bundle, _ := r.sessionBundle(ctx, sess)
		if !r.isLifecycleEligible(sess, bundle, now) {
			continue
		}
		if sessionModeManual(sess) {
			continue
		}
		if hasSessionApprovals(ctx, r.repo, sess.ID) {
			continue
		}
		if hasOpenExecutions(execs, sess.ID) {
			continue
		}
		decision, reason := r.decideLifecycleAction(ctx, sess, bundle)
		switch decision {
		case "ask_followup":
			_ = r.askFollowup(ctx, &sess, bundle.LifecyclePolicy, reason, now)
		case "close_session":
			_ = r.closeSession(ctx, &sess, reason, now)
		case "schedule_watch":
			if ok := r.ensureInferredWatch(ctx, &sess, bundle, eventsForSession(ctx, r.repo, sess.ID), now); ok {
				_ = r.markKeep(ctx, &sess, reason, now)
			} else {
				_ = r.askFollowup(ctx, &sess, bundle.LifecyclePolicy, "watch_requested_without_tool_context", now)
			}
		case "keep_open":
			_ = r.markKeep(ctx, &sess, reason, now)
		default:
			sess.IdleCheckedAt = now
			_ = r.repo.UpdateSession(ctx, sess)
		}
	}
}

func (r *Runner) processRunnableWatches(ctx context.Context, now time.Time) {
	items, err := r.repo.ListRunnableSessionWatches(ctx, now)
	if err != nil {
		return
	}
	for _, item := range items {
		_ = r.processWatch(ctx, item, now)
	}
}

func (r *Runner) isLifecycleEligible(sess session.Session, bundle policy.Bundle, now time.Time) bool {
	if sess.Status == session.StatusClosed {
		return false
	}
	last := sess.LastActivityAt
	if last.IsZero() {
		last = sess.CreatedAt
	}
	switch sess.Status {
	case session.StatusAwaitingCustomer:
		return now.Sub(last) >= awaitingCloseAfter(bundle.LifecyclePolicy)
	case session.StatusSessionKeep:
		return now.Sub(last) >= keepRecheckAfter(bundle.LifecyclePolicy)
	default:
		return now.Sub(last) >= idleCandidateAfter(bundle.LifecyclePolicy)
	}
}

func isLifecycleEligible(sess session.Session, now time.Time) bool {
	return (&Runner{}).isLifecycleEligible(sess, policy.Bundle{}, now)
}

func sessionModeManual(sess session.Session) bool {
	return strings.EqualFold(strings.TrimSpace(sess.Mode), "manual")
}

func hasOpenExecutions(execs []execution.TurnExecution, sessionID string) bool {
	for _, exec := range execs {
		if exec.SessionID != sessionID {
			continue
		}
		switch exec.Status {
		case execution.StatusPending, execution.StatusRunning, execution.StatusWaiting, execution.StatusBlocked:
			return true
		}
	}
	return false
}

func hasSessionApprovals(ctx context.Context, repo store.Repository, sessionID string) bool {
	items, err := repo.ListApprovalSessions(ctx, sessionID)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.Status == approval.StatusPending {
			return true
		}
	}
	return false
}

func (r *Runner) sessionBundle(ctx context.Context, sess session.Session) (policy.Bundle, bool) {
	if strings.TrimSpace(sess.AgentID) == "" {
		return defaultLifecycleBundle(), true
	}
	profile, err := r.repo.GetAgentProfile(ctx, sess.AgentID)
	if err != nil || strings.TrimSpace(profile.DefaultPolicyBundleID) == "" {
		return defaultLifecycleBundle(), true
	}
	bundles, err := r.repo.ListBundles(ctx)
	if err != nil {
		return defaultLifecycleBundle(), true
	}
	for _, item := range bundles {
		if item.ID == profile.DefaultPolicyBundleID {
			return item, true
		}
	}
	return defaultLifecycleBundle(), true
}

func defaultLifecycleBundle() policy.Bundle {
	return policy.Bundle{
		Semantics: policy.SemanticsPolicy{
			Signals: []policy.SemanticSignal{
				{ID: "delivery", Phrases: []string{"delivery", "shipping"}, Tokens: []string{"delivery", "shipping", "tracking", "package"}},
				{ID: "appointment_reminder", Phrases: []string{"appointment reminder"}, Tokens: []string{"remind", "appointment", "notify"}},
			},
		},
		WatchCapabilities: []policy.WatchCapability{
			{
				ID:                    "delivery_status_watch",
				Kind:                  sessionwatch.KindDeliveryStatus,
				ScheduleStrategy:      "poll",
				TriggerSignals:        []string{"delivery", "tracking", "order status", "package", "update me", "keep me updated", "notify me", "let me know"},
				ToolMatchTerms:        []string{"order", "delivery", "shipping", "tracking"},
				SubjectKeys:           []string{"order_id", "tracking_id", "shipment_id", "package_id", "id"},
				PollIntervalSeconds:   int((15 * time.Minute) / time.Second),
				StopCondition:         "delivered",
				AllowLifecycleFallback: true,
			},
			{
				ID:                    "appointment_reminder_watch",
				Kind:                  sessionwatch.KindAppointmentReminder,
				ScheduleStrategy:      "reminder",
				TriggerSignals:        []string{"appointment_reminder", "remind me", "appointment reminder", "reminder for my appointment", "notify me about my appointment"},
				ToolMatchTerms:        []string{"appointment", "schedule", "calendar", "booking"},
				SubjectKeys:           []string{"appointment_id", "booking_id", "id"},
				RequiredFields:        []string{"appointment_at"},
				ReminderLeadSeconds:   3600,
				AllowLifecycleFallback: true,
			},
		},
		LifecyclePolicy: policy.LifecyclePolicy{
			ID:                        "default_lifecycle_policy",
			FollowupMessage:           "Do you need any more help with this?",
			ResolutionSignals:         []string{"thanks", "thank you", "that helps", "all good", "solved", "ok got it"},
			DeliveryUpdateSignals:     []string{"update me", "keep me updated", "notify me", "let me know", "delivery", "shipping", "order status", "package"},
			AppointmentReminderSignals: []string{"remind me", "appointment reminder", "reminder for my appointment", "notify me about my appointment"},
		},
	}
}

func (r *Runner) decideLifecycleAction(ctx context.Context, sess session.Session, bundle policy.Bundle) (string, string) {
	events, err := r.repo.ListEvents(ctx, sess.ID)
	if err != nil || len(events) == 0 {
		return "", ""
	}
	if sess.Status == session.StatusAwaitingCustomer && sess.FollowupCount > 0 {
		return "close_session", "no_customer_reply_after_followup"
	}
	if shouldScheduleWatch(bundle, events) {
		return "schedule_watch", "customer_requested_delivery_updates"
	}
	if latestCustomerLooksResolved(bundle.LifecyclePolicy, events) {
		return "close_session", "customer_indicated_resolution"
	}
	if latestAgentAskedFollowup(events) {
		return "close_session", "followup_already_sent"
	}
	if action, reason := r.llmLifecycleDecision(ctx, sess, bundle, events); action != "" {
		return action, reason
	}
	return "ask_followup", "idle_conversation_unclear"
}

func latestCustomerLooksResolved(policyDef policy.LifecyclePolicy, events []session.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "customer" {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(sessionEventText(events[i])))
		return containsAny(text, policyDef.ResolutionSignals...)
	}
	return false
}

func latestAgentAskedFollowup(events []session.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "ai_agent" {
			continue
		}
		if events[i].Metadata != nil && events[i].Metadata["lifecycle_kind"] == "idle_followup" {
			return true
		}
		text := strings.ToLower(strings.TrimSpace(sessionEventText(events[i])))
		if containsAny(text, "anything else i can help", "need any more help", "anything more i can help") {
			return true
		}
		return false
	}
	return false
}

func shouldScheduleWatch(bundle policy.Bundle, events []session.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		text := strings.ToLower(strings.TrimSpace(sessionEventText(events[i])))
		if text == "" {
			continue
		}
		for _, capability := range bundle.WatchCapabilities {
			if !capability.AllowLifecycleFallback {
				continue
			}
			if containsAny(text, capability.TriggerSignals...) || textMatchesSignals(bundle.Semantics, text, capability.TriggerSignals) {
				return true
			}
		}
	}
	return false
}

func (r *Runner) llmLifecycleDecision(ctx context.Context, sess session.Session, bundle policy.Bundle, events []session.Event) (string, string) {
	if r.router == nil {
		return "", ""
	}
	type decision struct {
		Action    string `json:"action"`
		Rationale string `json:"rationale"`
	}
	transcript := formatRecentTranscript(events, 10)
	prompt := strings.TrimSpace(`Decide conversation lifecycle for a customer support session.
Return strict JSON: {"action":"close_session|ask_followup|keep_open|schedule_watch","rationale":"string"}.
Choose close_session only if the conversation is clearly resolved.
Choose ask_followup if unclear and a single polite follow-up is appropriate.
Choose schedule_watch only if the customer expects periodic updates that fit the lifecycle policy.
Session status: ` + string(sess.Status) + `
Followup count: ` + fmt.Sprint(sess.FollowupCount) + `
Lifecycle policy: ` + bundle.LifecyclePolicy.ID + `
Transcript:
` + transcript)
	resp, err := r.router.Generate(ctx, model.CapabilityStructured, model.Request{Prompt: prompt})
	if err != nil {
		return "", ""
	}
	var parsed decision
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Text)), &parsed); err != nil {
		return "", ""
	}
	switch strings.TrimSpace(parsed.Action) {
	case "close_session", "ask_followup", "keep_open", "schedule_watch":
		return strings.TrimSpace(parsed.Action), strings.TrimSpace(parsed.Rationale)
	default:
		return "", ""
	}
}

func (r *Runner) askFollowup(ctx context.Context, sess *session.Session, policyDef policy.LifecyclePolicy, reason string, now time.Time) error {
	if sess == nil {
		return nil
	}
	message := firstNonEmpty(strings.TrimSpace(policyDef.FollowupMessage), "Do you need any more help with this?")
	if _, err := r.sessions.CreateMessageEvent(ctx, sess.ID, "ai_agent", message, "", traceIDForSession(sess.ID, "idle_followup"), map[string]any{
		"lifecycle_kind": "idle_followup",
		"reason":         reason,
	}, false); err != nil {
		return err
	}
	sess.Status = session.StatusAwaitingCustomer
	sess.AwaitingCustomerSince = now
	sess.IdleCheckedAt = now
	sess.FollowupCount++
	sess.KeepReason = ""
	sess.CloseReason = ""
	if err := r.repo.UpdateSession(ctx, *sess); err != nil {
		return err
	}
	if err := knowledgelearning.New(r.repo).CompileDeferredFeedbackRecords(ctx, *sess); err != nil {
		return err
	}
	r.appendTrace(ctx, audit.Record{
		ID:        fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:      "session.lifecycle.followup",
		SessionID: sess.ID,
		TraceID:   traceIDForSession(sess.ID, "idle_followup"),
		Message:   "session lifecycle follow-up sent",
		Fields:    map[string]any{"reason": reason},
		CreatedAt: now,
	})
	return nil
}

func (r *Runner) closeSession(ctx context.Context, sess *session.Session, reason string, now time.Time) error {
	if sess == nil {
		return nil
	}
	sess.Status = session.StatusClosed
	sess.ClosedAt = now
	sess.CloseReason = firstNonEmpty(reason, "lifecycle_closed")
	sess.IdleCheckedAt = now
	sess.AwaitingCustomerSince = time.Time{}
	sess.KeepReason = ""
	if err := r.repo.UpdateSession(ctx, *sess); err != nil {
		return err
	}
	r.appendTrace(ctx, audit.Record{
		ID:        fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:      "session.lifecycle.closed",
		SessionID: sess.ID,
		TraceID:   traceIDForSession(sess.ID, "closed"),
		Message:   "session closed by lifecycle worker",
		Fields:    map[string]any{"reason": sess.CloseReason},
		CreatedAt: now,
	})
	return nil
}

func (r *Runner) markKeep(ctx context.Context, sess *session.Session, reason string, now time.Time) error {
	if sess == nil {
		return nil
	}
	sess.Status = session.StatusSessionKeep
	sess.KeepReason = firstNonEmpty(reason, "session_keep")
	sess.LastActivityAt = now
	sess.IdleCheckedAt = now
	sess.AwaitingCustomerSince = time.Time{}
	sess.ClosedAt = time.Time{}
	sess.CloseReason = ""
	if err := r.repo.UpdateSession(ctx, *sess); err != nil {
		return err
	}
	if err := knowledgelearning.New(r.repo).CompileDeferredFeedbackRecords(ctx, *sess); err != nil {
		return err
	}
	r.appendTrace(ctx, audit.Record{
		ID:        fmt.Sprintf("trace_%d", now.UnixNano()),
		Kind:      "session.lifecycle.keep",
		SessionID: sess.ID,
		TraceID:   traceIDForSession(sess.ID, "keep"),
		Message:   "session kept open by lifecycle worker",
		Fields:    map[string]any{"reason": sess.KeepReason},
		CreatedAt: now,
	})
	return nil
}

func (r *Runner) ensureInferredWatch(ctx context.Context, sess *session.Session, bundle policy.Bundle, events []session.Event, now time.Time) bool {
	if sess == nil {
		return false
	}
	if intent, ok := r.inferLifecycleWatchIntent(ctx, *sess, bundle, events, now); ok {
		_, _, err := sessionwatch.EnsureSessionWatch(ctx, r.repo, *sess, intent, now)
		return err == nil
	}
	return false
}

func (r *Runner) inferLifecycleWatchIntent(ctx context.Context, sess session.Session, bundle policy.Bundle, events []session.Event, now time.Time) (sessionwatch.UpdateIntent, bool) {
	for _, capability := range bundle.WatchCapabilities {
		if !capability.AllowLifecycleFallback {
			continue
		}
		if !latestCustomerRequestsCapability(bundle.Semantics, capability, events) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(capability.ScheduleStrategy), "reminder") {
			if appointmentAt, ok := latestAppointmentTime(events, now); ok {
				args := map[string]any{"appointment_at": appointmentAt.UTC().Format(time.RFC3339)}
				subjectRef := appointmentAt.UTC().Format(time.RFC3339)
				return sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceLifecycle, capability.ID, subjectRef, args, now)
			}
			continue
		}
		return r.pollingWatchIntentFromLatestExecution(ctx, sess, capability, now)
	}
	return sessionwatch.UpdateIntent{}, false
}

func (r *Runner) pollingWatchIntentFromLatestExecution(ctx context.Context, sess session.Session, capability policy.WatchCapability, now time.Time) (sessionwatch.UpdateIntent, bool) {
	execs, err := r.repo.ListExecutions(ctx)
	if err != nil {
		return sessionwatch.UpdateIntent{}, false
	}
	var latestExec execution.TurnExecution
	for _, exec := range execs {
		if exec.SessionID != sess.ID {
			continue
		}
		if latestExec.ID == "" || exec.CreatedAt.After(latestExec.CreatedAt) {
			latestExec = exec
		}
	}
	if latestExec.ID == "" {
		return sessionwatch.UpdateIntent{}, false
	}
	runs, err := r.repo.ListToolRuns(ctx, latestExec.ID)
	if err != nil {
		return sessionwatch.UpdateIntent{}, false
	}
	var chosen toolRunSeed
	for _, run := range runs {
		args := parseJSONMap(run.InputJSON)
		if containsAny(strings.ToLower(run.ToolID), capability.ToolMatchTerms...) {
			chosen = toolRunSeed{ToolID: run.ToolID, Arguments: args}
		}
	}
	if chosen.ToolID == "" {
		return sessionwatch.UpdateIntent{}, false
	}
	subjectRef := sessionwatch.ExtractSubjectRef(chosen.Arguments, capability.SubjectKeys...)
	return sessionwatch.BuildIntentFromCapability(capability, sessionwatch.SourceLifecycle, chosen.ToolID, subjectRef, chosen.Arguments, now)
}

type toolRunSeed struct {
	ToolID    string
	Arguments map[string]any
}

func latestCustomerRequestsCapability(sem policy.SemanticsPolicy, capability policy.WatchCapability, events []session.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "customer" {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(sessionEventText(events[i])))
		if containsAny(text, capability.TriggerSignals...) || textMatchesSignals(sem, text, capability.TriggerSignals) {
			return true
		}
		return false
	}
	return false
}

func latestAppointmentTime(events []session.Event, now time.Time) (time.Time, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Source != "customer" {
			continue
		}
		if parsed, ok := sessionwatch.ParseAppointmentTimeFromText(sessionEventText(events[i]), now); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func textMatchesSignals(sem policy.SemanticsPolicy, text string, signals []string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" || len(signals) == 0 {
		return false
	}
	for _, signal := range signals {
		signal = strings.ToLower(strings.TrimSpace(signal))
		if signal == "" {
			continue
		}
		if strings.Contains(text, signal) {
			return true
		}
		for _, item := range sem.Signals {
			if !strings.EqualFold(item.ID, signal) {
				continue
			}
			for _, phrase := range item.Phrases {
				if strings.Contains(text, strings.ToLower(strings.TrimSpace(phrase))) {
					return true
				}
			}
			for _, token := range item.Tokens {
				if strings.Contains(text, strings.ToLower(strings.TrimSpace(token))) {
					return true
				}
			}
			for _, alias := range item.Aliases {
				if strings.Contains(text, strings.ToLower(strings.TrimSpace(alias))) {
					return true
				}
			}
		}
	}
	return false
}

func (r *Runner) processWatch(ctx context.Context, watch session.Watch, now time.Time) error {
	ctx, done := observability.Current().StartSpan(ctx, "lifecycle", "process_watch")
	defer done("ok")
	sess, err := r.repo.GetSession(ctx, watch.SessionID)
	if err != nil {
		done("error")
		return err
	}
	if sess.Status == session.StatusClosed {
		watch.Status = session.WatchStatusStopped
		watch.UpdatedAt = now
		return r.repo.SaveSessionWatch(ctx, watch)
	}
	if watch.Kind == sessionwatch.KindAppointmentReminder {
		message := formatWatchUpdateMessage(watch.Kind, watch.Arguments)
		if _, err := r.sessions.CreateMessageEvent(ctx, watch.SessionID, "ai_agent", message, "", traceIDForSession(watch.SessionID, watch.ID), map[string]any{
			"lifecycle_kind": "watch_update",
			"watch_id":       watch.ID,
			"watch_kind":     watch.Kind,
		}, false); err != nil {
			return err
		}
		sess.Status = session.StatusSessionKeep
		sess.KeepReason = "background_watch_update"
		sess.LastActivityAt = now
		if err := r.repo.UpdateSession(ctx, sess); err != nil {
			return err
		}
		watch.Status = session.WatchStatusStopped
		watch.LastCheckedAt = now
		watch.UpdatedAt = now
		watch.LastResultHash = stableHash(message)
		return r.repo.SaveSessionWatch(ctx, watch)
	}
	entry, ok := findCatalogEntryByID(ctx, r.repo, watch.ToolID)
	if !ok {
		watch.Status = session.WatchStatusFailed
		watch.UpdatedAt = now
		return r.repo.SaveSessionWatch(ctx, watch)
	}
	binding, err := r.repo.GetProvider(ctx, entry.ProviderID)
	if err != nil {
		return err
	}
	auth, err := r.repo.GetProviderAuthBinding(ctx, entry.ProviderID)
	if err != nil {
		auth = tool.AuthBinding{}
	}
	output, err := r.invoker.Invoke(ctx, binding, auth, entry, watch.Arguments)
	watch.LastCheckedAt = now
	watch.UpdatedAt = now
	previousHash := strings.TrimSpace(watch.LastResultHash)
	if err != nil {
		watch.NextRunAt = now.Add(watchPollInterval())
		return r.repo.SaveSessionWatch(ctx, watch)
	}
	hash := stableHash(mustJSONMap(output))
	watch.NextRunAt = now.Add(watch.PollInterval)
	watch.LastResultHash = hash
	if err := r.repo.SaveSessionWatch(ctx, watch); err != nil {
		return err
	}
	if hash != "" && hash != previousHash {
		message := formatWatchUpdateMessage(watch.Kind, output)
		if _, err := r.sessions.CreateMessageEvent(ctx, watch.SessionID, "ai_agent", message, "", traceIDForSession(watch.SessionID, watch.ID), map[string]any{
			"lifecycle_kind": "watch_update",
			"watch_id":       watch.ID,
			"watch_kind":     watch.Kind,
		}, false); err != nil {
			return err
		}
		sess.Status = session.StatusSessionKeep
		sess.KeepReason = "background_watch_update"
		sess.LastActivityAt = now
		if err := r.repo.UpdateSession(ctx, sess); err != nil {
			return err
		}
	}
	if shouldStopWatch(watch, output) {
		watch.Status = session.WatchStatusStopped
		watch.UpdatedAt = now
		return r.repo.SaveSessionWatch(ctx, watch)
	}
	return nil
}

func formatWatchUpdateMessage(kind string, output map[string]any) string {
	switch kind {
	case sessionwatch.KindDeliveryStatus:
		status := firstNonEmpty(stringify(output["delivery_status"]), stringify(output["status"]), stringify(output["state"]), stringify(output["tracking_status"]))
		if status != "" {
			return "I have an update on your delivery status: " + status + "."
		}
	case sessionwatch.KindAppointmentReminder:
		when := firstNonEmpty(stringify(output["appointment_at"]), stringify(output["scheduled_for"]), stringify(output["time"]), stringify(output["date"]))
		if when != "" {
			return "This is your reminder about the appointment scheduled for " + when + "."
		}
		return "This is your reminder about the upcoming appointment."
	}
	raw, _ := json.Marshal(output)
	return "I have an update on your request: " + string(raw)
}

func shouldStopWatch(watch session.Watch, output map[string]any) bool {
	target := strings.ToLower(strings.TrimSpace(watch.StopCondition))
	if target == "" {
		return false
	}
	status := strings.ToLower(firstNonEmpty(stringify(output["delivery_status"]), stringify(output["status"]), stringify(output["state"]), stringify(output["tracking_status"])))
	return status == target
}

func watchPollInterval() time.Duration {
	return 15 * time.Minute
}

func idleCandidateAfter(policyDef policy.LifecyclePolicy) time.Duration {
	if policyDef.IdleCandidateAfterMS > 0 {
		return time.Duration(policyDef.IdleCandidateAfterMS) * time.Millisecond
	}
	return durationEnv("SESSION_IDLE_CANDIDATE_AFTER", 30*time.Minute)
}

func awaitingCloseAfter(policyDef policy.LifecyclePolicy) time.Duration {
	if policyDef.AwaitingCloseAfterMS > 0 {
		return time.Duration(policyDef.AwaitingCloseAfterMS) * time.Millisecond
	}
	return durationEnv("SESSION_AWAITING_CLOSE_AFTER", 12*time.Hour)
}

func keepRecheckAfter(policyDef policy.LifecyclePolicy) time.Duration {
	if policyDef.KeepRecheckAfterMS > 0 {
		return time.Duration(policyDef.KeepRecheckAfterMS) * time.Millisecond
	}
	return durationEnv("SESSION_KEEP_RECHECK_AFTER", 30*time.Minute)
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(strings.ToLower(getenv(key)))
	if raw == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}

var getenv = os.Getenv

func sessionEventText(event session.Event) string {
	var parts []string
	for _, part := range event.Content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, " ")
}

func formatRecentTranscript(events []session.Event, limit int) string {
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		text := strings.TrimSpace(sessionEventText(event))
		if text == "" {
			continue
		}
		lines = append(lines, event.Source+": "+text)
	}
	return strings.Join(lines, "\n")
}

func eventsForSession(ctx context.Context, repo store.Repository, sessionID string) []session.Event {
	items, err := repo.ListEvents(ctx, sessionID)
	if err != nil {
		return nil
	}
	return items
}

func findCatalogEntryByID(ctx context.Context, repo store.Repository, toolID string) (tool.CatalogEntry, bool) {
	items, err := repo.ListCatalogEntries(ctx)
	if err != nil {
		return tool.CatalogEntry{}, false
	}
	for _, item := range items {
		if item.ID == toolID {
			return item, true
		}
	}
	return tool.CatalogEntry{}, false
}

func parseJSONMap(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func mustJSONMap(v map[string]any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func stableHash(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func traceIDForSession(sessionID, suffix string) string {
	return "trace_" + stableHash(sessionID, suffix, time.Now().UTC().Format(time.RFC3339Nano))
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(part))) {
			return true
		}
	}
	return false
}

func stringify(v any) string {
	return strings.TrimSpace(fmt.Sprint(v))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (r *Runner) appendTrace(ctx context.Context, record audit.Record) {
	if r.writes != nil {
		_ = r.writes.AppendAuditRecord(ctx, record)
		return
	}
	_ = r.repo.AppendAuditRecord(ctx, record)
}
