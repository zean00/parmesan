package policyruntime

import (
	"context"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/model"
	semantics "github.com/sahal/parmesan/internal/engine/semantics"
)

func buildToolStageResults(ctx context.Context, router *model.Router, matchCtx MatchingContext, state *matchingState, relationships []policy.Relationship, catalog []tool.CatalogEntry, argumentResolver ToolArgumentResolver) (ToolPlanStageResult, ToolDecisionStageResult) {
	plan, decision := buildToolPlan(ctx, router, matchCtx, state.activeJourney, state.activeJourneyState, state.journeyProgressStage.Decision, state.matchFinalizeStage.MatchedGuidelines, state.toolExposureStage.ExposedTools, state.toolExposureStage.ToolApprovals, relationships, catalog, argumentResolver)
	grounding := map[string]semantics.ToolGroundingEvidence{}
	selection := map[string]semantics.ToolSelectionEvidence{}
	if len(plan.Candidates) > 0 {
		selectionCache := newToolSelectionEvalCache(plan.Candidates)
		for _, candidate := range plan.Candidates {
			grounding[candidate.ToolID] = candidate.GroundingEvidence
			selection[candidate.ToolID] = selectionCache.evaluate(candidate, plan.SelectedTool)
		}
	}
	return ToolPlanStageResult{
			Plan: plan,
			Evaluation: ToolPlanEvaluation{
				Candidates:        append([]ToolCandidate(nil), plan.Candidates...),
				Batches:           append([]ToolCallBatchResult(nil), plan.Batches...),
				Grounding:         cloneToolGrounding(grounding),
				SelectionEvidence: cloneToolSelection(selection),
				SelectedTool:      plan.SelectedTool,
				SelectedTools:     append([]string(nil), plan.SelectedTools...),
				OverlappingGroups: cloneOverlappingGroups(plan.OverlappingGroups),
				Rationale:         plan.Rationale,
			},
		}, ToolDecisionStageResult{
			Decision: decision,
			Evaluation: ToolDecisionEvaluation{
				PlannedSelectedTool: plan.SelectedTool,
				SelectedTools:       append([]string(nil), plan.SelectedTools...),
				FinalSelectedTool:   decision.SelectedTool,
				ApprovalRequired:    decision.ApprovalRequired,
				CanRun:              decision.CanRun,
				Grounded:            decision.Grounded,
				MissingIssues:       append([]ToolArgumentIssue(nil), decision.MissingIssues...),
				InvalidIssues:       append([]ToolArgumentIssue(nil), decision.InvalidIssues...),
				Rationale:           decision.Rationale,
			},
		}
}
