package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Options struct {
	FixturePath     string
	ScenarioID      string
	ParlantRoot     string
	JSONOut         string
	ScenarioTimeout time.Duration
}

func Run(ctx context.Context, opts Options) (Report, error) {
	fx, err := LoadFixture(opts.FixturePath)
	if err != nil {
		return Report{}, err
	}
	var reports []ScenarioReport
	for _, item := range fx.Scenarios {
		if opts.ScenarioID != "" && item.ID != opts.ScenarioID {
			continue
		}
		scenarioCtx := ctx
		cancel := func() {}
		scenarioTimeout := opts.ScenarioTimeout
		if item.TimeoutSeconds > 0 {
			scenarioTimeout = time.Duration(item.TimeoutSeconds) * time.Second
		}
		if scenarioTimeout > 0 {
			scenarioCtx, cancel = context.WithTimeout(ctx, scenarioTimeout)
		}

		parmesanResult, err := RunParmesan(scenarioCtx, item)
		if err != nil {
			cancel()
			reports = append(reports, ScenarioReport{
				Scenario: item,
				Passed:   false,
				ExpectationErrors: []string{
					fmt.Sprintf("parmesan execution failed: %v", err),
				},
			})
			continue
		}
		parlantResult, err := RunParlant(scenarioCtx, opts.ParlantRoot, item)
		cancel()
		if err != nil {
			if item.SkipParlantExpect && item.SkipEngineDiff {
				reports = append(reports, EvaluateScenario(item, parmesanResult, NormalizedResult{}))
				continue
			}
			reports = append(reports, ScenarioReport{
				Scenario: item,
				Parmesan: parmesanResult,
				Passed:   false,
				ExpectationErrors: []string{
					fmt.Sprintf("parlant execution failed: %v", err),
				},
			})
			continue
		}
		reports = append(reports, EvaluateScenario(item, parmesanResult, parlantResult))
	}
	report := BuildReport(fx, reports)
	if opts.JSONOut != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return report, err
		}
		if err := os.WriteFile(opts.JSONOut, raw, 0o644); err != nil {
			return report, err
		}
	}
	return report, nil
}
