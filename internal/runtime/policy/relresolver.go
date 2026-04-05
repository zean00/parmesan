package policyruntime

import (
	"strings"

	"github.com/sahal/parmesan/internal/domain/policy"
)

type relationalResolverResult struct {
	guidelines     []policy.Guideline
	suppressed     []SuppressedGuideline
	resolutions    []ResolutionRecord
	disambiguation string
	activeJourney  *policy.Journey
}

func resolveRelationships(bundle policy.Bundle, matchCtx MatchingContext, observations []policy.Observation, matches []Match, guidelines []policy.Guideline, activeJourney *policy.Journey, activeJourneyState *policy.JourneyNode) relationalResolverResult {
	activeObs := map[string]bool{}
	for _, item := range observations {
		activeObs[item.ID] = true
	}
	matchByID := map[string]Match{}
	for _, item := range matches {
		matchByID[item.ID] = item
	}
	active := map[string]policy.Guideline{}
	for _, item := range guidelines {
		active[item.ID] = item
	}
	guidelineIndex := map[string]policy.Guideline{}
	for _, item := range append(bundle.Guidelines, collectJourneyGuidelines(bundle)...) {
		guidelineIndex[item.ID] = item
	}
	if activeJourney != nil {
		for _, item := range activeJourney.Guidelines {
			guidelineIndex[item.ID] = item
		}
	}

	suppressed := []SuppressedGuideline{}
	suppressedIndex := map[string]int{}
	resolutions := map[string][]ResolutionRecord{}
	disambiguation := ""
	journeyActive := activeJourney != nil
	journeyEntityID := ""
	if activeJourney != nil {
		journeyEntityID = "journey:" + activeJourney.ID
	}

	for iteration := 0; iteration < 4; iteration++ {
		changed := false

		for sourceID, deps := range dependencyRelationsBySource(bundle.Relationships) {
			if !relationshipEntityActive(sourceID, active, activeJourney, journeyActive) {
				continue
			}
			var unmetAll []string
			for _, rel := range deps.all {
				if dependencySatisfied(rel.Target, active, activeObs, guidelineIndex, activeJourney, journeyActive) {
					continue
				}
				unmetAll = append(unmetAll, rel.Target)
			}
			if len(unmetAll) > 0 {
				if journeyEntityID != "" && sourceID == journeyEntityID {
					journeyActive = false
					suppressJourneyGuidelines(active, activeJourney, activeJourneyState, &suppressed, suppressedIndex, resolutions, ResolutionUnmetDependency, "dependency_unmet", "active journey was removed because a dependency target is not active", unmetAll...)
				} else {
					delete(active, sourceID)
					recordSuppressed(&suppressed, suppressedIndex, sourceID, "dependency_unmet", unmetAll...)
				}
				appendResolution(resolutions, sourceID, ResolutionUnmetDependency, "dependency target is not active", unmetAll...)
				changed = true
				continue
			}
			if len(deps.any) == 0 {
				continue
			}
			anySatisfied := false
			targets := make([]string, 0, len(deps.any))
			for _, rel := range deps.any {
				targets = append(targets, rel.Target)
				if dependencySatisfied(rel.Target, active, activeObs, guidelineIndex, activeJourney, journeyActive) {
					anySatisfied = true
				}
			}
			if anySatisfied {
				continue
			}
			if journeyEntityID != "" && sourceID == journeyEntityID {
				journeyActive = false
				suppressJourneyGuidelines(active, activeJourney, activeJourneyState, &suppressed, suppressedIndex, resolutions, ResolutionUnmetDependencyAny, "dependency_any_unmet", "active journey was removed because no dependency_any target is active", targets...)
			} else {
				delete(active, sourceID)
				recordSuppressed(&suppressed, suppressedIndex, sourceID, "dependency_any_unmet", targets...)
			}
			appendResolution(resolutions, sourceID, ResolutionUnmetDependencyAny, "no dependency_any target is active", targets...)
			changed = true
		}

		for _, rel := range bundle.Relationships {
			kind := strings.ToLower(strings.TrimSpace(rel.Kind))
			if kind != "priority" && kind != "overrides" {
				continue
			}
			sourceTargets := activePrioritySources(bundle.Relationships, rel.Source, active, activeJourney, journeyActive)
			targetTargets := matchingRelationshipTargets(rel.Target, active, activeJourney, journeyActive)
			if len(sourceTargets) == 0 || len(targetTargets) == 0 {
				continue
			}
			for _, loserID := range targetTargets {
				if !relationshipEntityActive(loserID, active, activeJourney, journeyActive) {
					continue
				}
				for _, winnerID := range sourceTargets {
					if !relationshipEntityActive(winnerID, active, activeJourney, journeyActive) || winnerID == loserID {
						continue
					}
					if journeyEntityID != "" && loserID == journeyEntityID {
						journeyActive = false
						suppressJourneyGuidelines(active, activeJourney, activeJourneyState, &suppressed, suppressedIndex, resolutions, ResolutionDeprioritized, "deprioritized", "active journey node lost to a higher-priority related entity", winnerID)
					} else {
						delete(active, loserID)
						recordSuppressed(&suppressed, suppressedIndex, loserID, "deprioritized", winnerID)
					}
					appendResolution(resolutions, loserID, ResolutionDeprioritized, "a higher-priority related guideline won", winnerID)
					changed = true
					break
				}
			}
		}

		if journeyActive && activeJourney != nil {
			priorityCeiling, hasCeiling := activeGuidelinePriorityCeiling(active, resolutions)
			if hasCeiling && activeJourney.Priority < priorityCeiling {
				journeyActive = false
				suppressJourneyGuidelines(active, activeJourney, activeJourneyState, &suppressed, suppressedIndex, nil, ResolutionDeprioritized, "deprioritized", "active journey lost to a higher numerical priority entity")
				appendResolution(resolutions, journeyEntityID, ResolutionDeprioritized, "journey lost to a higher numerical priority entity")
				changed = true
			}
		}
		if journeyActive && activeJourney != nil {
			for candidateID, candidate := range active {
				if entityHasResolutionKind(resolutions, candidateID, ResolutionEntailed) {
					continue
				}
				if candidate.Priority >= activeJourney.Priority {
					continue
				}
				if !shareConditionTopic(candidate.When, strings.Join(activeJourney.When, " ")) && !strings.HasPrefix(candidateID, "journey_node:"+activeJourney.ID+":") {
					continue
				}
				delete(active, candidateID)
				recordSuppressed(&suppressed, suppressedIndex, candidateID, "deprioritized", journeyEntityID)
				appendResolution(resolutions, candidateID, ResolutionDeprioritized, "guideline lost to a higher numerical priority journey", journeyEntityID)
				changed = true
			}
		}

		for _, rel := range bundle.Relationships {
			kind := strings.ToLower(strings.TrimSpace(rel.Kind))
			if kind != "entails" && kind != "entailment" {
				continue
			}
			sourceTargets := matchingRelationshipTargets(rel.Source, active, activeJourney, journeyActive)
			if len(sourceTargets) == 0 {
				continue
			}
			for _, entailedID := range resolveEntailmentTargets(rel.Target, guidelineIndex) {
				if _, ok := active[entailedID]; ok {
					continue
				}
				target, ok := guidelineIndex[entailedID]
				if !ok {
					continue
				}
				active[entailedID] = target
				if _, ok := matchByID[entailedID]; !ok {
					matchByID[entailedID] = Match{ID: entailedID, Kind: "guideline", Score: 0.5, Rationale: "activated by entailment"}
				}
				appendResolution(resolutions, entailedID, ResolutionEntailed, "guideline was activated by entailment", rel.Source)
				changed = true
			}
		}

		if !changed {
			break
		}
	}

	for _, rel := range bundle.Relationships {
		kind := strings.ToLower(strings.TrimSpace(rel.Kind))
		if kind != "disambiguation" && kind != "disambiguates" {
			continue
		}
		if len(matchingRelationshipTargets(rel.Source, active, activeJourney, journeyActive)) > 0 && len(matchingRelationshipTargets(rel.Target, active, activeJourney, journeyActive)) > 0 {
			disambiguation = "Could you clarify which option you mean?"
			break
		}
	}

	out := make([]policy.Guideline, 0, len(active))
	for _, item := range active {
		out = append(out, item)
		appendResolution(resolutions, item.ID, ResolutionNone, "guideline remained active")
	}
	if journeyEntityID != "" {
		if journeyActive {
			recordInactivePrioritySourceJourneyNodes(bundle, active, activeJourney, journeyActive, resolutions)
			appendResolution(resolutions, journeyEntityID, ResolutionNone, "journey remained active")
		}
		if !journeyActive {
			activeJourney = nil
		}
	}
	allMatches := make([]Match, 0, len(matchByID))
	for _, item := range matchByID {
		allMatches = append(allMatches, item)
	}
	sortMatches(allMatches)
	sortGuidelines(out, allMatches)
	return relationalResolverResult{
		guidelines:     out,
		suppressed:     suppressed,
		resolutions:    flattenResolutions(resolutions),
		disambiguation: disambiguation,
		activeJourney:  activeJourney,
	}
}

