package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"sort"

	"github.com/sahal/parmesan/internal/quality"
)

type summary struct {
	Total      int            `json:"total"`
	LiveGate   int            `json:"live_gate"`
	ByCategory map[string]int `json:"by_category"`
	ByDomain   map[string]int `json:"by_domain"`
	ByRisk     map[string]int `json:"by_risk"`
}

func main() {
	var liveOnly bool
	var category string
	var domain string
	var summaryOnly bool
	flag.BoolVar(&liveOnly, "live-only", false, "print only scenarios marked for live-provider release gates")
	flag.StringVar(&category, "category", "", "filter by category")
	flag.StringVar(&domain, "domain", "", "filter by domain")
	flag.BoolVar(&summaryOnly, "summary", false, "print aggregate catalog summary instead of scenario list")
	flag.Parse()

	scenarios := filterScenarios(quality.ProductionReadinessScenarios(), liveOnly, category, domain)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if summaryOnly {
		if err := encoder.Encode(summarize(scenarios)); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := encoder.Encode(scenarios); err != nil {
		log.Fatal(err)
	}
}

func filterScenarios(scenarios []quality.ScenarioExpectation, liveOnly bool, category, domain string) []quality.ScenarioExpectation {
	var out []quality.ScenarioExpectation
	for _, scenario := range scenarios {
		if liveOnly && !scenario.LiveGate {
			continue
		}
		if category != "" && scenario.Category != category {
			continue
		}
		if domain != "" && scenario.Domain != domain {
			continue
		}
		out = append(out, scenario)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func summarize(scenarios []quality.ScenarioExpectation) summary {
	out := summary{
		Total:      len(scenarios),
		ByCategory: map[string]int{},
		ByDomain:   map[string]int{},
		ByRisk:     map[string]int{},
	}
	for _, scenario := range scenarios {
		if scenario.LiveGate {
			out.LiveGate++
		}
		out.ByCategory[scenario.Category]++
		out.ByDomain[scenario.Domain]++
		out.ByRisk[scenario.Risk]++
	}
	return out
}
