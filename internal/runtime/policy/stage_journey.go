package policyruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/model"
	semantics "github.com/sahal/parmesan/internal/runtime/semantics"
)

func runJourneyBacktrackARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, instance *journey.Instance) JourneyDecision {
	if activeJourney == nil || activeState == nil || instance == nil {
		return JourneyDecision{}
	}
	if lateCompletedPreviousJourneyStep(matchCtx, activeJourney, activeState) {
		return JourneyDecision{}
	}
	rootID := ""
	if root := journeyRootState(activeJourney); root != nil {
		rootID = root.ID
	}
	if router != nil {
		var structured struct {
			RequiresBacktracking bool   `json:"requires_backtracking"`
			BacktrackToSame      bool   `json:"backtrack_to_same_journey_process"`
			Rationale            string `json:"rationale"`
		}
		prompt := buildJourneyBacktrackPrompt(matchCtx, activeJourney, activeState, rootID)
		if generateStructuredWithRetry(ctx, router, prompt, &structured) && structured.RequiresBacktracking {
			target := rootID
			nextState := ""
			if structured.BacktrackToSame {
				if selection := selectBestBacktrackEvaluation(matchCtx, activeJourney, instance.Path, activeState.ID, false); selectedBacktrackTargetID(selection) != "" {
					target = selectedBacktrackTargetID(selection)
					nextState = backtrackFastForwardState(matchCtx, activeJourney, target, activeState)
				} else if strings.TrimSpace(target) == "" {
					target = activeState.ID
				}
			}
			if !isLegalBacktrackTarget(instance.Path, target, rootID) {
				return JourneyDecision{}
			}
			return JourneyDecision{
				Action:       "backtrack",
				CurrentState: activeState.ID,
				BacktrackTo:  target,
				NextState:    nextState,
				Rationale:    firstNonEmpty(structured.Rationale, "journey should backtrack before proceeding"),
			}
		}
	}
	intent := semantics.DefaultJourneyBacktrackEvaluator{}.Evaluate(semantics.JourneyBacktrackContext{
		LatestCustomerText: matchCtx.LatestCustomerText,
	})
	if intent.RestartFromRoot && len(instance.Path) == 0 {
		intent.RestartFromRoot = false
		intent.RequiresBacktrack = false
	}
	if intent.RequiresBacktrack && !intent.RestartFromRoot {
		if selection := selectBestBacktrackEvaluation(matchCtx, activeJourney, instance.Path, activeState.ID, false); selectedBacktrackTargetID(selection) != "" {
			target := selectedBacktrackTargetID(selection)
			nextState := backtrackFastForwardState(matchCtx, activeJourney, target, activeState)
			if !isLegalBacktrackTarget(instance.Path, target, rootID) {
				return JourneyDecision{}
			}
			return JourneyDecision{
				Action:       "backtrack",
				CurrentState: activeState.ID,
				BacktrackTo:  target,
				NextState:    nextState,
				Rationale:    firstNonEmpty(intent.Rationale, "customer is revisiting a previous decision in the same journey process"),
			}
		}
	}
	if intent.RequiresBacktrack && intent.RestartFromRoot && rootID != "" && activeState.ID != rootID {
		if !isLegalBacktrackTarget(instance.Path, rootID, rootID) {
			return JourneyDecision{}
		}
		return JourneyDecision{
			Action:       "backtrack",
			CurrentState: activeState.ID,
			BacktrackTo:  rootID,
			Rationale:    firstNonEmpty(intent.Rationale, "customer changed the purpose of the journey, so restart from the beginning"),
		}
	}
	return JourneyDecision{}
}

