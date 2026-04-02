package app

import (
	"context"
	"fmt"
	"log"
	"os"

	httpapi "github.com/sahal/parmesan/internal/api/http"
	"github.com/sahal/parmesan/internal/api/sse"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/gateway"
	"github.com/sahal/parmesan/internal/model"
	replayrunner "github.com/sahal/parmesan/internal/replay"
	"github.com/sahal/parmesan/internal/runtime/runner"
	"github.com/sahal/parmesan/internal/secrets"
	"github.com/sahal/parmesan/internal/store"
	"github.com/sahal/parmesan/internal/store/asyncwrite"
	"github.com/sahal/parmesan/internal/store/memory"
	"github.com/sahal/parmesan/internal/store/postgres"
	"github.com/sahal/parmesan/internal/toolsync"
	"github.com/sahal/parmesan/internal/worker"
)

func RunAPI(ctx context.Context) error {
	cfg := config.Load("api")
	var repo store.Repository = memory.New()
	var postgresClient *postgres.Client
	var err error
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	broker := sse.NewBroker()
	router := model.NewRouter(cfg.Provider)
	syncer := toolsync.New()

	if cfg.DatabaseURL != "" {
		postgresClient, err = postgres.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Printf("postgres unavailable, continuing with memory store: %v", err)
		} else {
			postgresClient.Crypter = secrets.New(cfg.SecretsMasterKey)
			repo = postgresClient
			writes = asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
			defer postgresClient.Close()
			log.Printf("postgres connected")
		}
	}
	writes.Start(ctx, 1)
	defer writes.Stop()

	server := httpapi.New(cfg.HTTP.Address, repo, writes, broker, router, syncer)
	return server.Run(ctx)
}

func RunGateway(ctx context.Context) error {
	cfg := config.Load("gateway")
	if cfg.DatabaseURL == "" {
		return errDatabaseRequired("gateway")
	}
	client, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	client.Crypter = secrets.New(cfg.SecretsMasterKey)
	repo := store.Repository(client)
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	defer client.Close()
	log.Printf("gateway postgres connected")
	writes.Start(ctx, 1)
	defer writes.Stop()
	return gateway.New(cfg.HTTP.Address, repo, writes).Run(ctx)
}

func RunWorker(ctx context.Context) error {
	cfg := config.Load("worker")
	if cfg.DatabaseURL == "" {
		return errDatabaseRequired("worker")
	}
	client, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	client.Crypter = secrets.New(cfg.SecretsMasterKey)
	repo := store.Repository(client)
	writes := asyncwrite.New(repo, cfg.AsyncWriteQueueSize)
	router := model.NewRouter(cfg.Provider)
	broker := sse.NewBroker()
	defer client.Close()
	log.Printf("worker postgres connected")
	writes.Start(ctx, 1)
	defer writes.Stop()
	runner.New(repo, writes, broker, router, "worker-"+hostname()).Start(ctx)
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
