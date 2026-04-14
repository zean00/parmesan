package app

import (
	"context"
	"strings"
	"testing"
)

func TestRunAPIFailsWithoutSecretsMasterKeyInDurableMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/parmesan?sslmode=disable")
	t.Setenv("SECRETS_MASTER_KEY", "")
	err := RunAPI(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SECRETS_MASTER_KEY") {
		t.Fatalf("RunAPI() error = %v, want SECRETS_MASTER_KEY failure", err)
	}
}

func TestRunAPIFailsWhenConfiguredPostgresUnavailable(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://127.0.0.1:1/parmesan?sslmode=disable")
	t.Setenv("SECRETS_MASTER_KEY", "test-key")
	err := RunAPI(context.Background())
	if err == nil || !strings.Contains(err.Error(), "postgres unavailable") {
		t.Fatalf("RunAPI() error = %v, want postgres unavailable failure", err)
	}
}
