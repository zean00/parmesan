package main

import (
	"testing"

	"github.com/sahal/parmesan/internal/quality"
)

func TestBuildReportMarksCatalogDomainsReady(t *testing.T) {
	report := buildReport(quality.BuiltInProductionReadinessScenarios(), "", 20, 10)
	if !report.Ready {
		t.Fatalf("report ready = false, packs = %#v", report.Packs)
	}
	if len(report.Packs) != 3 {
		t.Fatalf("packs = %d, want 3", len(report.Packs))
	}
	for _, pack := range report.Packs {
		if !pack.Ready {
			t.Fatalf("pack %s not ready: %v", pack.Domain, pack.Failures)
		}
		if pack.LiveGate < 10 {
			t.Fatalf("pack %s live gate = %d, want >= 10", pack.Domain, pack.LiveGate)
		}
	}
}

func TestBuildReportFailsMissingDomain(t *testing.T) {
	report := buildReport(quality.BuiltInProductionReadinessScenarios(), "banking", 20, 10)
	if report.Ready {
		t.Fatal("report ready = true, want false")
	}
	if len(report.Packs) != 1 || report.Packs[0].Domain != "banking" {
		t.Fatalf("packs = %#v, want missing banking pack", report.Packs)
	}
}

func TestEvaluatePackRequiresLiveHighRiskCoverage(t *testing.T) {
	ready, failures := evaluatePack(domainPack{
		Domain:   "support",
		Total:    20,
		LiveGate: 10,
		HighRisk: 5,
		ByCategory: map[string]int{
			"failure_modes": 20,
		},
		LiveGateByCategory: map[string]int{
			"failure_modes": 10,
		},
	}, 20, 10)
	if ready {
		t.Fatalf("ready = true, failures = %v", failures)
	}
}
