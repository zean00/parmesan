package parity

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestGoldenSnapshotsExactlyMatchFixtureScenarios(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	snapshotDir := filepath.Join("testdata", "golden")

	fx, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}

	want := make([]string, 0, len(fx.Scenarios))
	for _, scenario := range fx.Scenarios {
		want = append(want, SnapshotFileName(scenario.ID))
	}
	slices.Sort(want)

	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", snapshotDir, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		got = append(got, entry.Name())
	}
	slices.Sort(got)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot file set mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParmesanGoldenSnapshots(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	snapshotDir := filepath.Join("testdata", "golden")

	fx, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}

	for _, scenario := range fx.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			ctx := context.Background()
			got, err := RunParmesanLocal(ctx, scenario)
			if err != nil {
				t.Fatalf("RunParmesan() error = %v", err)
			}
			got = canonicalizeNormalizedResult(got)
			snapshotPath := filepath.Join(snapshotDir, SnapshotFileName(scenario.ID))
			want, err := LoadGoldenSnapshot(snapshotPath)
			if err != nil {
				t.Fatalf("LoadGoldenSnapshot(%q) error = %v", snapshotPath, err)
			}
			if want.ScenarioID != scenario.ID {
				t.Fatalf("snapshot scenario_id = %q, want %q", want.ScenarioID, scenario.ID)
			}
			if !reflect.DeepEqual(got, want.Result) {
				t.Fatalf("golden mismatch for %s\n got: %#v\nwant: %#v", scenario.ID, got, want.Result)
			}
		})
	}
}

func TestParmesanGoldenCorpusIsDeterministic(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("LoadFixture() error = %v", err)
	}

	for _, scenario := range fx.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			ctx := context.Background()
			first, err := RunParmesanLocal(ctx, scenario)
			if err != nil {
				t.Fatalf("first RunParmesan() error = %v", err)
			}
			second, err := RunParmesanLocal(ctx, scenario)
			if err != nil {
				t.Fatalf("second RunParmesan() error = %v", err)
			}
			first = canonicalizeNormalizedResult(first)
			second = canonicalizeNormalizedResult(second)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("nondeterministic normalized result for %s\n first: %#v\nsecond: %#v", scenario.ID, first, second)
			}
		})
	}
}
