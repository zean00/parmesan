package policyruntime

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestArchitectureRemovesLegacyViewAndHelperNames(t *testing.T) {
	root := repoRoot(t)
	targets := []string{
		filepath.Join(root, "internal"),
	}
	legacyPreferredHelper := regexp.MustCompile(`\bPreferred[A-Z][A-Za-z0-9_]*\s*\(`)

	for _, dir := range targets {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(raw)
			if strings.Contains(text, "ResolvedView") {
				t.Fatalf("%s still references ResolvedView", path)
			}
			if legacyPreferredHelper.FindStringIndex(text) != nil {
				t.Fatalf("%s still references legacy Preferred helper naming", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WalkDir(%q) error = %v", dir, err)
		}
	}
}

func TestArchitectureKeepsStageBuildersOutOfRuntimeFile(t *testing.T) {
	root := repoRoot(t)
	runtimePath := filepath.Join(root, "internal", "engine", "policy", "runtime.go")
	raw, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", runtimePath, err)
	}
	text := string(raw)
	disallowed := []string{
		"func buildToolStageResults(",
		"func buildResponseAnalysisStageResult(",
		"func buildResponseCoverage(",
		"func buildCustomerDependencyStageResult(",
		"func buildPreviouslyAppliedStageResult(",
		"func buildRelationshipResolutionStageResult(",
		"func buildDisambiguationStageResult(",
		"func buildJourneyBacktrackStageResult(",
		"func buildJourneyProgressStageResult(",
	}
	for _, item := range disallowed {
		if strings.Contains(text, item) {
			t.Fatalf("runtime.go still contains stage builder %q", item)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