func buildJourneyBacktrackStageResult(ctx context.Context, router *model.Router, bundle policy.Bundle, matchCtx MatchingContext, instances []journey.Instance) JourneyBacktrackStageResult {
	activeJourney, activeJourneyState, instance := resolveJourney(bundle, instances, matchCtx)
	intent := JourneyBacktrackIntent{}
	backtrackEvaluations := map[string]BacktrackCandidateEvaluation{}
	selectedBacktrack := BacktrackSelectionEvaluation{}
	if activeJourney != nil && activeJourneyState != nil && instance != nil {
		intent = semantics.DefaultJourneyBacktrackEvaluator{}.Evaluate(semantics.JourneyBacktrackContext{
			LatestCustomerText: matchCtx.LatestCustomerText,
		})
		if intent.RestartFromRoot && len(instance.Path) == 0 {
			intent.RestartFromRoot = false
			intent.RequiresBacktrack = false
		}
		backtrackEvaluations = buildBacktrackCandidateEvaluations(matchCtx, activeJourney, instance.Path, activeJourneyState.ID)
	}
	backtrack := runJourneyBacktrackARQ(ctx, router, matchCtx, activeJourney, activeJourneyState, instance)
	if activeJourney != nil && activeJourneyState != nil && instance != nil {
		selectedBacktrack = selectBestBacktrackEvaluationFromMap(
			visitedBacktrackCandidates(activeJourney, instance.Path, activeJourneyState.ID),
			backtrackEvaluations,
			backtrackFallbackID(activeJourney, instance.Path, activeJourneyState.ID, strings.EqualFold(backtrack.Action, "backtrack") && backtrack.BacktrackTo != "" && backtrack.BacktrackTo != activeJourneyState.ID),
		)
		if backtrack.BacktrackTo != "" && selectedBacktrack.Candidate.Selection.StateID == "" && selectedBacktrack.FallbackID != backtrack.BacktrackTo {
			selectedBacktrack.FallbackID = backtrack.BacktrackTo
		}
	}
	return JourneyBacktrackStageResult{
		Evaluation: JourneyBacktrackEvaluation{
			ActiveJourney:        activeJourney,
			ActiveJourneyState:   activeJourneyState,
			JourneyInstance:      instance,
			BacktrackIntent:      intent,
			BacktrackEvaluations: backtrackEvaluations,
			SelectedBacktrack:    selectedBacktrack,
		},
		Decision: backtrack,
	}
}

func runJourneyProgressARQ(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, instance *journey.Instance, backtrackDecision JourneyDecision) JourneyDecision {
	if activeJourney == nil || activeState == nil || instance == nil {
		return JourneyDecision{}
	}
	if strings.EqualFold(backtrackDecision.Action, "backtrack") {
		return backtrackDecision
	}
	if lateCompletedPreviousJourneyStep(matchCtx, activeJourney, activeState) {
		nextIDs := journeyNextStateIDs(*activeJourney, activeState.ID)
		nextState := ""
		if len(nextIDs) == 1 {
			nextState = nextIDs[0]
		}
		nextState = skipSatisfiedJourneyStates(matchCtx, activeJourney, activeState.ID, nextState, "")
		return JourneyDecision{
			Action:       "advance",
			CurrentState: activeState.ID,
			NextState:    nextState,
			Rationale:    "customer completed a prior missing journey step and the journey can continue forward",
		}
	}
	decision := JourneyDecision{
		Action:       "continue",
		CurrentState: activeState.ID,
		Rationale:    "active journey remains relevant for this turn",
	}
	if router != nil {
		if decision, ok := runJourneyProgressStructuredARQ(ctx, router, matchCtx, activeJourney, activeState); ok {
			return decision
		}
	}
	stateRelevance := cachedEvaluateJourneyState(matchCtx, *activeState, "", true)
	if len(activeState.When) > 0 && !stateRelevance.Satisfied {
		rootID := ""
		if root := journeyRootState(activeJourney); root != nil {
			rootID = root.ID
		}
		if selection := selectBestBacktrackEvaluation(matchCtx, activeJourney, instance.Path, activeState.ID, true); selectedBacktrackTargetID(selection) != "" && isLegalBacktrackTarget(instance.Path, selectedBacktrackTargetID(selection), rootID) {
			target := selectedBacktrackTargetID(selection)
			nextState := backtrackFastForwardState(matchCtx, activeJourney, target, activeState)
			decision.Action = "backtrack"
			decision.BacktrackTo = target
			decision.NextState = nextState
			decision.Rationale = "active node no longer matches the customer context, so the journey should backtrack"
			return decision
		}
		decision.Missing = append(decision.Missing, stateRelevance.Missing...)
		decision.Rationale = firstNonEmpty(stateRelevance.Rationale, "state entry condition is not yet satisfied")
		return decision
	}
	nextIDs := journeyNextStateIDs(*activeJourney, activeState.ID)
	if activeState.Type == "tool" && len(nextIDs) > 0 {
		if !journeyToolStateExecuted(matchCtx, *activeState) {
			decision.Missing = append(decision.Missing, "tool_execution")
			decision.Rationale = "tool state requires execution before the journey can advance"
			return decision
		}
		decision.Action = "advance"
		decision.NextState = nextIDs[0]
		decision.Rationale = "tool state can advance after execution"
		return decision
	}
	if strings.EqualFold(activeState.Kind, "fork") && len(nextIDs) > 0 {
		evaluation := selectNextJourneyNode(matchCtx, activeJourney, activeState, nextIDs)
		bestNextID := evaluation.Selection.StateID
		bestNextID = skipSatisfiedJourneyStates(matchCtx, activeJourney, activeState.ID, bestNextID, "")
		if bestNextID != "" {
			decision.Action = "advance"
			decision.NextState = bestNextID
			decision.Rationale = firstNonEmpty(evaluation.Selection.Rationale, "fork state routes to the best matching legal follow-up")
			return decision
		}
	}
	evaluation := selectNextJourneyNode(matchCtx, activeJourney, activeState, nextIDs)
	bestNextID := evaluation.Selection.StateID
	bestNextID = skipSatisfiedJourneyStates(matchCtx, activeJourney, activeState.ID, bestNextID, "")
	if bestNextID != "" {
		decision.Action = "advance"
		decision.NextState = bestNextID
		decision.Rationale = firstNonEmpty(evaluation.Selection.Rationale, "customer response best matches the selected journey follow-up")
		return decision
	}
	return decision
}

