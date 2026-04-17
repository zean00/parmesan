package policyruntime

import (
	"context"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/model"
	semantics "github.com/sahal/parmesan/internal/engine/semantics"
)

func buildCustomerDependencyStageResult(ctx MatchingContext, items []policy.Guideline) CustomerDependencyStageResult {
	decisions, guidelines := runCustomerDependentARQ(ctx, items)
	evidence := map[string]semantics.CustomerDependencyEvidence{}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Scope), "journey") {
			evidence[item.ID] = semantics.CustomerDependencyEvidence{
				CustomerDependent: false,
				Rationale:         "journey-scoped guidance is controlled by the active journey state, not customer-dependent filtering",
			}
			continue
		}
		evidence[item.ID] = semantics.EvaluateGuidelineCustomerDependency(
			item,
			ctx.LatestCustomerText,
			customerSatisfiedGuideline(ctx.LatestCustomerText, item),
			len(matchedSemanticTerms(ctx.LatestCustomerText, []string{"because", "reason", "damaged", "refund", "cancel", "return", "order", "item", "details", "status"})) > 0,
		)
	}
	return CustomerDependencyStageResult{
		Decisions:  decisions,
		Evidence:   evidence,
		Guidelines: guidelines,
	}
}

func buildPreviouslyAppliedStageResult(ctx MatchingContext, items []policy.Guideline, matches []Match) PreviouslyAppliedStageResult {
	decisions, guidelines := runPreviouslyAppliedARQ(ctx, items, matches)
	return PreviouslyAppliedStageResult{Decisions: decisions, Guidelines: guidelines}
}

func buildRelationshipResolutionStageResult(bundle policy.Bundle, matchCtx MatchingContext, matchedObservations []policy.Observation, guidelineMatches []Match, matchedGuidelines []policy.Guideline, activeJourney *policy.Journey, activeJourneyState *policy.JourneyNode) RelationshipResolutionStageResult {
	resolved := resolveRelationships(bundle, matchCtx, matchedObservations, guidelineMatches, matchedGuidelines, activeJourney, activeJourneyState)
	return RelationshipResolutionStageResult{
		Guidelines:           resolved.guidelines,
		SuppressedGuidelines: resolved.suppressed,
		ResolutionRecords:    resolved.resolutions,
		DisambiguationPrompt: resolved.disambiguation,
		ActiveJourney:        resolved.activeJourney,
	}
}

func buildDisambiguationStageResult(ctx context.Context, router *model.Router, bundle policy.Bundle, matchCtx MatchingContext, guidelineMatches []Match, matchedGuidelines []policy.Guideline, suppressed []SuppressedGuideline, resolutions []ResolutionRecord, existingPrompt string) DisambiguationStageResult {
	guidelines, suppressedGuidelines, resolutionRecords := applySiblingDisambiguation(bundle, matchCtx, guidelineMatches, matchedGuidelines, suppressed, resolutions)
	prompt := runDisambiguationARQ(ctx, router, matchCtx, guidelines, existingPrompt)
	return DisambiguationStageResult{
		Guidelines:           guidelines,
		SuppressedGuidelines: suppressedGuidelines,
		ResolutionRecords:    resolutionRecords,
		Prompt:               prompt,
	}
}
