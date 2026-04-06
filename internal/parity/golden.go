package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type GoldenSnapshot struct {
	ScenarioID string           `json:"scenario_id"`
	Result     NormalizedResult `json:"result"`
}

func SnapshotFileName(id string) string {
	return id + ".json"
}

func LoadGoldenSnapshot(path string) (GoldenSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return GoldenSnapshot{}, err
	}
	var out GoldenSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return GoldenSnapshot{}, err
	}
	if out.ScenarioID == "" {
		return GoldenSnapshot{}, fmt.Errorf("snapshot %q is missing scenario_id", path)
	}
	out.Result = canonicalizeNormalizedResult(out.Result)
	return out, nil
}

func WriteGoldenSnapshot(path string, snapshot GoldenSnapshot) error {
	snapshot.Result = canonicalizeNormalizedResult(snapshot.Result)
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func RunGoldenScenario(ctx context.Context, fx Fixture, scenarioID string) (GoldenSnapshot, error) {
	for _, item := range fx.Scenarios {
		if item.ID != scenarioID {
			continue
		}
		result, err := RunParmesanLocal(ctx, item)
		if err != nil {
			return GoldenSnapshot{}, err
		}
		return GoldenSnapshot{
			ScenarioID: item.ID,
			Result:     canonicalizeNormalizedResult(result),
		}, nil
	}
	return GoldenSnapshot{}, fmt.Errorf("unknown scenario %q", scenarioID)
}

func RunGoldenCorpus(ctx context.Context, fx Fixture, scenarioID string) ([]GoldenSnapshot, error) {
	out := make([]GoldenSnapshot, 0, len(fx.Scenarios))
	for _, item := range fx.Scenarios {
		if scenarioID != "" && item.ID != scenarioID {
			continue
		}
		result, err := RunParmesanLocal(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.ID, err)
		}
		out = append(out, GoldenSnapshot{
			ScenarioID: item.ID,
			Result:     canonicalizeNormalizedResult(result),
		})
	}
	return out, nil
}

func canonicalizeNormalizedResult(in NormalizedResult) NormalizedResult {
	in.MatchedObservations = normalizeStringSlice(in.MatchedObservations)
	in.MatchedGuidelines = normalizeStringSlice(in.MatchedGuidelines)
	in.SuppressedGuidelines = normalizeStringSlice(in.SuppressedGuidelines)
	in.SuppressionReasons = normalizeStringSlice(in.SuppressionReasons)
	in.ResolutionRecords = normalizeResolutionSlice(in.ResolutionRecords)
	in.ProjectedFollowUps = normalizeStringMapSlice(in.ProjectedFollowUps)
	in.LegalFollowUps = normalizeStringMapSlice(in.LegalFollowUps)
	in.ExposedTools = normalizeStringSlice(in.ExposedTools)
	in.ToolCandidates = normalizeStringSlice(in.ToolCandidates)
	in.ToolCandidateTandemWith = normalizeStringMapSlice(in.ToolCandidateTandemWith)
	in.OverlappingToolGroups = normalizeGroups(in.OverlappingToolGroups)
	in.SelectedTools = normalizeStringSlice(in.SelectedTools)
	in.ToolCallTools = normalizeStringSlice(in.ToolCallTools)
	in.ResponseAnalysisStillRequired = normalizeStringSlice(in.ResponseAnalysisStillRequired)
	in.ResponseAnalysisAlreadySatisfied = normalizeStringSlice(in.ResponseAnalysisAlreadySatisfied)
	in.ResponseAnalysisPartiallyApplied = normalizeStringSlice(in.ResponseAnalysisPartiallyApplied)
	in.ResponseAnalysisToolSatisfied = normalizeStringSlice(in.ResponseAnalysisToolSatisfied)
	in.ToolCalls = normalizeToolCalls(in.ToolCalls)
	in.UnsupportedFields = normalizeStringSlice(in.UnsupportedFields)
	return in
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	slices.Sort(out)
	return out
}

func normalizeResolutionSlice(in []NormalizedResolution) []NormalizedResolution {
	if len(in) == 0 {
		return nil
	}
	out := append([]NormalizedResolution(nil), in...)
	slices.SortFunc(out, func(a, b NormalizedResolution) int {
		if cmp := strings.Compare(a.EntityID, b.EntityID); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return out
}

func normalizeStringMapSlice(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = normalizeStringSlice(values)
	}
	return out
}

func normalizeGroups(in [][]string) [][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([][]string, 0, len(in))
	for _, group := range in {
		out = append(out, normalizeStringSlice(group))
	}
	slices.SortFunc(out, func(a, b []string) int {
		return strings.Compare(strings.Join(a, "\x00"), strings.Join(b, "\x00"))
	})
	return out
}

func normalizeToolCalls(in []ToolCall) []ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := append([]ToolCall(nil), in...)
	slices.SortFunc(out, func(a, b ToolCall) int {
		if cmp := strings.Compare(a.ToolID, b.ToolID); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.DocumentID, b.DocumentID); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.ModulePath, b.ModulePath); cmp != 0 {
			return cmp
		}
		left, _ := json.Marshal(a.Arguments)
		right, _ := json.Marshal(b.Arguments)
		return strings.Compare(string(left), string(right))
	})
	return out
}
