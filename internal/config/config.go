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
	OpenAIAPIKey      string
	OpenRouterAPIKey  string
	OpenRouterBase    string
	DefaultReasoning  string
	DefaultStructured string
}

type Config struct {
	ServiceName         string
	HTTP                HTTPConfig
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
		DatabaseURL:      env("DATABASE_URL", ""),
		SecretsMasterKey: env("SECRETS_MASTER_KEY", ""),
		Provider: ProviderConfig{
			OpenAIAPIKey:      env("OPENAI_API_KEY", ""),
			OpenRouterAPIKey:  env("OPENROUTER_API_KEY", ""),
			OpenRouterBase:    env("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
			DefaultReasoning:  env("DEFAULT_REASONING_PROVIDER", "openrouter"),
			DefaultStructured: env("DEFAULT_STRUCTURED_PROVIDER", "openrouter"),
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