func buildJourneyProgressStageResult(ctx context.Context, router *model.Router, matchCtx MatchingContext, activeJourney *policy.Journey, activeState *policy.JourneyNode, instance *journey.Instance, backtrackDecision JourneyDecision) JourneyProgressStageResult {
	decision := runJourneyProgressARQ(ctx, router, matchCtx, activeJourney, activeState, instance, backtrackDecision)
	satisfactions := map[string]semantics.JourneyStateSatisfaction{}
	nextNodeEvaluations := map[string]JourneyNextNodeEvaluation{}
	selectedNextNode := JourneyNextNodeEvaluation{}
	if activeJourney != nil && activeState != nil {
		satisfactions[activeState.ID] = cachedEvaluateJourneyState(matchCtx, *activeState, "", true)
		nextIDs := journeyNextStateIDs(*activeJourney, activeState.ID)
		nextNodeEvaluations = buildJourneyNextNodeEvaluations(matchCtx, activeJourney, activeState.ID, nextIDs)
		selectedNextNode = selectNextJourneyNodeFromEvaluations(nextIDs, nextNodeEvaluations)
		if decision.NextState != "" && selectedNextNode.Selection.StateID == "" {
			selectedNextNode.Selection.StateID = decision.NextState
		}
	}
	return JourneyProgressStageResult{
		Evaluation: JourneyProgressEvaluation{
			JourneySatisfactions: satisfactions,
			NextNodeEvaluations:  nextNodeEvaluations,
			SelectedNextNode:     selectedNextNode,
		},
		Decision: decision,
	}
}

func resolveJourney(bundle policy.Bundle, instances []journey.Instance, ctx MatchingContext) (*policy.Journey, *policy.JourneyNode, *journey.Instance) {
	instanceByJourney := map[string]journey.Instance{}
	for _, item := range instances {
		if item.Status == journey.StatusActive {
			instanceByJourney[item.JourneyID] = item
		}
	}
	for _, j := range bundle.Journeys {
		if instance, ok := instanceByJourney[j.ID]; ok {
			state := findState(j, instance.StateID)
			if state != nil {
				copiedJourney := j
				copiedState := *state
				copiedInstance := instance
				return &copiedJourney, &copiedState, &copiedInstance
			}
		}
	}

	bestScore := 0
	var selected *policy.Journey
	for _, j := range bundle.Journeys {
		score := 0
		for _, cond := range j.When {
			if v := cachedEvaluateCondition(ctx, cond, ctx.LatestCustomerText).Score; v > score {
				score = v
			}
		}
		score += j.Priority
		if score > bestScore && journeyRootState(&j) != nil {
			copied := j
			selected = &copied
			bestScore = score
		}
	}
	root := journeyRootState(selected)
	if selected == nil || root == nil {
		return nil, nil, nil
	}
	startState, startPath := firstExecutableJourneyState(selected, root.ID)
	if startState == nil {
		startState = root
		startPath = []string{root.ID}
	}
	instance := journey.Instance{
		ID:        fmt.Sprintf("journey_%s", selected.ID),
		SessionID: "",
		JourneyID: selected.ID,
		StateID:   startState.ID,
		Path:      startPath,
		Status:    journey.StatusActive,
		UpdatedAt: time.Now().UTC(),
	}
	state := *startState
	return selected, &state, &instance
}
