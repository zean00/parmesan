package config

import (
	"os"
	"strconv"
	"strings"
	"time"
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

type ObservabilityConfig struct {
	MetricsAddress string
	OTLPEndpoint   string
	OTLPInsecure   bool
	OTLPHeaders    string
	OrgID          string
}

type Config struct {
	ServiceName         string
	HTTP                HTTPConfig
	Observability       ObservabilityConfig
	DatabaseURL         string
	SecretsMasterKey    string
	Provider            ProviderConfig
	AsyncWriteQueueSize int
	RequestTimeout      time.Duration
}

func Load(service string) Config {
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
		DatabaseURL:      env("DATABASE_URL", ""),
		SecretsMasterKey: env("SECRETS_MASTER_KEY", ""),
		Provider: ProviderConfig{
			OpenAIAPIKey:         env("OPENAI_API_KEY", ""),
			OpenRouterAPIKey:     env("OPENROUTER_API_KEY", ""),
			OpenRouterBase:       env("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
			DefaultReasoning:     env("DEFAULT_REASONING_PROVIDER", "openrouter"),
			DefaultStructured:    env("DEFAULT_STRUCTURED_PROVIDER", "openrouter"),
			DefaultEmbedding:     env("DEFAULT_EMBEDDING_PROVIDER", "openrouter"),
			MaintainerReasoning:  env("DEFAULT_MAINTAINER_REASONING_PROVIDER", env("DEFAULT_REASONING_PROVIDER", "openrouter")),
			MaintainerStructured: env("DEFAULT_MAINTAINER_STRUCTURED_PROVIDER", env("DEFAULT_STRUCTURED_PROVIDER", "openrouter")),
			MaintainerEmbedding:  env("DEFAULT_MAINTAINER_EMBEDDING_PROVIDER", env("DEFAULT_EMBEDDING_PROVIDER", "openrouter")),
		},
		AsyncWriteQueueSize: intEnv("ASYNC_WRITE_QUEUE_SIZE", 256),
		RequestTimeout:      durationEnv("REQUEST_TIMEOUT_SECONDS", 15),
	}
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
