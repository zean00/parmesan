package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type HTTPConfig struct {
	Address string
}

type ProviderConfig struct {
	OpenAIAPIKey         string
	OpenRouterAPIKey     string
	OpenRouterBase       string
	DefaultReasoning     string
	DefaultStructured    string
	DefaultEmbedding     string
	MaintainerReasoning  string
	MaintainerStructured string
	MaintainerEmbedding  string
}

type OperatorConfig struct {
	APIKey               string
	TrustedIDHeader      string
	TrustedRolesHeader   string
	DefaultOperatorID    string
	DefaultOperatorRoles string
}

type KnowledgeConfig struct {
	Root string
}

type ACPConfig struct {
	ResponseCoalesceMS    int
	DelegationTimeoutSecs int
}

type RetryModelProfileConfig struct {
	ID                 string `yaml:"id" json:"id"`
	Name               string `yaml:"name" json:"name,omitempty"`
	ReasoningProvider  string `yaml:"reasoning_provider" json:"reasoning_provider,omitempty"`
	ReasoningModel     string `yaml:"reasoning_model" json:"reasoning_model,omitempty"`
	StructuredProvider string `yaml:"structured_provider" json:"structured_provider,omitempty"`
	StructuredModel    string `yaml:"structured_model" json:"structured_model,omitempty"`
}

type BootstrapConfig struct {
	AgentsDir string
}

type MCPConfig struct {
	Providers []MCPProviderConfig `yaml:"providers" json:"providers,omitempty"`
}

type ToolProviderSecurityConfig struct {
	AllowedHosts  []string `yaml:"allowed_hosts" json:"allowed_hosts,omitempty"`
	AllowLocalDev bool     `yaml:"allow_local_dev" json:"allow_local_dev,omitempty"`
}

type AgentServerConfig struct {
	Command               string            `yaml:"command" json:"command"`
	Args                  []string          `yaml:"args" json:"args,omitempty"`
	Env                   map[string]string `yaml:"env" json:"env,omitempty"`
	StartupTimeoutSeconds int               `yaml:"startup_timeout_seconds" json:"startup_timeout_seconds,omitempty"`
	RequestTimeoutSeconds int               `yaml:"request_timeout_seconds" json:"request_timeout_seconds,omitempty"`
	ACP                   ACPAgentConfig    `yaml:"acp" json:"acp,omitempty"`
}

type ACPAgentConfig struct {
	Model        string               `yaml:"model" json:"model,omitempty"`
	PromptPrefix string               `yaml:"prompt_prefix" json:"prompt_prefix,omitempty"`
	PromptSuffix string               `yaml:"prompt_suffix" json:"prompt_suffix,omitempty"`
	MCPServers   []ACPMCPServerConfig `yaml:"mcp_servers" json:"mcp_servers,omitempty"`
}

type ACPMCPServerConfig struct {
	Type    string            `yaml:"type" json:"type"`
	Name    string            `yaml:"name" json:"name"`
	Command string            `yaml:"command" json:"command,omitempty"`
	Args    []string          `yaml:"args" json:"args,omitempty"`
	Env     map[string]string `yaml:"env" json:"env,omitempty"`
	URL     string            `yaml:"url" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	Meta    map[string]any    `yaml:"_meta" json:"_meta,omitempty"`
}

type CustomerContextConfig struct {
	Enrichment CustomerContextEnrichmentConfig `yaml:"enrichment" json:"enrichment,omitempty"`
}

type ModerationConfig struct {
	Alerts ModerationAlertConfig `yaml:"alerts" json:"alerts,omitempty"`
}

type ModerationAlertConfig struct {
	Enabled           bool     `yaml:"enabled" json:"enabled,omitempty"`
	NotifyOnCensored  bool     `yaml:"notify_on_censored" json:"notify_on_censored,omitempty"`
	NotifyOnJailbreak bool     `yaml:"notify_on_jailbreak" json:"notify_on_jailbreak,omitempty"`
	NotifyCategories  []string `yaml:"notify_categories" json:"notify_categories,omitempty"`
}

type CustomerContextEnrichmentConfig struct {
	Enabled        bool                                    `yaml:"enabled" json:"enabled,omitempty"`
	TimeoutSeconds int                                     `yaml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	OnError        string                                  `yaml:"on_error" json:"on_error,omitempty"`
	Sources        []CustomerContextEnrichmentSourceConfig `yaml:"sources" json:"sources,omitempty"`
}

