package toolsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/toolsecurity"
)

type Syncer struct {
	policy toolsecurity.ProviderURLPolicy
}

func New() *Syncer {
	return &Syncer{}
}

func (s *Syncer) WithProviderURLPolicy(policy toolsecurity.ProviderURLPolicy) *Syncer {
	s.policy = policy
	return s
}

func (s *Syncer) SyncProvider(ctx context.Context, binding tool.ProviderBinding) ([]tool.CatalogEntry, error) {
	if err := s.policy.Validate(binding.URI); err != nil {
		return nil, err
	}
	switch binding.Kind {
	case tool.ProviderOpenAPI:
		return s.syncOpenAPI(ctx, binding)
	case tool.ProviderMCP:
		return s.syncMCP(ctx, binding)
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", binding.Kind)
	}
}

func (s *Syncer) syncOpenAPI(ctx context.Context, binding tool.ProviderBinding) ([]tool.CatalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binding.URI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, err
	}

	var entries []tool.CatalogEntry
	for path, pathItem := range doc.Paths.Map() {
		for method, operation := range pathItem.Operations() {
			schema := operationJSON(operation)
			name := strings.TrimSpace(operation.OperationID)
			if name == "" {
				name = strings.ToLower(method) + "_" + sanitizePath(path)
			}
			entries = append(entries, tool.CatalogEntry{
				ID:              fmt.Sprintf("%s_%s", binding.ID, name),
				ProviderID:      binding.ID,
				Name:            name,
				Description:     operation.Description,
				Schema:          schema,
				RuntimeProtocol: "mcp",
				MetadataJSON: mustJSON(map[string]any{
					"source":       "openapi_import",
					"method":       strings.ToUpper(method),
					"path":         path,
					"operation_id": name,
				}),
				ImportedAt: time.Now().UTC(),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (s *Syncer) syncMCP(ctx context.Context, binding tool.ProviderBinding) ([]tool.CatalogEntry, error) {
	type rpcRequest struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	type toolResult struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	type toolsResp struct {
		Result struct {
			Tools []toolResult `json:"tools"`
		} `json:"result"`
	}

	call := func(body rpcRequest) ([]byte, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, binding.URI, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}

	_, _ = call(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "parmesan", "version": "0.1.0"},
		},
	})

	raw, err := call(rpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	})
	if err != nil {
		return nil, err
	}

	var decoded toolsResp
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	var entries []tool.CatalogEntry
	for _, item := range decoded.Result.Tools {
		schemaRaw, _ := json.Marshal(item.InputSchema)
		entries = append(entries, tool.CatalogEntry{
			ID:              fmt.Sprintf("%s_%s", binding.ID, item.Name),
			ProviderID:      binding.ID,
			Name:            item.Name,
			Description:     item.Description,
			Schema:          string(schemaRaw),
			RuntimeProtocol: "mcp",
			MetadataJSON: mustJSON(map[string]any{
				"source": "mcp",
				"tool":   item.Name,
			}),
			ImportedAt: time.Now().UTC(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func operationJSON(operation *openapi3.Operation) string {
	raw, err := json.Marshal(operation)
	if err != nil {
		return `{}`
	}
	return string(raw)
}

func sanitizePath(path string) string {
	replacer := strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_")
	return strings.Trim(replacer.Replace(path), "_")
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(raw)
}

func (s *Syncer) httpClient() *http.Client {
	policy := s.policy
	if policy.RequestTimeout <= 0 {
		policy.RequestTimeout = 20 * time.Second
	}
	return policy.HTTPClient()
}
