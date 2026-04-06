package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/sahal/parmesan/internal/parity"
)

func main() {
	var fixturePath string
	var outDir string
	var scenarioID string

	flag.StringVar(&fixturePath, "fixture", "examples/golden_scenarios.yaml", "path to golden scenario fixture")
	flag.StringVar(&outDir, "out", "internal/parity/testdata/golden", "directory to write Parmesan-only golden snapshots")
	flag.StringVar(&scenarioID, "scenario", "", "optional single scenario id to regenerate")
	flag.Parse()

	fx, err := parity.LoadFixture(fixturePath)
	if err != nil {
		log.Fatal(err)
	}
	snapshots, err := parity.RunGoldenCorpus(context.Background(), fx, scenarioID)
	if err != nil {
		log.Fatal(err)
	}
	for _, snapshot := range snapshots {
		path := filepath.Join(outDir, parity.SnapshotFileName(snapshot.ScenarioID))
		if err := parity.WriteGoldenSnapshot(path, snapshot); err != nil {
			log.Fatal(err)
		}
		fmt.Println(path)
	}
	if len(snapshots) == 0 {
		fmt.Fprintln(os.Stderr, "no snapshots generated")
		os.Exit(1)
	}
}
