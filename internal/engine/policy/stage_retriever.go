package policyruntime

import (
	"context"
	"strings"
	"sync"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
)

type retrieverTask struct {
	binding policy.RetrieverBinding
	result  <-chan knowledgeretriever.Result
}

func startAgentRetrieverTasks(ctx context.Context, options ResolveOptions, bindings []policy.RetrieverBinding, matchCtx MatchingContext) []retrieverTask {
	var tasks []retrieverTask
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Scope) != "agent" {
			continue
		}
		resultCh := make(chan knowledgeretriever.Result, 1)
		tasks = append(tasks, retrieverTask{binding: binding, result: resultCh})
		go func(binding policy.RetrieverBinding) {
			item := retrieverForBinding(binding, options)
			snapshotID := ""
			if options.KnowledgeSnapshot != nil {
				snapshotID = options.KnowledgeSnapshot.ID
			}
			if item == nil {
				resultCh <- knowledgeretriever.Result{RetrieverID: binding.ID, KnowledgeSnapshotID: snapshotID, Error: "retriever not configured"}
				return
			}
			result, err := item.Retrieve(ctx, knowledgeretriever.Context{
				SessionID:          matchCtx.SessionID,
				LatestCustomerText: matchCtx.LatestCustomerText,
				ConversationText:   matchCtx.ConversationText,
				DerivedSignals:     append([]string(nil), matchCtx.DerivedSignals...),
				KnowledgeSnapshot:  knowledgeSnapshotValue(options),
			})
			if err != nil {
				result = knowledgeretriever.Result{RetrieverID: binding.ID, KnowledgeSnapshotID: snapshotID, Error: err.Error()}
			}
			if strings.TrimSpace(result.RetrieverID) == "" {
				result.RetrieverID = binding.ID
			}
			if strings.TrimSpace(result.KnowledgeSnapshotID) == "" {
				result.KnowledgeSnapshotID = snapshotID
			}
			resultCh <- result
		}(binding)
	}
	return tasks
}

func buildRetrieverStageResult(ctx context.Context, options ResolveOptions, state *matchingState, agentTasks []retrieverTask) RetrieverStageResult {
	if state == nil || len(state.bundle.Retrievers) == 0 {
		return RetrieverStageResult{}
	}
	snapshotID := ""
	if options.KnowledgeSnapshot != nil {
		snapshotID = options.KnowledgeSnapshot.ID
	}
	active := activeRetrieverBindings(state.bundle.Retrievers, state)
	if len(active) == 0 {
		return RetrieverStageResult{
			KnowledgeSnapshotID: snapshotID,
			Outcome:             RetrievalOutcome{State: "not_attempted"},
		}
	}

	results := make([]knowledgeretriever.Result, len(active))
	var wg sync.WaitGroup
	for i, binding := range active {
		if task, ok := agentTaskForBinding(agentTasks, binding.ID); ok {
			results[i] = <-task.result
			continue
		}
		wg.Add(1)
		go func(i int, binding policy.RetrieverBinding) {
			defer wg.Done()
			item := retrieverForBinding(binding, options)
			if item == nil {
				results[i] = knowledgeretriever.Result{
					RetrieverID:         binding.ID,
					KnowledgeSnapshotID: snapshotID,
					Error:               "retriever not configured",
				}
				return
			}
			result, err := item.Retrieve(ctx, knowledgeretriever.Context{
				SessionID:           state.context.SessionID,
				LatestCustomerText:  state.context.LatestCustomerText,
				ConversationText:    state.context.ConversationText,
				MatchedGuidelineIDs: idsFromGuidelinesRuntime(state.matchFinalizeStage.MatchedGuidelines),
				ActiveJourneyID:     journeyIDRuntime(state.activeJourney),
				ActiveStateID:       journeyStateIDRuntime(state.activeJourneyState),
				DerivedSignals:      append([]string(nil), state.context.DerivedSignals...),
				KnowledgeSnapshot:   knowledgeSnapshotValue(options),
			})
			if err != nil {
				result = knowledgeretriever.Result{
					RetrieverID:         binding.ID,
					KnowledgeSnapshotID: snapshotID,
					Error:               err.Error(),
				}
			}
			if strings.TrimSpace(result.RetrieverID) == "" {
				result.RetrieverID = binding.ID
			}
			if strings.TrimSpace(result.KnowledgeSnapshotID) == "" {
				result.KnowledgeSnapshotID = snapshotID
			}
			results[i] = result
		}(i, binding)
	}
	wg.Wait()
	return RetrieverStageResult{
		Results:             results,
		KnowledgeSnapshotID: snapshotID,
		TransientGuidelines: transientGuidelinesFromRetrieverResults(results),
		Outcome:             retrievalOutcomeFromResults(results),
	}
}