type CustomerContextEnrichmentSourceConfig struct {
	ID               string                           `yaml:"id" json:"id"`
	Type             string                           `yaml:"type" json:"type"`
	Enabled          *bool                            `yaml:"enabled" json:"enabled,omitempty"`
	MergeStrategy    string                           `yaml:"merge_strategy" json:"merge_strategy,omitempty"`
	FieldMerge       map[string]string                `yaml:"field_merge" json:"field_merge,omitempty"`
	PromptSafeFields []string                         `yaml:"prompt_safe_fields" json:"prompt_safe_fields,omitempty"`
	Request          CustomerContextHTTPRequestConfig `yaml:"request" json:"request,omitempty"`
	ResponseMapping  CustomerContextMappingConfig     `yaml:"response_mapping" json:"response_mapping,omitempty"`
	DatabaseURL      string                           `yaml:"database_url" json:"database_url,omitempty"`
	Query            string                           `yaml:"query" json:"query,omitempty"`
	Args             []string                         `yaml:"args" json:"args,omitempty"`
	CustomerID       string                           `yaml:"customer_id" json:"customer_id,omitempty"`
	CustomerContext  map[string]any                   `yaml:"customer_context" json:"customer_context,omitempty"`
}

type CustomerContextHTTPRequestConfig struct {
	Method       string            `yaml:"method" json:"method,omitempty"`
	URL          string            `yaml:"url" json:"url,omitempty"`
	Headers      map[string]string `yaml:"headers" json:"headers,omitempty"`
	BodyTemplate string            `yaml:"body_template" json:"body_template,omitempty"`
}

type CustomerContextMappingConfig struct {
	CustomerID      string            `yaml:"customer_id" json:"customer_id,omitempty"`
	CustomerContext map[string]string `yaml:"customer_context" json:"customer_context,omitempty"`
}

type MCPProviderConfig struct {
	ID       string            `yaml:"id" json:"id"`
	Name     string            `yaml:"name" json:"name"`
	Kind     string            `yaml:"kind" json:"kind"`
	BaseURL  string            `yaml:"base_url" json:"base_url"`
	Metadata map[string]string `yaml:"metadata" json:"metadata,omitempty"`
}

type ObservabilityConfig struct {
	MetricsAddress string
	OTLPEndpoint   string
	OTLPInsecure   bool
	OTLPHeaders    string
	OrgID          string
}

type Config struct {
	ServiceName          string
	HTTP                 HTTPConfig
	Observability        ObservabilityConfig
	DatabaseURL          string
	SecretsMasterKey     string
	Provider             ProviderConfig
	Operator             OperatorConfig
	Knowledge            KnowledgeConfig
	ACP                  ACPConfig
	Bootstrap            BootstrapConfig
	MCP                  MCPConfig
	ToolProviderSecurity ToolProviderSecurityConfig
	AgentServers         map[string]AgentServerConfig
	CustomerContext      CustomerContextConfig
	Moderation           ModerationConfig
	RetryModelProfiles   []RetryModelProfileConfig
	AsyncWriteQueueSize  int
	RequestTimeout       time.Duration
}

