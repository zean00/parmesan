package customercontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/session"
)

type Enricher struct {
	cfg    config.CustomerContextEnrichmentConfig
	client *http.Client
}

type Request struct {
	SessionID       string
	AgentID         string
	CustomerID      string
	Channel         string
	Labels          []string
	Metadata        map[string]any
	Meta            map[string]any
	CustomerContext map[string]any
}

type Result struct {
	CustomerID      string
	CustomerContext map[string]any
	Metadata        map[string]any
	Labels          []string
}

type sourcePatch struct {
	CustomerID      string
	CustomerContext map[string]any
	PromptSafe      []string
}

func New(cfg config.CustomerContextConfig) *Enricher {
	if !cfg.Enrichment.Enabled {
		return nil
	}
	return &Enricher{
		cfg: cfg.Enrichment,
		client: &http.Client{
			Timeout: time.Duration(defaultPositive(cfg.Enrichment.TimeoutSeconds, 2)) * time.Second,
		},
	}
}

func (e *Enricher) Enrich(ctx context.Context, sess session.Session) (session.Session, error) {
	if e == nil || !e.cfg.Enabled || len(e.cfg.Sources) == 0 {
		return sess, nil
	}
	timeout := time.Duration(defaultPositive(e.cfg.TimeoutSeconds, 2)) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := Result{
		CustomerID:      strings.TrimSpace(sess.CustomerID),
		CustomerContext: cloneMap(mapField(sess.Metadata, "customer_context")),
		Metadata:        cloneMap(sess.Metadata),
		Labels:          append([]string(nil), sess.Labels...),
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	meta := mapField(result.Metadata, "_meta")
	status := map[string]any{
		"status":      "succeeded",
		"enriched_at": time.Now().UTC().Format(time.RFC3339Nano),
		"sources":     []map[string]any{},
	}
	if result.CustomerContext == nil {
		result.CustomerContext = map[string]any{}
	}

	for _, source := range e.cfg.Sources {
		if !sourceEnabled(source) {
			continue
		}
		req := Request{
			SessionID:       sess.ID,
			AgentID:         sess.AgentID,
			CustomerID:      result.CustomerID,
			Channel:         sess.Channel,
			Labels:          result.Labels,
			Metadata:        result.Metadata,
			Meta:            meta,
			CustomerContext: result.CustomerContext,
		}
		patch, err := e.runSource(ctx, source, req)
		sourceStatus := map[string]any{"id": source.ID, "type": source.Type}
		if err != nil {
			sourceStatus["status"] = "failed"
			sourceStatus["error"] = err.Error()
			status["status"] = "partial"
			status["sources"] = append(status["sources"].([]map[string]any), sourceStatus)
			if strings.EqualFold(strings.TrimSpace(e.cfg.OnError), "fail_session") {
				result.Metadata["customer_context_enrichment"] = status
				sess.Metadata = result.Metadata
				return sess, err
			}
			continue
		}
		sourceStatus["status"] = "succeeded"
		status["sources"] = append(status["sources"].([]map[string]any), sourceStatus)
		mergePatch(&result, source, patch)
	}
	if result.CustomerID != "" {
		result.CustomerContext["id"] = result.CustomerID
		result.CustomerContext["customer_id"] = result.CustomerID
	}
	if len(result.CustomerContext) > 0 {
		result.Metadata["customer_context"] = result.CustomerContext
	}
	result.Metadata["customer_context_enrichment"] = status
	sess.CustomerID = result.CustomerID
	sess.Metadata = result.Metadata
	sess.Labels = result.Labels
	return sess, nil
}

func (e *Enricher) runSource(ctx context.Context, source config.CustomerContextEnrichmentSourceConfig, req Request) (sourcePatch, error) {
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "static":
		return sourcePatch{CustomerID: renderString(source.CustomerID, req), CustomerContext: cloneMap(source.CustomerContext), PromptSafe: source.PromptSafeFields}, nil
	case "http":
		return e.runHTTP(ctx, source, req)
	case "sql":
		return e.runSQL(ctx, source, req)
	default:
		return sourcePatch{}, fmt.Errorf("unsupported customer context source type %q", source.Type)
	}
}

func (e *Enricher) runHTTP(ctx context.Context, source config.CustomerContextEnrichmentSourceConfig, req Request) (sourcePatch, error) {
	method := strings.ToUpper(strings.TrimSpace(source.Request.Method))
	if method == "" {
		method = http.MethodGet
	}
	urlText, err := renderTemplate(source.Request.URL, req)
	if err != nil {
		return sourcePatch{}, err
	}
	var body io.Reader
	if strings.TrimSpace(source.Request.BodyTemplate) != "" {
		rendered, err := renderTemplate(source.Request.BodyTemplate, req)
		if err != nil {
			return sourcePatch{}, err
		}
		body = strings.NewReader(rendered)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, urlText, body)
	if err != nil {
		return sourcePatch{}, err
	}
	for key, value := range source.Request.Headers {
		rendered, err := renderTemplate(value, req)
		if err != nil {
			return sourcePatch{}, err
		}
		httpReq.Header.Set(key, rendered)
	}
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return sourcePatch{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode >= 400 {
		return sourcePatch{}, fmt.Errorf("http enricher %s failed: %s", source.ID, resp.Status)
	}
	var payload any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return sourcePatch{}, err
		}
	}
	return patchFromMapping(source, payload), nil
}

