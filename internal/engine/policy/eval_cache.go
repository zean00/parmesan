package policyruntime

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
	semantics "github.com/sahal/parmesan/internal/engine/semantics"
)

type matchingEvalCache struct {
	condition      map[string]semantics.ConditionEvidence
	conditionMulti map[string]semantics.ConditionEvidence
	journeyState   map[string]semantics.JourneyStateSatisfaction
}

func newMatchingEvalCache() *matchingEvalCache {
	return &matchingEvalCache{
		condition:      map[string]semantics.ConditionEvidence{},
		conditionMulti: map[string]semantics.ConditionEvidence{},
		journeyState:   map[string]semantics.JourneyStateSatisfaction{},
	}
}

func cachedEvaluateCondition(ctx MatchingContext, condition string, text string) semantics.ConditionEvidence {
	if ctx.cache == nil {
		return semantics.EvaluateCondition(condition, text)
	}
	key := strings.TrimSpace(condition) + "\x00" + strings.TrimSpace(text)
	if evidence, ok := ctx.cache.condition[key]; ok {
		return evidence
	}
	evidence := semantics.EvaluateCondition(condition, text)
	ctx.cache.condition[key] = evidence
	return evidence
}

func cachedEvaluateConditionAcrossTexts(ctx MatchingContext, condition string, texts ...string) semantics.ConditionEvidence {
	if ctx.cache == nil {
		return semantics.EvaluateConditionAcrossTexts(condition, texts...)
	}
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(condition))
	for _, text := range texts {
		builder.WriteString("\x00")
		builder.WriteString(strings.TrimSpace(text))
	}
	key := builder.String()
	if evidence, ok := ctx.cache.conditionMulti[key]; ok {
		return evidence
	}
	evidence := semantics.EvaluateConditionAcrossTexts(condition, texts...)
	ctx.cache.conditionMulti[key] = evidence
	return evidence
}

func cachedEvaluateJourneyState(ctx MatchingContext, state policy.JourneyNode, edgeCondition string, latestOnly bool) semantics.JourneyStateSatisfaction {
	if ctx.cache == nil {
		return semantics.EvaluateJourneyState(ctx.LatestCustomerText, ctx.CustomerHistory, state, edgeCondition, latestOnly, customerSatisfiedGuideline)
	}
	key := strings.TrimSpace(state.ID) + "\x00" + strings.TrimSpace(edgeCondition) + "\x00" + boolKey(latestOnly)
	if satisfaction, ok := ctx.cache.journeyState[key]; ok {
		return satisfaction
	}
	satisfaction := semantics.EvaluateJourneyState(ctx.LatestCustomerText, ctx.CustomerHistory, state, edgeCondition, latestOnly, customerSatisfiedGuideline)
	ctx.cache.journeyState[key] = satisfaction
	return satisfaction
}

func boolKey(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
