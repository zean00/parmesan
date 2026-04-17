package policyruntime

import (
	"context"
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/model"
	semantics "github.com/sahal/parmesan/internal/engine/semantics"
)

func buildResponseAnalysisStageResult(ctx context.Context, router *model.Router, matchCtx MatchingContext, bundle policy.Bundle, matchedGuidelines []policy.Guideline, templates []policy.Template, existingCoverage map[string]semantics.ActionCoverageEvidence) ResponseAnalysisStageResult {
	mode := modeOrDefault(bundle.CompositionMode, templates)
	analysisGuidelines := responseAnalysisGuidelines(bundle, matchCtx, matchedGuidelines)
	analysis := analyzeResponsePlan(ctx, router, matchCtx, analysisGuidelines, templates, mode, bundle.NoMatch)
	coverage := cloneActionCoverage(existingCoverage)
	if coverage == nil {
		coverage = buildResponseCoverage(matchCtx, analysisGuidelines)
	}
	return ResponseAnalysisStageResult{
		CandidateTemplates: append([]policy.Template(nil), templates...),
		Analysis:           analysis,
		Evaluation: ResponseAnalysisEvaluation{
			Coverage:            coverage,
			AnalyzedGuidelines:  append([]AnalyzedGuideline(nil), analysis.AnalyzedGuidelines...),
			NeedsRevision:       analysis.NeedsRevision,
			NeedsStrictMode:     analysis.NeedsStrictMode,
			RecommendedTemplate: analysis.RecommendedTemplate,
			Rationale:           analysis.Rationale,
		},
	}
}

func buildResponseCoverage(matchCtx MatchingContext, guidelines []policy.Guideline) map[string]semantics.ActionCoverageEvidence {
	coverage := map[string]semantics.ActionCoverageEvidence{}
	history := strings.ToLower(strings.Join(matchCtx.AssistantHistory, "\n"))
	for _, guideline := range guidelines {
		coverage[guideline.ID] = semantics.EvaluateActionCoverage(history, guideline.Then, toolHistorySatisfiesInstruction, containsEquivalentInstruction, splitActionSegments, segmentSatisfiedByHistory, dedupe)
	}
	return coverage
}
