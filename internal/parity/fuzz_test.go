package parity

import (
	"testing"

	policyruntime "github.com/sahal/parmesan/internal/runtime/policy"
)

func FuzzViolatesMustNotInclude(f *testing.F) {
	seeds := []struct {
		text   string
		needle string
		want   bool
	}{
		{text: "pineapple isn't available", needle: "pineapple", want: false},
		{text: "pineapple isn't available, but pineapple juice is", needle: "pineapple", want: true},
		{text: "we do not offer pineapple", needle: "pineapple", want: false},
		{text: "we offer pineapple", needle: "pineapple", want: true},
		{text: "", needle: "pineapple", want: false},
	}
	for _, seed := range seeds {
		f.Add(seed.text, seed.needle)
	}

	f.Fuzz(func(t *testing.T, text string, needle string) {
		got := violatesMustNotInclude(text, needle)
		if needle == "" && got {
			t.Fatalf("violatesMustNotInclude(%q, %q) = true, want false for empty needle", text, needle)
		}
		if !got {
			return
		}
		if needle == "" {
			t.Fatalf("violatesMustNotInclude(%q, %q) = true, want false for empty needle", text, needle)
		}
		if !containsFold(text, needle) {
			t.Fatalf("violatesMustNotInclude(%q, %q) = true without containing needle", text, needle)
		}
	})
}

func FuzzCanonicalizeNormalizedResult(f *testing.F) {
	f.Add("b", "a")
	f.Add("under_21", "under_21")

	f.Fuzz(func(t *testing.T, left string, right string) {
		in := NormalizedResult{
			MatchedGuidelines:    []string{left, right, left},
			SuppressedGuidelines: []string{right, left},
			ResolutionRecords: []NormalizedResolution{
				{EntityID: right, Kind: "none"},
				{EntityID: left, Kind: "deprioritized"},
			},
			ToolCandidateTandemWith: map[string][]string{
				"tool": {right, left},
			},
			OverlappingToolGroups: [][]string{
				{right, left},
			},
		}
		got := canonicalizeNormalizedResult(in)
		gotAgain := canonicalizeNormalizedResult(got)
		if len(got.MatchedGuidelines) > 1 && got.MatchedGuidelines[0] > got.MatchedGuidelines[1] {
			t.Fatalf("MatchedGuidelines not sorted: %#v", got.MatchedGuidelines)
		}
		if len(got.ResolutionRecords) > 1 {
			first := got.ResolutionRecords[0]
			second := got.ResolutionRecords[1]
			if first.EntityID > second.EntityID || (first.EntityID == second.EntityID && first.Kind > second.Kind) {
				t.Fatalf("ResolutionRecords not sorted: %#v", got.ResolutionRecords)
			}
		}
		if len(gotAgain.MatchedGuidelines) != len(got.MatchedGuidelines) {
			t.Fatalf("canonicalization changed guideline length: %#v then %#v", got, gotAgain)
		}
		for i := range got.MatchedGuidelines {
			if gotAgain.MatchedGuidelines[i] != got.MatchedGuidelines[i] {
				t.Fatalf("canonicalization is not idempotent: %#v then %#v", got, gotAgain)
			}
		}
	})
}

func FuzzNormalizeResolutionRecords(f *testing.F) {
	f.Add("under_21", "none", "under_21", "none")
	f.Add("journey:Book Flight", "deprioritized", "journey:Book Flight", "none")

	f.Fuzz(func(t *testing.T, id1, kind1, id2, kind2 string) {
		got := normalizeResolutionRecords([]policyruntime.ResolutionRecord{
			{EntityID: id1, Kind: policyruntime.ResolutionKind(kind1)},
			{EntityID: id2, Kind: policyruntime.ResolutionKind(kind2)},
			{EntityID: id1, Kind: policyruntime.ResolutionKind(kind1)},
		})
		seen := map[NormalizedResolution]struct{}{}
		for _, item := range got {
			if _, ok := seen[item]; ok {
				t.Fatalf("normalizeResolutionRecords() kept duplicate %#v in %#v", item, got)
			}
			seen[item] = struct{}{}
		}
	})
}
