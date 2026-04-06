package parity

import (
	"context"
	"path/filepath"
	"testing"
)

func BenchmarkRunParmesanFullGoldenCorpus(b *testing.B) {
	fixturePath := filepath.Join("..", "..", "examples", "golden_scenarios.yaml")
	fx, err := LoadFixture(fixturePath)
	if err != nil {
		b.Fatalf("LoadFixture() error = %v", err)
	}
	if len(fx.Scenarios) == 0 {
		b.Fatalf("fixture has no scenarios")
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, scenario := range fx.Scenarios {
			if _, err := RunParmesan(ctx, scenario); err != nil {
				b.Fatalf("RunParmesan(%q) error = %v", scenario.ID, err)
			}
		}
	}
}
