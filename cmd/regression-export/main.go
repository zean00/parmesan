package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type exportEnvelope struct {
	Items []map[string]any `json:"items"`
}

func main() {
	baseURL := flag.String("base-url", envOrDefault("PARMESAN_BASE_URL", "http://127.0.0.1:8080"), "base API URL")
	status := flag.String("status", envOrDefault("REGRESSION_EXPORT_STATUS", "accepted"), "fixture review status filter")
	outPath := flag.String("out", envOrDefault("REGRESSION_EXPORT_OUT", "artifacts/regression-fixtures.json"), "output file path")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP timeout")
	flag.Parse()

	token := strings.TrimSpace(os.Getenv("OPERATOR_API_KEY"))
	if token == "" {
		fatalf("OPERATOR_API_KEY is required")
	}

	client := &http.Client{Timeout: *timeout}
	url := strings.TrimRight(*baseURL, "/") + "/v1/operator/quality/regressions/export?status=" + *status
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		fatalf("request export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		fatalf("export failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload exportEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		fatalf("decode export: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	file, err := os.Create(*outPath)
	if err != nil {
		fatalf("create output: %v", err)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		fatalf("write output: %v", err)
	}
	fmt.Printf("exported %d regression fixtures to %s\n", len(payload.Items), *outPath)
}

func envOrDefault(key, fallback string) string {
	if got := strings.TrimSpace(os.Getenv(key)); got != "" {
		return got
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