func retrievalOutcomeFromResults(results []knowledgeretriever.Result) RetrievalOutcome {
	outcome := RetrievalOutcome{
		Attempted:         len(results) > 0,
		GroundingRequired: len(results) > 0,
		State:             "not_attempted",
	}
	if len(results) == 0 {
		return outcome
	}
	outcome.State = "no_results"
	for _, item := range results {
		if strings.TrimSpace(item.Data) != "" || len(item.Citations) > 0 {
			outcome.HasUsableEvidence = true
			outcome.State = "evidence_available"
			return outcome
		}
		if len(item.TransientGuidelines) > 0 {
			outcome.HasUsableEvidence = true
			outcome.GroundingRequired = false
			outcome.State = "guidance_available"
			return outcome
		}
	}
	for _, item := range results {
		if strings.TrimSpace(item.Error) != "" {
			outcome.State = "insufficient"
			return outcome
		}
	}
	return outcome
}

func transientGuidelinesFromRetrieverResults(results []knowledgeretriever.Result) []policy.Guideline {
	var out []policy.Guideline
	for _, result := range results {
		for _, guideline := range result.TransientGuidelines {
			if strings.TrimSpace(guideline.ID) == "" {
				continue
			}
			prefix := "retriever:" + result.RetrieverID + ":"
			if !strings.HasPrefix(guideline.ID, prefix) {
				guideline.ID = prefix + guideline.ID
			}
			out = append(out, guideline)
		}
	}
	return out
}

func agentTaskForBinding(tasks []retrieverTask, id string) (retrieverTask, bool) {
	for _, task := range tasks {
		if task.binding.ID == id {
			return task, true
		}
	}
	return retrieverTask{}, false
}

func activeRetrieverBindings(bindings []policy.RetrieverBinding, state *matchingState) []policy.RetrieverBinding {
	var out []policy.RetrieverBinding
	guidelines := map[string]struct{}{}
	for _, guideline := range state.matchFinalizeStage.MatchedGuidelines {
		guidelines[strings.TrimSpace(guideline.ID)] = struct{}{}
	}
	activeJourney := journeyIDRuntime(state.activeJourney)
	activeState := journeyStateIDRuntime(state.activeJourneyState)
	for _, binding := range bindings {
		scope := strings.TrimSpace(binding.Scope)
		switch scope {
		case "agent":
			out = append(out, binding)
		case "guideline":
			if _, ok := guidelines[strings.TrimSpace(binding.TargetID)]; ok {
				out = append(out, binding)
			}
		case "journey":
			if strings.TrimSpace(binding.TargetID) == activeJourney {
				out = append(out, binding)
			}
		case "journey_state":
			if strings.TrimSpace(binding.TargetID) == activeState || strings.TrimSpace(binding.TargetID) == activeJourney+":"+activeState {
				out = append(out, binding)
			}
		}
	}
	return out
}

func retrieverForBinding(binding policy.RetrieverBinding, options ResolveOptions) knowledgeretriever.Interface {
	if options.RetrieverRegistry != nil {
		if item := options.RetrieverRegistry.GetRetriever(binding.ID); item != nil {
			return item
		}
	}
	if binding.Kind == "knowledge" && options.KnowledgeSnapshot != nil {
		return knowledgeretriever.Hybrid{
			ID:         binding.ID,
			Snapshot:   *options.KnowledgeSnapshot,
			Chunks:     options.KnowledgeChunks,
			MaxResults: binding.MaxResults,
			Embedder:   options.Router,
			Searcher:   options.KnowledgeSearcher,
		}
	}
	return nil
}

func knowledgeSnapshotValue(options ResolveOptions) knowledge.Snapshot {
	if options.KnowledgeSnapshot == nil {
		return knowledge.Snapshot{}
	}
	return *options.KnowledgeSnapshot
}

func idsFromGuidelinesRuntime(items []policy.Guideline) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ID) != "" {
			out = append(out, item.ID)
		}
	}
	return out
}

func journeyIDRuntime(item *policy.Journey) string {
	if item == nil {
		return ""
	}
	return item.ID
}

func journeyStateIDRuntime(item *policy.JourneyNode) string {
	if item == nil {
		return ""
	}
	return item.ID
}
