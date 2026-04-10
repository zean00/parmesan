package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sahal/parmesan/internal/config"
)

func main() {
	cfg := config.Load("migrate")
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL or database.url in PARMESAN_CONFIG is required")
		os.Exit(1)
	}

	dir := os.Getenv("PARMESAN_MIGRATIONS_DIR")
	if dir == "" {
		dir = "migrations"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "list migrations: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no migrations found in %s\n", dir)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ping database: %v\n", err)
		os.Exit(1)
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		fmt.Fprintf(os.Stderr, "ensure schema_migrations: %v\n", err)
		os.Exit(1)
	}

	for _, file := range files {
		version := filepath.Base(file)
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			fmt.Fprintf(os.Stderr, "check migration %s: %v\n", version, err)
			os.Exit(1)
		}
		if exists {
			fmt.Printf("skip %s\n", version)
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read migration %s: %v\n", file, err)
			os.Exit(1)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "begin migration %s: %v\n", version, err)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			_ = tx.Rollback(ctx)
			fmt.Fprintf(os.Stderr, "apply migration %s: %v\n", version, err)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			fmt.Fprintf(os.Stderr, "record migration %s: %v\n", version, err)
			os.Exit(1)
		}
		if err := tx.Commit(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "commit migration %s: %v\n", version, err)
			os.Exit(1)
		}
		fmt.Printf("applied %s\n", version)
	}
}
