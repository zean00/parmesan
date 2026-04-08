package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/sahal/parmesan/internal/quality"
)

type domainPack struct {
	Domain             string         `json:"domain"`
	Total              int            `json:"total"`
	LiveGate           int            `json:"live_gate"`
	HighRisk           int            `json:"high_risk"`
	HighRiskLiveGate   int            `json:"high_risk_live_gate"`
	ByCategory         map[string]int `json:"by_category"`
	LiveGateByCategory map[string]int `json:"live_gate_by_category"`
	ByRisk             map[string]int `json:"by_risk"`
	LiveGateByRisk     map[string]int `json:"live_gate_by_risk"`
	Ready              bool           `json:"ready"`
	Failures           []string       `json:"failures,omitempty"`
}

type report struct {
	MinimumTotal    int          `json:"minimum_total"`
	MinimumLiveGate int          `json:"minimum_live_gate"`
	Packs           []domainPack `json:"packs"`
	Ready           bool         `json:"ready"`
}

func main() {
	var domain string
	var minTotal int
	var minLive int
	var fail bool
	flag.StringVar(&domain, "domain", "", "only report one domain")
	flag.IntVar(&minTotal, "min-total", 20, "minimum deterministic scenarios required per domain")
	flag.IntVar(&minLive, "min-live", 10, "minimum live-gate scenarios required per domain")
	flag.BoolVar(&fail, "fail", false, "exit non-zero when any reported domain is not ready")
	flag.Parse()

	out := buildReport(quality.ProductionReadinessScenarios(), domain, minTotal, minLive)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		log.Fatal(err)
	}
	if fail && !out.Ready {
		os.Exit(1)
	}
}

func buildReport(scenarios []quality.ScenarioExpectation, domain string, minTotal, minLive int) report {
	packs := map[string]*domainPack{}
	for _, scenario := range scenarios {
		if domain != "" && scenario.Domain != domain {
			continue
		}
		pack := packs[scenario.Domain]
		if pack == nil {
			pack = &domainPack{
				Domain:             scenario.Domain,
				ByCategory:         map[string]int{},
				LiveGateByCategory: map[string]int{},
				ByRisk:             map[string]int{},
				LiveGateByRisk:     map[string]int{},
			}
			packs[scenario.Domain] = pack
		}
		pack.Total++
		pack.ByCategory[scenario.Category]++
		pack.ByRisk[scenario.RiskTier]++
		if scenario.RiskTier == "high" {
			pack.HighRisk++
		}
		if scenario.LiveGate {
			pack.LiveGate++
			pack.LiveGateByCategory[scenario.Category]++
			pack.LiveGateByRisk[scenario.RiskTier]++
			if scenario.RiskTier == "high" {
				pack.HighRiskLiveGate++
			}
		}
	}

	out := report{
		MinimumTotal:    minTotal,
		MinimumLiveGate: minLive,
		Ready:           true,
	}
	for _, pack := range packs {
		pack.Ready, pack.Failures = evaluatePack(*pack, minTotal, minLive)
		if !pack.Ready {
			out.Ready = false
		}
		out.Packs = append(out.Packs, *pack)
	}
	sort.Slice(out.Packs, func(i, j int) bool {
		return out.Packs[i].Domain < out.Packs[j].Domain
	})
	if domain != "" && len(out.Packs) == 0 {
		out.Ready = false
		out.Packs = append(out.Packs, domainPack{
			Domain:   domain,
			Ready:    false,
			Failures: []string{fmt.Sprintf("domain %q has no scenarios", domain)},
		})
	}
	return out
}

func evaluatePack(pack domainPack, minTotal, minLive int) (bool, []string) {
	var failures []string
	if pack.Total < minTotal {
		failures = append(failures, fmt.Sprintf("total scenarios %d below minimum %d", pack.Total, minTotal))
	}
	if pack.LiveGate < minLive {
		failures = append(failures, fmt.Sprintf("live-gate scenarios %d below minimum %d", pack.LiveGate, minLive))
	}
	if len(pack.ByCategory) == 0 {
		failures = append(failures, "no category coverage")
	}
	if len(pack.LiveGateByCategory) == 0 {
		failures = append(failures, "no live-gate category coverage")
	}
	if pack.HighRisk > 0 && pack.HighRiskLiveGate == 0 {
		failures = append(failures, "high-risk scenarios exist but none are in the live gate")
	}
	return len(failures) == 0, failures
}
