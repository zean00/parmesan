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
    acp:
      model: anthropic/claude-3.7-sonnet
      prompt_prefix: "Solve carefully."
      prompt_suffix: "Return only the final answer."
      mcp_servers:
        - type: stdio
          name: Repo Tools
          command: npx
          args: ["-y", "@acme/repo-mcp"]
          env:
            REPO_TOKEN: "${OPENROUTER_API_KEY}"
        - type: sse
          name: Docs
          url: "https://docs.example/sse"
          headers:
            Authorization: "Bearer ${OPENROUTER_API_KEY}"
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
	if server.ACP.Model != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("ACP.Model = %q, want anthropic/claude-3.7-sonnet", server.ACP.Model)
	}
	if server.ACP.PromptPrefix != "Solve carefully." || server.ACP.PromptSuffix != "Return only the final answer." {
		t.Fatalf("ACP prompt injection = %#v, want configured prefix/suffix", server.ACP)
	}
	if len(server.ACP.MCPServers) != 2 {
		t.Fatalf("ACP.MCPServers = %#v, want two servers", server.ACP.MCPServers)
	}
	if server.ACP.MCPServers[0].Env["REPO_TOKEN"] != "test-key" {
		t.Fatalf("ACP MCP env expansion = %#v, want test-key", server.ACP.MCPServers[0].Env)
	}
	if server.ACP.MCPServers[1].Headers["Authorization"] != "Bearer test-key" {
		t.Fatalf("ACP MCP headers = %#v, want expanded auth header", server.ACP.MCPServers[1].Headers)
	}
}

func TestLoadCustomerContextEnrichmentFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte(`
customer_context:
  enrichment:
    enabled: true
    timeout_seconds: 3
    on_error: continue
    sources:
      - id: crm_http
        type: http
        merge_strategy: overwrite
        prompt_safe_fields: [name, tier]
        request:
          method: POST
          url: "${CRM_URL}/lookup"
          headers:
            Authorization: "Bearer ${CRM_TOKEN}"
          body_template: |
            {"customer_id":"{{ .customer_id }}"}
        response_mapping:
          customer_id: "$.id"
          customer_context:
            name: "$.profile.name"
            tier: "$.profile.tier"
      - id: crm_sql
        type: sql
        database_url: "${CRM_DATABASE_URL}"
        query: "select id, name from customers where id = $1"
        args: ["{{ .customer_id }}"]
        response_mapping:
          customer_id: "id"
          customer_context:
            name: "name"
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PARMESAN_CONFIG", path)
	t.Setenv("CRM_URL", "https://crm.example")
	t.Setenv("CRM_TOKEN", "secret")
	t.Setenv("CRM_DATABASE_URL", "postgres://crm")

	cfg := Load("api")
	enrichment := cfg.CustomerContext.Enrichment
	if !enrichment.Enabled || enrichment.TimeoutSeconds != 3 || len(enrichment.Sources) != 2 {
		t.Fatalf("enrichment = %#v, want enabled with two sources", enrichment)
	}
	if enrichment.Sources[0].Request.URL != "https://crm.example/lookup" || enrichment.Sources[0].Request.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("http source = %#v, want env-expanded request", enrichment.Sources[0].Request)
	}
	if enrichment.Sources[1].DatabaseURL != "postgres://crm" || enrichment.Sources[1].Args[0] != "{{ .customer_id }}" {
		t.Fatalf("sql source = %#v, want sql config", enrichment.Sources[1])
	}
}

func TestLoadModerationAlertsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte(`
moderation:
  alerts:
    enabled: true
    notify_on_censored: true
    notify_on_jailbreak: false
    notify_categories: [self_harm, violence]
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PARMESAN_CONFIG", path)

	cfg := Load("api")
	alerts := cfg.Moderation.Alerts
	if !alerts.Enabled || !alerts.NotifyOnCensored || alerts.NotifyOnJailbreak {
		t.Fatalf("alerts = %#v, want enabled censored-only config", alerts)
	}
	if len(alerts.NotifyCategories) != 2 || alerts.NotifyCategories[0] != "self_harm" || alerts.NotifyCategories[1] != "violence" {
		t.Fatalf("notify_categories = %#v, want self_harm/violence", alerts.NotifyCategories)
	}
}

func TestLoadRetryModelProfilesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte(`
runtime:
  retry_model_profiles:
    - id: structured_safe
      name: Structured-safe fallback
      reasoning_provider: openrouter
      reasoning_model: openai/gpt-4.1-mini
      structured_provider: openrouter
      structured_model: openai/gpt-4.1
    - id: provider_swap
      structured_provider: openai
      structured_model: gpt-4.1-mini
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PARMESAN_CONFIG", path)

	cfg := Load("api")
	if len(cfg.RetryModelProfiles) != 2 {
		t.Fatalf("RetryModelProfiles = %#v, want two profiles", cfg.RetryModelProfiles)
	}
	first := cfg.RetryModelProfiles[0]
	if first.ID != "structured_safe" || first.Name != "Structured-safe fallback" {
		t.Fatalf("first profile = %#v, want structured_safe named profile", first)
	}
	if first.ReasoningProvider != "openrouter" || first.StructuredModel != "openai/gpt-4.1" {
		t.Fatalf("first profile fields = %#v, want configured overrides", first)
	}
	second := cfg.RetryModelProfiles[1]
	if second.ID != "provider_swap" || second.StructuredProvider != "openai" || second.StructuredModel != "gpt-4.1-mini" {
		t.Fatalf("second profile = %#v, want provider_swap structured override", second)
	}
}

func TestLoadRuntimeWorkerSettingsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parmesan.yaml")
	raw := []byte(`
runtime:
  execution_concurrency: 3
  async_write_workers: 4
  async_write_queue_size: 512
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PARMESAN_CONFIG", path)
	t.Setenv("EXECUTION_CONCURRENCY", "")
	t.Setenv("ASYNC_WRITE_WORKERS", "")
	t.Setenv("ASYNC_WRITE_QUEUE_SIZE", "")

	cfg := Load("worker")
	if cfg.ExecutionConcurrency != 3 {
		t.Fatalf("ExecutionConcurrency = %d, want 3", cfg.ExecutionConcurrency)
	}
	if cfg.AsyncWriteWorkers != 4 {
		t.Fatalf("AsyncWriteWorkers = %d, want 4", cfg.AsyncWriteWorkers)
	}
	if got := os.Getenv("EXECUTION_CONCURRENCY"); got != "3" {
		t.Fatalf("EXECUTION_CONCURRENCY env = %q, want 3", got)
	}
	if got := os.Getenv("ASYNC_WRITE_WORKERS"); got != "4" {
		t.Fatalf("ASYNC_WRITE_WORKERS env = %q, want 4", got)
	}
	if cfg.AsyncWriteQueueSize != 512 {
		t.Fatalf("AsyncWriteQueueSize = %d, want 512", cfg.AsyncWriteQueueSize)
	}
}