func Load(service string) Config {
	fileCfg := loadFileConfig()
	applyFileEnv(fileCfg)
	return Config{
		ServiceName: service,
		HTTP: HTTPConfig{
			Address: env("HTTP_ADDR", defaultAddr(service)),
		},
		Observability: ObservabilityConfig{
			MetricsAddress: env("METRICS_ADDR", defaultMetricsAddr(service)),
			OTLPEndpoint:   env("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			OTLPInsecure:   boolEnv("OTEL_EXPORTER_OTLP_INSECURE", true),
			OTLPHeaders:    env("OTEL_EXPORTER_OTLP_HEADERS", ""),
			OrgID:          env("DEFAULT_ORG_ID", "default"),
		},
		DatabaseURL:      env("DATABASE_URL", fileCfg.Database.URL),
		SecretsMasterKey: env("SECRETS_MASTER_KEY", fileCfg.Secrets.MasterKey),
		Provider: ProviderConfig{
			OpenAIAPIKey:         env("OPENAI_API_KEY", fileCfg.Providers.OpenAIAPIKey),
			OpenRouterAPIKey:     env("OPENROUTER_API_KEY", fileCfg.Providers.OpenRouterAPIKey),
			OpenRouterBase:       env("OPENROUTER_BASE_URL", defaultString(fileCfg.Providers.OpenRouterBaseURL, "https://openrouter.ai/api/v1")),
			DefaultReasoning:     env("DEFAULT_REASONING_PROVIDER", defaultString(fileCfg.Providers.DefaultReasoning, "openrouter")),
			DefaultStructured:    env("DEFAULT_STRUCTURED_PROVIDER", defaultString(fileCfg.Providers.DefaultStructured, "openrouter")),
			DefaultEmbedding:     env("DEFAULT_EMBEDDING_PROVIDER", defaultString(fileCfg.Providers.DefaultEmbedding, "openrouter")),
			MaintainerReasoning:  env("DEFAULT_MAINTAINER_REASONING_PROVIDER", env("DEFAULT_REASONING_PROVIDER", defaultString(fileCfg.Providers.MaintainerReasoning, "openrouter"))),
			MaintainerStructured: env("DEFAULT_MAINTAINER_STRUCTURED_PROVIDER", env("DEFAULT_STRUCTURED_PROVIDER", defaultString(fileCfg.Providers.MaintainerStructured, "openrouter"))),
			MaintainerEmbedding:  env("DEFAULT_MAINTAINER_EMBEDDING_PROVIDER", env("DEFAULT_EMBEDDING_PROVIDER", defaultString(fileCfg.Providers.MaintainerEmbedding, "openrouter"))),
		},
		Operator: OperatorConfig{
			APIKey:               env("OPERATOR_API_KEY", fileCfg.Operator.APIKey),
			TrustedIDHeader:      env("OPERATOR_TRUSTED_ID_HEADER", fileCfg.Operator.TrustedIDHeader),
			TrustedRolesHeader:   env("OPERATOR_TRUSTED_ROLES_HEADER", fileCfg.Operator.TrustedRolesHeader),
			DefaultOperatorID:    env("DEFAULT_OPERATOR_ID", defaultString(fileCfg.Operator.DefaultOperatorID, "dev_operator")),
			DefaultOperatorRoles: env("DEFAULT_OPERATOR_ROLES", defaultString(fileCfg.Operator.DefaultOperatorRoles, "operator")),
		},
		Knowledge: KnowledgeConfig{
			Root: env("KNOWLEDGE_SOURCE_ROOT", fileCfg.Knowledge.Root),
		},
		ACP: ACPConfig{
			ResponseCoalesceMS:    intEnvAllowZero("ACP_RESPONSE_COALESCE_MS", intPointerDefault(fileCfg.ACP.ResponseCoalesceMS, 1500)),
			DelegationTimeoutSecs: intEnv("ACP_DELEGATION_TIMEOUT_SECONDS", defaultInt(fileCfg.ACP.DelegationTimeoutSeconds, 30)),
		},
		Bootstrap: BootstrapConfig{
			AgentsDir: env("PARMESAN_AGENTS_DIR", fileCfg.Bootstrap.AgentsDir),
		},
		MCP: MCPConfig{Providers: fileCfg.MCP.Providers},
		ToolProviderSecurity: ToolProviderSecurityConfig{
			AllowedHosts:  csvEnv("TOOL_PROVIDER_ALLOWED_HOSTS", fileCfg.ToolProviders.AllowedHosts),
			AllowLocalDev: boolEnv("TOOL_PROVIDER_ALLOW_LOCAL_DEV", fileCfg.ToolProviders.AllowLocalDev),
		},
		AgentServers:        fileCfg.AgentServers,
		CustomerContext:     fileCfg.CustomerContext,
		Moderation:          fileCfg.Moderation,
		RetryModelProfiles:  fileCfg.Runtime.RetryModelProfiles,
		AsyncWriteQueueSize: intEnv("ASYNC_WRITE_QUEUE_SIZE", 256),
		RequestTimeout:      durationEnv("REQUEST_TIMEOUT_SECONDS", 15),
	}
}

type fileConfig struct {
	HTTP struct {
		Address string `yaml:"address"`
	} `yaml:"http"`
	Database struct {
		URL string `yaml:"url"`
	} `yaml:"database"`
	Secrets struct {
		MasterKey string `yaml:"master_key"`
	} `yaml:"secrets"`
	Providers struct {
		OpenAIAPIKey         string `yaml:"openai_api_key"`
		OpenRouterAPIKey     string `yaml:"openrouter_api_key"`
		OpenRouterBaseURL    string `yaml:"openrouter_base_url"`
		DefaultReasoning     string `yaml:"default_reasoning"`
		DefaultStructured    string `yaml:"default_structured"`
		DefaultEmbedding     string `yaml:"default_embedding"`
		MaintainerReasoning  string `yaml:"maintainer_reasoning"`
		MaintainerStructured string `yaml:"maintainer_structured"`
		MaintainerEmbedding  string `yaml:"maintainer_embedding"`
	} `yaml:"providers"`
	Operator struct {
		APIKey               string `yaml:"api_key"`
		TrustedIDHeader      string `yaml:"trusted_id_header"`
		TrustedRolesHeader   string `yaml:"trusted_roles_header"`
		DefaultOperatorID    string `yaml:"default_operator_id"`
		DefaultOperatorRoles string `yaml:"default_operator_roles"`
	} `yaml:"operator"`
	Knowledge struct {
		Root string `yaml:"root"`
	} `yaml:"knowledge"`
	ACP struct {
		ResponseCoalesceMS       *int `yaml:"response_coalesce_ms"`
		DelegationTimeoutSeconds int  `yaml:"delegation_timeout_seconds"`
	} `yaml:"acp"`
	Bootstrap struct {
		AgentsDir string `yaml:"agents_dir"`
	} `yaml:"bootstrap"`
	ToolProviders struct {
		AllowedHosts  []string `yaml:"allowed_hosts"`
		AllowLocalDev bool     `yaml:"allow_local_dev"`
	} `yaml:"tool_providers"`
	MCP             MCPConfig                    `yaml:"mcp"`
	AgentServers    map[string]AgentServerConfig `yaml:"agent_servers"`
	CustomerContext CustomerContextConfig        `yaml:"customer_context"`
	Moderation      ModerationConfig             `yaml:"moderation"`
	Observability   struct {
		MetricsAddress string `yaml:"metrics_address"`
		OTLPEndpoint   string `yaml:"otlp_endpoint"`
		OTLPInsecure   *bool  `yaml:"otlp_insecure"`
		OTLPHeaders    string `yaml:"otlp_headers"`
		OrgID          string `yaml:"org_id"`
	} `yaml:"observability"`
	Runtime struct {
		AsyncWriteQueueSize int                       `yaml:"async_write_queue_size"`
		RequestTimeoutSecs  int                       `yaml:"request_timeout_seconds"`
		RetryModelProfiles  []RetryModelProfileConfig `yaml:"retry_model_profiles"`
	} `yaml:"runtime"`
}

func loadFileConfig() fileConfig {
	var cfg fileConfig
	path := strings.TrimSpace(os.Getenv("PARMESAN_CONFIG"))
	if path == "" {
		return cfg
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read PARMESAN_CONFIG %q: %v\n", path, err)
		return cfg
	}
	expanded := os.ExpandEnv(string(raw))
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: parse PARMESAN_CONFIG %q: %v\n", path, err)
	}
	return cfg
}