func (e *Enricher) runSQL(ctx context.Context, source config.CustomerContextEnrichmentSourceConfig, req Request) (sourcePatch, error) {
	if strings.TrimSpace(source.DatabaseURL) == "" {
		return sourcePatch{}, errors.New("sql enricher database_url is required")
	}
	args := make([]any, 0, len(source.Args))
	for _, item := range source.Args {
		rendered, err := renderTemplate(item, req)
		if err != nil {
			return sourcePatch{}, err
		}
		args = append(args, rendered)
	}
	conn, err := pgx.Connect(ctx, source.DatabaseURL)
	if err != nil {
		return sourcePatch{}, err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, source.Query, args...)
	if err != nil {
		return sourcePatch{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return sourcePatch{}, err
		}
		return sourcePatch{PromptSafe: source.PromptSafeFields}, nil
	}
	values, err := rows.Values()
	if err != nil {
		return sourcePatch{}, err
	}
	fields := rows.FieldDescriptions()
	row := map[string]any{}
	for i, field := range fields {
		if i < len(values) {
			row[string(field.Name)] = values[i]
		}
	}
	return patchFromMapping(source, row), nil
}

func patchFromMapping(source config.CustomerContextEnrichmentSourceConfig, payload any) sourcePatch {
	patch := sourcePatch{CustomerContext: map[string]any{}, PromptSafe: source.PromptSafeFields}
	if source.ResponseMapping.CustomerID != "" {
		if value := selectValue(payload, source.ResponseMapping.CustomerID); value != nil {
			patch.CustomerID = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	for key, selector := range source.ResponseMapping.CustomerContext {
		if value := selectValue(payload, selector); value != nil {
			patch.CustomerContext[key] = value
		}
	}
	return patch
}

func mergePatch(result *Result, source config.CustomerContextEnrichmentSourceConfig, patch sourcePatch) {
	strategy := normalizedStrategy(source.MergeStrategy)
	if result.CustomerContext == nil {
		result.CustomerContext = map[string]any{}
	}
	if patch.CustomerID != "" {
		mergeField(result, source, "customer_id", patch.CustomerID, strategy)
	}
	for key, value := range patch.CustomerContext {
		mergeField(result, source, key, value, strategy)
	}
	if len(patch.PromptSafe) > 0 {
		existing := stringSlice(result.Metadata["customer_context_prompt_safe_fields"])
		result.Metadata["customer_context_prompt_safe_fields"] = dedupeStrings(append(existing, patch.PromptSafe...))
	}
}

func mergeField(result *Result, source config.CustomerContextEnrichmentSourceConfig, key string, value any, strategy string) {
	if value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return
	}
	if fieldStrategy := strings.TrimSpace(source.FieldMerge[key]); fieldStrategy != "" {
		strategy = normalizedStrategy(fieldStrategy)
	}
	if key == "customer_id" {
		if result.CustomerID == "" || strategy == "overwrite" {
			result.CustomerID = strings.TrimSpace(fmt.Sprint(value))
			return
		}
		if strategy == "keep_both" && result.CustomerID != strings.TrimSpace(fmt.Sprint(value)) {
			addAlternate(result.Metadata, key, value, source.ID)
		}
		return
	}
	_, exists := result.CustomerContext[key]
	switch strategy {
	case "overwrite":
		result.CustomerContext[key] = value
	case "keep_both":
		if !exists {
			result.CustomerContext[key] = value
		} else if fmt.Sprint(result.CustomerContext[key]) != fmt.Sprint(value) {
			addAlternate(result.Metadata, key, value, source.ID)
		}
	default:
		if !exists {
			result.CustomerContext[key] = value
		}
	}
}

func addAlternate(metadata map[string]any, key string, value any, sourceID string) {
	alts := mapField(metadata, "customer_context_alternates")
	if alts == nil {
		alts = map[string]any{}
		metadata["customer_context_alternates"] = alts
	}
	items, _ := alts[key].([]any)
	alts[key] = append(items, map[string]any{
		"value":       value,
		"source":      sourceID,
		"observed_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func renderString(text string, req Request) string {
	rendered, err := renderTemplate(text, req)
	if err != nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(rendered)
}

func renderTemplate(text string, req Request) (string, error) {
	tmpl, err := template.New("customer_context").Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"session_id":       req.SessionID,
		"agent_id":         req.AgentID,
		"customer_id":      req.CustomerID,
		"channel":          req.Channel,
		"labels":           req.Labels,
		"metadata":         req.Metadata,
		"meta":             req.Meta,
		"customer_context": req.CustomerContext,
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func selectValue(payload any, selector string) any {
	selector = strings.TrimSpace(selector)
	if selector == "" || payload == nil {
		return nil
	}
	if strings.HasPrefix(selector, "$.") {
		selector = strings.TrimPrefix(selector, "$.")
	}
	current := payload
	for _, part := range strings.Split(selector, ".") {
		key := part
		index := -1
		if open := strings.Index(part, "["); open >= 0 && strings.HasSuffix(part, "]") {
			key = part[:open]
			index, _ = strconv.Atoi(strings.TrimSuffix(part[open+1:], "]"))
		}
		if key != "" {
			mapped, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = mapped[key]
		}
		if index >= 0 {
			items, ok := current.([]any)
			if !ok || index >= len(items) {
				return nil
			}
			current = items[index]
		}
	}
	return current
}

func sourceEnabled(source config.CustomerContextEnrichmentSourceConfig) bool {
	return source.Enabled == nil || *source.Enabled
}

func normalizedStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "overwrite", "keep_both":
		return strings.ToLower(strings.TrimSpace(strategy))
	default:
		return "ignore"
	}
}

func mapField(values map[string]any, key string) map[string]any {
	raw, _ := values[key]
	if typed, ok := raw.(map[string]any); ok {
		return typed
	}
	return nil
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if nested, ok := value.(map[string]any); ok {
			out[key] = cloneMap(nested)
			continue
		}
		out[key] = value
	}
	return out
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func defaultPositive(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