func recordInactivePrioritySourceJourneyNodes(bundle policy.Bundle, active map[string]policy.Guideline, activeJourney *policy.Journey, journeyActive bool, resolutions map[string][]ResolutionRecord) {
	if activeJourney == nil || !journeyActive {
		return
	}
	activeJourneyEntityID := "journey:" + activeJourney.ID
	for _, rel := range bundle.Relationships {
		kind := strings.ToLower(strings.TrimSpace(rel.Kind))
		if kind != "priority" && kind != "overrides" {
			continue
		}
		sourceID := strings.TrimSpace(rel.Source)
		targetID := strings.TrimSpace(rel.Target)
		if targetID != activeJourneyEntityID || !strings.HasPrefix(sourceID, "journey:") {
			continue
		}
		if relationshipEntityActive(sourceID, active, activeJourney, journeyActive) {
			continue
		}
		sourceJourneyID := strings.TrimSpace(strings.TrimPrefix(sourceID, "journey:"))
		if sourceJourneyID == "" || sourceJourneyID == activeJourney.ID {
			continue
		}
		sourceJourney := findJourneyByID(bundle, sourceJourneyID)
		if sourceJourney == nil {
			continue
		}
		rootState := journeyRootState(sourceJourney)
		if rootState == nil {
			continue
		}
		journeyNodeID := projectedNodeGuideline(*sourceJourney, *rootState).ID
		if relationshipEntityActive(journeyNodeID, active, activeJourney, journeyActive) || entityHasResolutionKind(resolutions, journeyNodeID, ResolutionDeprioritized) {
			continue
		}
		appendResolution(resolutions, journeyNodeID, ResolutionDeprioritized, "inactive competing journey node lost to the active priority target journey", activeJourneyEntityID)
	}
}

