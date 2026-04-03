package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sahal/parmesan/internal/parity"
)

func main() {
	var opts parity.Options
	flag.StringVar(&opts.FixturePath, "fixture", "examples/golden_scenarios.yaml", "path to golden scenario fixture")
	flag.StringVar(&opts.ScenarioID, "scenario", "", "run a single scenario by id")
	flag.StringVar(&opts.ParlantRoot, "parlant-root", "/home/sahal/workspace/agents/parlant", "path to parlant repository root")
	flag.StringVar(&opts.JSONOut, "json-out", "", "optional path to write JSON report")
	flag.DurationVar(&opts.ScenarioTimeout, "scenario-timeout", 90*time.Second, "per-scenario execution timeout")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	report, err := parity.Run(ctx, opts)
	if err != nil {
		log.Fatal(err)
	}

	for _, item := range report.Scenarios {
		status := "PASS"
		if !item.Passed {
			status = "FAIL"
		}
		fmt.Printf("[%s] %s\n", status, item.Scenario.ID)
		for _, err := range item.ExpectationErrors {
			fmt.Printf("  expectation: %s\n", err)
		}
		for _, err := range item.DiffErrors {
			fmt.Printf("  diff: %s\n", err)
		}
	}

	fmt.Printf("\nSummary: %d passed, %d failed, %d total\n", report.PassedCount, report.FailedCount, report.ScenarioCount)
	if report.FailedCount > 0 {
		os.Exit(1)
	}
}
