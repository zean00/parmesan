package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func RunParlant(ctx context.Context, parlantRoot string, s Scenario) (NormalizedResult, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return NormalizedResult{}, err
	}
	script, err := filepath.Abs(filepath.Join("scripts", "parity", "parlant_runner.py"))
	if err != nil {
		return NormalizedResult{}, err
	}
	cmd := exec.CommandContext(ctx, "uv", "run", "python", script)
	cmd.Dir = parlantRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return NormalizedResult{}, fmt.Errorf("parlant runner failed: %s", msg)
	}
	var out NormalizedResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return NormalizedResult{}, fmt.Errorf("decode parlant output: %w", err)
	}
	return out, nil
}