func findJourneyByID(bundle policy.Bundle, journeyID string) *policy.Journey {
	for i := range bundle.Journeys {
		if strings.TrimSpace(bundle.Journeys[i].ID) == strings.TrimSpace(journeyID) {
			return &bundle.Journeys[i]
		}
	}
	return nil
}

func activePrioritySources(rels []policy.Relationship, source string, active map[string]policy.Guideline, activeJourney *policy.Journey, journeyActive bool) []string {
	direct := matchingRelationshipTargets(source, active, activeJourney, journeyActive)
	if len(direct) > 0 {
		return direct
	}
	seen := map[string]struct{}{}
	var search func(string) []string
	search = func(target string) []string {
		if _, ok := seen[target]; ok {
			return nil
		}
		seen[target] = struct{}{}
		var out []string
		for _, rel := range rels {
			kind := strings.ToLower(strings.TrimSpace(rel.Kind))
			if kind != "priority" && kind != "overrides" {
				continue
			}
			if strings.TrimSpace(rel.Target) != strings.TrimSpace(target) {
				continue
			}
			if direct := matchingRelationshipTargets(rel.Source, active, activeJourney, journeyActive); len(direct) > 0 {
				out = append(out, direct...)
				continue
			}
			out = append(out, search(rel.Source)...)
		}
		return dedupe(out)
	}
	return search(source)
}

