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

func TestLoadAgentServersFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte(`
agent_servers:
  OpenCode:
    command: opencode
    args: ["acp", "--pure"]
    env:
      OPENCODE_API_KEY: "${OPENROUTER_API_KEY}"
    startup_timeout_seconds: 7
    request_timeout_seconds: 11
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PARMESAN_CONFIG", path)
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg := Load("worker")
	server, ok := cfg.AgentServers["OpenCode"]
	if !ok {
		t.Fatalf("OpenCode agent server missing from config: %#v", cfg.AgentServers)
	}
	if server.Command != "opencode" {
		t.Fatalf("Command = %q, want opencode", server.Command)
	}
	if len(server.Args) != 2 || server.Args[0] != "acp" || server.Args[1] != "--pure" {
		t.Fatalf("Args = %#v, want opencode acp --pure args", server.Args)
	}
	if server.Env["OPENCODE_API_KEY"] != "test-key" {
		t.Fatalf("env expansion = %q, want test-key", server.Env["OPENCODE_API_KEY"])
	}
	if server.StartupTimeoutSeconds != 7 || server.RequestTimeoutSeconds != 11 {
		t.Fatalf("timeouts = %d/%d, want 7/11", server.StartupTimeoutSeconds, server.RequestTimeoutSeconds)
	}
}