func applyFileEnv(cfg fileConfig) {
	setEnvDefault("HTTP_ADDR", cfg.HTTP.Address)
	setEnvDefault("DATABASE_URL", cfg.Database.URL)
	setEnvDefault("SECRETS_MASTER_KEY", cfg.Secrets.MasterKey)
	setEnvDefault("OPENAI_API_KEY", cfg.Providers.OpenAIAPIKey)
	setEnvDefault("OPENROUTER_API_KEY", cfg.Providers.OpenRouterAPIKey)
	setEnvDefault("OPENROUTER_BASE_URL", cfg.Providers.OpenRouterBaseURL)
	setEnvDefault("DEFAULT_REASONING_PROVIDER", cfg.Providers.DefaultReasoning)
	setEnvDefault("DEFAULT_STRUCTURED_PROVIDER", cfg.Providers.DefaultStructured)
	setEnvDefault("DEFAULT_EMBEDDING_PROVIDER", cfg.Providers.DefaultEmbedding)
	setEnvDefault("DEFAULT_MAINTAINER_REASONING_PROVIDER", cfg.Providers.MaintainerReasoning)
	setEnvDefault("DEFAULT_MAINTAINER_STRUCTURED_PROVIDER", cfg.Providers.MaintainerStructured)
	setEnvDefault("DEFAULT_MAINTAINER_EMBEDDING_PROVIDER", cfg.Providers.MaintainerEmbedding)
	setEnvDefault("OPERATOR_API_KEY", cfg.Operator.APIKey)
	setEnvDefault("OPERATOR_TRUSTED_ID_HEADER", cfg.Operator.TrustedIDHeader)
	setEnvDefault("OPERATOR_TRUSTED_ROLES_HEADER", cfg.Operator.TrustedRolesHeader)
	setEnvDefault("DEFAULT_OPERATOR_ID", cfg.Operator.DefaultOperatorID)
	setEnvDefault("DEFAULT_OPERATOR_ROLES", cfg.Operator.DefaultOperatorRoles)
	setEnvDefault("KNOWLEDGE_SOURCE_ROOT", cfg.Knowledge.Root)
	setEnvDefault("PARMESAN_AGENTS_DIR", cfg.Bootstrap.AgentsDir)
	if len(cfg.ToolProviders.AllowedHosts) > 0 {
		setEnvDefault("TOOL_PROVIDER_ALLOWED_HOSTS", strings.Join(cfg.ToolProviders.AllowedHosts, ","))
	}
	if cfg.ToolProviders.AllowLocalDev {
		setEnvDefault("TOOL_PROVIDER_ALLOW_LOCAL_DEV", strconv.FormatBool(cfg.ToolProviders.AllowLocalDev))
	}
	setEnvDefault("METRICS_ADDR", cfg.Observability.MetricsAddress)
	setEnvDefault("OTEL_EXPORTER_OTLP_ENDPOINT", cfg.Observability.OTLPEndpoint)
	setEnvDefault("OTEL_EXPORTER_OTLP_HEADERS", cfg.Observability.OTLPHeaders)
	setEnvDefault("DEFAULT_ORG_ID", cfg.Observability.OrgID)
	if cfg.Observability.OTLPInsecure != nil {
		setEnvDefault("OTEL_EXPORTER_OTLP_INSECURE", strconv.FormatBool(*cfg.Observability.OTLPInsecure))
	}
	if cfg.ACP.ResponseCoalesceMS != nil {
		setEnvDefault("ACP_RESPONSE_COALESCE_MS", strconv.Itoa(*cfg.ACP.ResponseCoalesceMS))
	}
	if cfg.ACP.DelegationTimeoutSeconds > 0 {
		setEnvDefault("ACP_DELEGATION_TIMEOUT_SECONDS", strconv.Itoa(cfg.ACP.DelegationTimeoutSeconds))
	}
	if cfg.Runtime.AsyncWriteQueueSize > 0 {
		setEnvDefault("ASYNC_WRITE_QUEUE_SIZE", strconv.Itoa(cfg.Runtime.AsyncWriteQueueSize))
	}
	if cfg.Runtime.RequestTimeoutSecs > 0 {
		setEnvDefault("REQUEST_TIMEOUT_SECONDS", strconv.Itoa(cfg.Runtime.RequestTimeoutSecs))
	}
}

func setEnvDefault(key, value string) {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(os.Getenv(key)) != "" {
		return
	}
	_ = os.Setenv(key, strings.TrimSpace(value))
}

func defaultAddr(service string) string {
	switch strings.ToLower(service) {
	case "gateway":
		return ":8081"
	case "worker":
		return ":8082"
	default:
		return ":8080"
	}
}

func csvEnv(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return normalizeCSV(fallback)
	}
	return normalizeCSV(strings.Split(raw, ","))
}

func normalizeCSV(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func defaultMetricsAddr(service string) string {
	switch strings.ToLower(service) {
	case "gateway":
		return ":9091"
	case "worker":
		return ":9092"
	default:
		return ":9090"
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func intPointerDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func durationEnv(key string, fallbackSeconds int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return time.Duration(fallbackSeconds) * time.Second
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return time.Duration(fallbackSeconds) * time.Second
	}

	return time.Duration(v) * time.Second
}

func intEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}

	return v
}

func intEnvAllowZero(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}

	return v
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