func dependencySatisfied(target string, active map[string]policy.Guideline, activeObs map[string]bool, guidelineIndex map[string]policy.Guideline, activeJourney *policy.Journey, journeyActive bool) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if journeyActive && matchesJourneyDependencyTarget(target, activeJourney) {
		return len(activeJourneyTargets(active, activeJourney)) > 0
	}
	if strings.HasPrefix(target, "tag:") || strings.HasPrefix(target, "tag_any:") || strings.HasPrefix(target, "tag_all:") {
		return tagDependencySatisfied(target, active, guidelineIndex)
	}
	if _, ok := active[target]; ok {
		return true
	}
	if _, ok := activeObs[target]; ok {
		return true
	}
	_, exists := guidelineIndex[target]
	return !exists && activeObs[target]
}

func activeJourneyTargets(active map[string]policy.Guideline, activeJourney *policy.Journey) []string {
	if activeJourney == nil {
		return nil
	}
	var out []string
	prefix := "journey_node:" + activeJourney.ID + ":"
	for _, item := range active {
		if strings.HasPrefix(item.ID, prefix) {
			out = append(out, item.ID)
		}
	}
	return dedupe(out)
}

type dependencyRelationGroups struct {
	all []policy.Relationship
	any []policy.Relationship
}

func dependencyRelationsBySource(rels []policy.Relationship) map[string]dependencyRelationGroups {
	out := map[string]dependencyRelationGroups{}
	for _, rel := range rels {
		kind := normalizedDependencyKind(rel.Kind)
		if kind == "" {
			continue
		}
		groups := out[rel.Source]
		switch kind {
		case "dependency":
			groups.all = append(groups.all, rel)
		case "dependency_any":
			groups.any = append(groups.any, rel)
		}
		out[rel.Source] = groups
	}
	return out
}

func normalizedDependencyKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dependency", "depend_on":
		return "dependency"
	case "dependency_any", "depend_on_any":
		return "dependency_any"
	default:
		return ""
	}
}

func matchingRelationshipTargets(target string, active map[string]policy.Guideline, activeJourney *policy.Journey, journeyActive bool) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if strings.HasPrefix(target, "journey:") {
		if journeyActive && matchesJourneyDependencyTarget(target, activeJourney) {
			return []string{target}
		}
		return nil
	}
	if strings.HasPrefix(target, "tag:") || strings.HasPrefix(target, "tag_any:") || strings.HasPrefix(target, "tag_all:") {
		tag := normalizedTagTarget(target)
		var out []string
		for _, item := range active {
			for _, candidate := range item.Tags {
				if candidate == tag {
					out = append(out, item.ID)
					break
				}
			}
		}
		return dedupe(out)
	}
	if _, ok := active[target]; ok {
		return []string{target}
	}
	return nil
}

func relationshipEntityActive(entityID string, active map[string]policy.Guideline, activeJourney *policy.Journey, journeyActive bool) bool {
	if entityID == "" {
		return false
	}
	if strings.HasPrefix(entityID, "journey:") {
		return journeyActive && matchesJourneyDependencyTarget(entityID, activeJourney)
	}
	_, ok := active[entityID]
	return ok
}

