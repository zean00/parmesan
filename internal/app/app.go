package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sahal/parmesan/internal/acppeer"
	httpapi "github.com/sahal/parmesan/internal/api/http"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/customercontext"
	"github.com/sahal/parmesan/internal/engine/runner"
	"github.com/sahal/parmesan/internal/gateway"
	orbyteintegration "github.com/sahal/parmesan/internal/integrations/orbyte"
	"github.com/sahal/parmesan/internal/lifecycle"
	maintainerworker "github.com/sahal/parmesan/internal/maintainer"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/observability"
	replayrunner "github.com/sahal/parmesan/internal/replay"
	"github.com/sahal/parmesan/internal/secrets"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
	"github.com/sahal/parmesan/internal/store/postgres"
	"github.com/sahal/parmesan/internal/toolsecurity"
	"github.com/sahal/parmesan/internal/toolsync"
	"github.com/sahal/parmesan/internal/worker"
)

func RunAPI(ctx context.Context) error {
	cfg := config.Load("api")
	logger := slog.Default().With("service", cfg.ServiceName)
	var repo store.Repository = memory.New()
	var postgresClient *postgres.Client
	var err error
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	broker := sse.NewBroker()
	router := model.NewRouter(cfg.Provider)
	providerPolicy := toolsecurity.ProviderURLPolicy{
		AllowedHosts:   cfg.ToolProviderSecurity.AllowedHosts,
		AllowLocalDev:  cfg.ToolProviderSecurity.AllowLocalDev,
		RequestTimeout: cfg.RequestTimeout,
	}
	syncer := toolsync.New().WithProviderURLPolicy(providerPolicy)
	shutdownObs, err := observability.Init(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownObs(context.Background()) }()

	if cfg.DatabaseURL != "" {
		if strings.TrimSpace(cfg.SecretsMasterKey) == "" {
			return fmt.Errorf("api requires SECRETS_MASTER_KEY when DATABASE_URL is configured")
		}
		postgresClient, err = postgres.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("postgres unavailable: %w", err)
		} else {
			postgresClient.Crypter = secrets.New(cfg.SecretsMasterKey)
			repo = postgresClient
			writes = asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
			defer postgresClient.Close()
			logger.Info("postgres connected")
		}
	}
	writes.Start(ctx, cfg.AsyncWriteWorkers)
	defer writes.Stop()

	server := httpapi.New(cfg.HTTP.Address, repo, writes, broker, router, syncer).
		WithCustomerContextEnricher(customercontext.New(cfg.CustomerContext)).
		WithModerationConfig(cfg.Moderation).
		WithRetryModelProfiles(cfg.RetryModelProfiles).
		WithToolArgumentResolver(orbyteintegration.NewToolArgumentResolver()).
		WithToolProviderSecurity(cfg.ToolProviderSecurity)
	return server.Run(ctx)
}

func RunGateway(ctx context.Context) error {
	cfg := config.Load("gateway")
	if shutdownObs, err := observability.Init(ctx, cfg); err == nil {
		defer func() { _ = shutdownObs(context.Background()) }()
	} else {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errDatabaseRequired("gateway")
	}
	if strings.TrimSpace(cfg.SecretsMasterKey) == "" {
		return fmt.Errorf("gateway requires SECRETS_MASTER_KEY when DATABASE_URL is configured")
	}
	client, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	client.Crypter = secrets.New(cfg.SecretsMasterKey)
	repo := store.Repository(client)
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	defer client.Close()
	slog.Info("gateway postgres connected", "service", cfg.ServiceName)
	writes.Start(ctx, cfg.AsyncWriteWorkers)
	defer writes.Stop()
	return gateway.New(cfg.HTTP.Address, repo, writes).Run(ctx)
}

func RunWorker(ctx context.Context) error {
	cfg := config.Load("worker")
	if shutdownObs, err := observability.Init(ctx, cfg); err == nil {
		defer func() { _ = shutdownObs(context.Background()) }()
	} else {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errDatabaseRequired("worker")
	}
	if strings.TrimSpace(cfg.SecretsMasterKey) == "" {
		return fmt.Errorf("worker requires SECRETS_MASTER_KEY when DATABASE_URL is configured")
	}
	client, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	client.Crypter = secrets.New(cfg.SecretsMasterKey)
	repo := store.Repository(client)
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	router := model.NewRouter(cfg.Provider)
	maintainerRouter := model.NewRouterWithDefaults(cfg.Provider, cfg.Provider.MaintainerReasoning, cfg.Provider.MaintainerStructured, cfg.Provider.MaintainerEmbedding)
	broker := sse.NewBroker()
	defer client.Close()
	slog.Info("worker postgres connected", "service", cfg.ServiceName)
	writes.Start(ctx, cfg.AsyncWriteWorkers)
	defer writes.Stop()
	peerManager := acppeer.NewManager(cfg.AgentServers)
	providerPolicy := toolsecurity.ProviderURLPolicy{
		AllowedHosts:   cfg.ToolProviderSecurity.AllowedHosts,
		AllowLocalDev:  cfg.ToolProviderSecurity.AllowLocalDev,
		RequestTimeout: cfg.RequestTimeout,
	}
	runner.New(repo, writes, broker, router, "worker-"+hostname()).
		WithExecutionConcurrency(cfg.ExecutionConcurrency).
		WithToolArgumentResolver(orbyteintegration.NewToolArgumentResolver()).
		WithProviderURLPolicy(providerPolicy).
		WithAgentPeers(peerManager).
		Start(ctx)
	maintainerworker.New(repo, maintainerRouter).Start(ctx)
	lifecycle.New(repo, writes, router).WithProviderURLPolicy(providerPolicy).Start(ctx)
	replayrunner.New(repo, writes).Start(ctx)
	return worker.New(cfg.HTTP.Address).Run(ctx)
}

func errDatabaseRequired(service string) error {
	return fmt.Errorf("%s requires DATABASE_URL because process-local memory stores cannot coordinate across gateway/worker processes", service)
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}
