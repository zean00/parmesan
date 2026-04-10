package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPreservesZeroACPResponseCoalesceFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte("acp:\n  response_coalesce_ms: 0\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PARMESAN_CONFIG", path)
	t.Setenv("ACP_RESPONSE_COALESCE_MS", "")

	cfg := Load("api")
	if cfg.ACP.ResponseCoalesceMS != 0 {
		t.Fatalf("ResponseCoalesceMS = %d, want 0", cfg.ACP.ResponseCoalesceMS)
	}
	if got := os.Getenv("ACP_RESPONSE_COALESCE_MS"); got != "0" {
		t.Fatalf("ACP_RESPONSE_COALESCE_MS env = %q, want 0", got)
	}
}

func TestLoadDefaultsACPResponseCoalesceWhenUnset(t *testing.T) {
	t.Setenv("PARMESAN_CONFIG", "")
	t.Setenv("ACP_RESPONSE_COALESCE_MS", "")

	cfg := Load("api")
	if cfg.ACP.ResponseCoalesceMS != 1500 {
		t.Fatalf("ResponseCoalesceMS = %d, want 1500", cfg.ACP.ResponseCoalesceMS)
	}
}