func suppressJourneyGuidelines(active map[string]policy.Guideline, activeJourney *policy.Journey, activeJourneyState *policy.JourneyNode, suppressed *[]SuppressedGuideline, suppressedIndex map[string]int, resolutions map[string][]ResolutionRecord, resolutionKind ResolutionKind, reason string, description string, relatedIDs ...string) {
	if activeJourney == nil || activeJourneyState == nil {
		return
	}
	journeyNodeID := projectedNodeGuideline(*activeJourney, *activeJourneyState).ID
	delete(active, journeyNodeID)
	recordSuppressed(suppressed, suppressedIndex, journeyNodeID, reason, relatedIDs...)
	if resolutions != nil {
		appendResolution(resolutions, journeyNodeID, resolutionKind, description, relatedIDs...)
	}
}

func resolveEntailmentTargets(target string, guidelineIndex map[string]policy.Guideline) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if strings.HasPrefix(target, "tag:") || strings.HasPrefix(target, "tag_any:") || strings.HasPrefix(target, "tag_all:") {
		tag := normalizedTagTarget(target)
		var out []string
		for _, item := range guidelineIndex {
			for _, candidate := range item.Tags {
				if candidate == tag {
					out = append(out, item.ID)
					break
				}
			}
		}
		return dedupe(out)
	}
	if _, ok := guidelineIndex[target]; ok {
		return []string{target}
	}
	return nil
}

func appendResolution(store map[string][]ResolutionRecord, entityID string, kind ResolutionKind, description string, targetIDs ...string) {
	store[entityID] = append(store[entityID], ResolutionRecord{
		EntityID: entityID,
		Kind:     kind,
		Details: ResolutionDetails{
			Description: description,
			TargetIDs:   dedupe(append([]string(nil), targetIDs...)),
		},
	})
}

func entityHasResolutionKind(store map[string][]ResolutionRecord, entityID string, kind ResolutionKind) bool {
	for _, item := range store[entityID] {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func activeGuidelinePriorityCeiling(active map[string]policy.Guideline, resolutions map[string][]ResolutionRecord) (int, bool) {
	has := false
	best := 0
	for id, item := range active {
		if entityHasResolutionKind(resolutions, id, ResolutionEntailed) {
			continue
		}
		if !has || item.Priority > best {
			best = item.Priority
			has = true
		}
	}
	return best, has
}

func flattenResolutions(store map[string][]ResolutionRecord) []ResolutionRecord {
	var out []ResolutionRecord
	for entityID, items := range store {
		if len(items) == 0 {
			out = append(out, ResolutionRecord{
				EntityID: entityID,
				Kind:     ResolutionNone,
				Details:  ResolutionDetails{Description: "entity remained active after relational resolution"},
			})
			continue
		}
		out = append(out, items...)
	}
	return out
}

func normalizedTagTarget(target string) string {
	switch {
	case strings.HasPrefix(target, "tag_any:"):
		return strings.TrimPrefix(target, "tag_any:")
	case strings.HasPrefix(target, "tag_all:"):
		return strings.TrimPrefix(target, "tag_all:")
	default:
		return strings.TrimPrefix(target, "tag:")
	}
}

func tagDependencySatisfied(target string, active map[string]policy.Guideline, guidelineIndex map[string]policy.Guideline) bool {
	tag := normalizedTagTarget(target)
	if tag == "" {
		return false
	}
	requiredAll := strings.HasPrefix(target, "tag_all:")
	activeTagged := 0
	totalTagged := 0
	for _, item := range guidelineIndex {
		hasTag := false
		for _, candidate := range item.Tags {
			if candidate == tag {
				hasTag = true
				break
			}
		}
		if !hasTag {
			continue
		}
		totalTagged++
		if _, ok := active[item.ID]; ok {
			activeTagged++
		}
	}
	if requiredAll {
		return totalTagged > 0 && activeTagged == totalTagged
	}
	return activeTagged > 0
}
