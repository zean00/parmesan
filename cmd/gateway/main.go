package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/sahal/parmesan/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.RunGateway(ctx); err != nil {
		log.Fatal(err)
	}
}
