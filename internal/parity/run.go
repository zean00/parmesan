package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	useAuthoritativeFallback := !parlantLiveAvailable()
	var reports []ScenarioReport
	for _, item := range fx.Scenarios {
		if opts.ScenarioID != "" && item.ID != opts.ScenarioID {
			continue
		}
		scenarioTimeout := opts.ScenarioTimeout
		if item.TimeoutSeconds > 0 {
			scenarioTimeout = time.Duration(item.TimeoutSeconds) * time.Second
		}
		scenarioCtx, cancel := scenarioContext(ctx, scenarioTimeout)

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
		cancel()
		if useAuthoritativeFallback {
			reports = append(reports, EvaluateScenario(item, parmesanResult, authoritativeParlantFallback(item)))
			continue
		}
		parlantResult, err := runParlantWithRetry(ctx, scenarioTimeout, opts.ParlantRoot, item, 2)
		if err != nil {
			if isAuthoritativeScenario(item) {
				reports = append(reports, EvaluateScenario(item, parmesanResult, authoritativeParlantFallback(item)))
				continue
			}
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

func parlantLiveAvailable() bool {
	return strings.TrimSpace(os.Getenv("EMCIE_API_KEY")) != ""
}

func scenarioContext(ctx context.Context, timeout time.Duration) (context.Context, func()) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}

func runParlantWithRetry(ctx context.Context, timeout time.Duration, parlantRoot string, item Scenario, attempts int) (NormalizedResult, error) {
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		scenarioCtx, cancel := scenarioContext(ctx, timeout)
		result, err := RunParlant(scenarioCtx, parlantRoot, item)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return NormalizedResult{}, lastErr
}
