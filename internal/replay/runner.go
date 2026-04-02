package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/policy"
	replaydomain "github.com/sahal/parmesan/internal/domain/replay"
	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
)

type Runner struct {
	repo     store.Repository
	writes   *asyncwrite.Queue
	interval time.Duration
}

func New(repo store.Repository, writes *asyncwrite.Queue) *Runner {
	return &Runner{
		repo:     repo,
		writes:   writes,
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
	items, err := r.repo.ListRunnableEvalRuns(ctx, time.Now().UTC())
	if err != nil {
		return
	}
	for _, item := range items {
		_ = r.process(ctx, item)
	}
}

func (r *Runner) process(ctx context.Context, run replaydomain.Run) error {
	if run.Status == replaydomain.StatusSucceeded || run.Status == replaydomain.StatusFailed {
		return nil
	}
	run.Status = replaydomain.StatusRunning
	run.UpdatedAt = time.Now().UTC()
	if err := r.repo.UpdateEvalRun(ctx, run); err != nil {
		return err
	}

	exec, _, err := r.repo.GetExecution(ctx, run.SourceExecutionID)
	if err != nil {
		return r.fail(ctx, run, err)
	}
	events, err := r.repo.ListEvents(ctx, exec.SessionID)
	if err != nil {
		return r.fail(ctx, run, err)
	}
	journeys, err := r.repo.ListJourneyInstances(ctx, exec.SessionID)
	if err != nil {
		return r.fail(ctx, run, err)
	}
	catalog, err := r.repo.ListCatalogEntries(ctx)
	if err != nil {
		return r.fail(ctx, run, err)
	}
	bundles, err := r.repo.ListBundles(ctx)
	if err != nil {
		return r.fail(ctx, run, err)
	}

	activeView, err := policyruntime.Resolve(events, selectBundles(bundles, run.ActiveBundleID, exec.PolicyBundleID), journeys, catalog)
	if err != nil {
		return r.fail(ctx, run, err)
	}
	result := map[string]any{
		"execution_id": exec.ID,
		"active":       summarizeView(activeView),
	}
	diff := map[string]any{}

	if run.Type == replaydomain.TypeShadow && run.ShadowBundleID != "" {
		shadowView, err := policyruntime.Resolve(events, selectBundles(bundles, run.ShadowBundleID, exec.PolicyBundleID), journeys, catalog)
		if err != nil {
			return r.fail(ctx, run, err)
		}
		result["shadow"] = summarizeView(shadowView)
		diff = map[string]any{
			"guidelines":          changedStrings(guidelineIDs(activeView.MatchedGuidelines), guidelineIDs(shadowView.MatchedGuidelines)),
			"tools":               changedStrings(activeView.ExposedTools, shadowView.ExposedTools),
			"templates":           changedStrings(templateIDs(activeView), templateIDs(shadowView)),
			"composition_mode":    changedScalar(activeView.CompositionMode, shadowView.CompositionMode),
			"no_match":            changedScalar(activeView.NoMatch, shadowView.NoMatch),
			"journey_id":          changedScalar(journeyID(activeView), journeyID(shadowView)),
			"journey_state":       changedScalar(journeyStateID(activeView), journeyStateID(shadowView)),
			"journey_decision":    changedScalar(activeView.JourneyDecision.Action, shadowView.JourneyDecision.Action),
			"selected_tool":       changedScalar(activeView.ToolDecision.SelectedTool, shadowView.ToolDecision.SelectedTool),
			"tool_can_run":        changedBool(activeView.ToolDecision.CanRun, shadowView.ToolDecision.CanRun),
			"tool_missing_args":   changedStrings(activeView.ToolDecision.MissingArguments, shadowView.ToolDecision.MissingArguments),
			"tool_invalid_args":   changedStrings(activeView.ToolDecision.InvalidArguments, shadowView.ToolDecision.InvalidArguments),
			"tool_missing_issues": changedArgumentIssues(activeView.ToolDecision.MissingIssues, shadowView.ToolDecision.MissingIssues),
			"tool_invalid_issues": changedArgumentIssues(activeView.ToolDecision.InvalidIssues, shadowView.ToolDecision.InvalidIssues),
			"suppressed":          changedStrings(suppressedIDs(activeView), suppressedIDs(shadowView)),
			"reapply":             changedStrings(reapplyIDs(activeView), reapplyIDs(shadowView)),
			"customer_blocked":    changedStrings(customerBlockedIDs(activeView), customerBlockedIDs(shadowView)),
			"response_revision":   changedBool(activeView.ResponseAnalysis.NeedsRevision, shadowView.ResponseAnalysis.NeedsRevision),
			"response_strict":     changedBool(activeView.ResponseAnalysis.NeedsStrictMode, shadowView.ResponseAnalysis.NeedsStrictMode),
			"arqs":                changedStrings(arqNames(activeView), arqNames(shadowView)),
		}
	}

	run.ResultJSON = mustJSON(result)
	run.DiffJSON = mustJSON(diff)
	run.Status = replaydomain.StatusSucceeded
	run.UpdatedAt = time.Now().UTC()
	if err := r.repo.UpdateEvalRun(ctx, run); err != nil {
		return err
	}
	if run.ProposalID != "" {
		if err := r.updateProposalSummary(ctx, run, diff, result); err != nil {
			return err
		}
	}
	if r.writes != nil {
		_ = r.writes.AppendAuditRecord(ctx, audit.Record{
			ID:        fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Kind:      "replay.completed",
			TraceID:   fmt.Sprintf("trace_%d", time.Now().UnixNano()),
			Message:   "replay or shadow evaluation completed",
			Fields:    map[string]any{"eval_run_id": run.ID, "type": run.Type},
			CreatedAt: time.Now().UTC(),
		})
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, run replaydomain.Run, err error) error {
	run.Status = replaydomain.StatusFailed
	run.LastError = err.Error()
	run.UpdatedAt = time.Now().UTC()
	_ = r.repo.UpdateEvalRun(ctx, run)
	return err
}

func (r *Runner) updateProposalSummary(ctx context.Context, run replaydomain.Run, diff map[string]any, result map[string]any) error {
	proposal, err := r.repo.GetProposal(ctx, run.ProposalID)
	if err != nil {
		return err
	}
	proposal.EvalSummaryJSON = mustJSON(map[string]any{
		"eval_run_id": run.ID,
		"type":        run.Type,
		"result":      result,
		"diff":        diff,
	})
	proposal.ReplayScore = computeReplayScore(diff)
	proposal.SafetyScore = proposal.ReplayScore
	proposal.UpdatedAt = time.Now().UTC()
	return r.repo.SaveProposal(ctx, proposal)
}

func selectBundles(bundles []policy.Bundle, explicit string, fallback string) []policy.Bundle {
	if explicit != "" {
		for _, item := range bundles {
			if item.ID == explicit {
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

func summarizeView(view policyruntime.ResolvedView) map[string]any {
	out := map[string]any{
		"composition_mode":    view.CompositionMode,
		"no_match":            view.NoMatch,
		"observations":        observationIDs(view.MatchedObservations),
		"guidelines":          guidelineIDs(view.MatchedGuidelines),
		"suppressed":          suppressedIDs(view),
		"reapply":             reapplyIDs(view),
		"customer_blocked":    customerBlockedIDs(view),
		"response_analysis":   view.ResponseAnalysis,
		"batch_results":       view.BatchResults,
		"prompt_set_versions": view.PromptSetVersions,
		"tools":               view.ExposedTools,
		"selected_tool":       view.ToolDecision.SelectedTool,
		"tool_can_run":        view.ToolDecision.CanRun,
		"tool_missing_args":   view.ToolDecision.MissingArguments,
		"tool_invalid_args":   view.ToolDecision.InvalidArguments,
		"tool_missing_issues": view.ToolDecision.MissingIssues,
		"tool_invalid_issues": view.ToolDecision.InvalidIssues,
		"templates":           templateIDs(view),
		"arqs":                arqNames(view),
	}
	if view.Bundle != nil {
		out["bundle_id"] = view.Bundle.ID
	}
	if view.ActiveJourney != nil {
		out["journey_id"] = view.ActiveJourney.ID
	}
	if view.ActiveJourneyState != nil {
		out["journey_state"] = view.ActiveJourneyState.ID
	}
	if view.JourneyDecision.Action != "" {
		out["journey_decision"] = view.JourneyDecision
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

func templateIDs(view policyruntime.ResolvedView) []string {
	out := make([]string, 0, len(view.CandidateTemplates))
	for _, item := range view.CandidateTemplates {
		out = append(out, item.ID)
	}
	return out
}

func suppressedIDs(view policyruntime.ResolvedView) []string {
	out := make([]string, 0, len(view.SuppressedGuidelines))
	for _, item := range view.SuppressedGuidelines {
		out = append(out, item.ID)
	}
	return out
}

func arqNames(view policyruntime.ResolvedView) []string {
	out := make([]string, 0, len(view.ARQResults))
	for _, item := range view.ARQResults {
		out = append(out, item.Name)
	}
	return out
}

func reapplyIDs(view policyruntime.ResolvedView) []string {
	out := make([]string, 0, len(view.ReapplyDecisions))
	for _, item := range view.ReapplyDecisions {
		if item.ShouldReapply {
			out = append(out, item.ID)
		}
	}
	return out
}

func customerBlockedIDs(view policyruntime.ResolvedView) []string {
	out := make([]string, 0, len(view.CustomerDecisions))
	for _, item := range view.CustomerDecisions {
		if len(item.MissingCustomerData) > 0 {
			out = append(out, item.ID)
		}
	}
	return out
}

func changedStrings(left, right []string) map[string][]string {
	return map[string][]string{
		"only_left":  difference(left, right),
		"only_right": difference(right, left),
	}
}

func changedScalar(left, right string) map[string]string {
	if left == right {
		return map[string]string{}
	}
	return map[string]string{
		"left":  left,
		"right": right,
	}
}

func changedBool(left, right bool) map[string]bool {
	if left == right {
		return map[string]bool{}
	}
	return map[string]bool{
		"left":  left,
		"right": right,
	}
}

func changedArgumentIssues(left, right []policyruntime.ToolArgumentIssue) map[string]any {
	if mustJSON(left) == mustJSON(right) {
		return map[string]any{}
	}
	return map[string]any{
		"left":  left,
		"right": right,
	}
}

func journeyID(view policyruntime.ResolvedView) string {
	if view.ActiveJourney == nil {
		return ""
	}
	return view.ActiveJourney.ID
}

func journeyStateID(view policyruntime.ResolvedView) string {
	if view.ActiveJourneyState == nil {
		return ""
	}
	return view.ActiveJourneyState.ID
}

func computeReplayScore(diff map[string]any) float64 {
	if len(diff) == 0 {
		return 1
	}
	total := 0
	for _, value := range diff {
		if groups, ok := value.(map[string][]string); ok {
			total += len(groups["only_left"]) + len(groups["only_right"])
			continue
		}
		if groups, ok := value.(map[string]any); ok {
			for _, key := range []string{"only_left", "only_right"} {
				if raw, ok := groups[key].([]string); ok {
					total += len(raw)
				}
			}
		}
	}
	score := 1 - float64(total)*0.05
	if score < 0 {
		return 0
	}
	return score
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

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
