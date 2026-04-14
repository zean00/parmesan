package toolruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/toolsecurity"
)

type Invoker struct {
	policy toolsecurity.ProviderURLPolicy
}

type ErrorClass string

const (
	ErrorRetryable        ErrorClass = "retryable"
	ErrorAuthFailed       ErrorClass = "auth_failed"
	ErrorInvalidArguments ErrorClass = "invalid_arguments"
	ErrorPolicyBlocked    ErrorClass = "policy_blocked"
	ErrorPermanent        ErrorClass = "permanent"
)

type InvokeError struct {
	Class     ErrorClass
	Retryable bool
	Message   string
	Status    int
}

func (e *InvokeError) Error() string {
	return e.Message
}

func New() *Invoker {
	return &Invoker{}
}

func (i *Invoker) WithProviderURLPolicy(policy toolsecurity.ProviderURLPolicy) *Invoker {
	i.policy = policy
	return i
}

func (i *Invoker) Invoke(ctx context.Context, binding tool.ProviderBinding, auth tool.AuthBinding, entry tool.CatalogEntry, input map[string]any) (map[string]any, error) {
	if err := i.policy.Validate(binding.URI); err != nil {
		return nil, classifyInvokeFailure(err, 0)
	}
	switch entry.RuntimeProtocol {
	case "", "mcp":
		return i.invokeMCP(ctx, binding, auth, entry, input)
	default:
		return nil, fmt.Errorf("unsupported runtime protocol %q", entry.RuntimeProtocol)
	}
}

func (i *Invoker) invokeMCP(ctx context.Context, binding tool.ProviderBinding, auth tool.AuthBinding, entry tool.CatalogEntry, input map[string]any) (map[string]any, error) {
	meta := parseMetadata(entry.MetadataJSON)
	source, _ := meta["source"].(string)
	if source == "openapi_import" {
		return i.invokeOpenAPIImport(ctx, binding, auth, meta, input)
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      entry.Name,
			"arguments": input,
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, binding.URI, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req, auth)
	resp, err := i.httpClient().Do(req)
	if err != nil {
		return nil, classifyInvokeFailure(err, 0)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyInvokeFailure(err, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, classifyHTTPStatus(resp.StatusCode, string(raw))
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"raw": string(raw)}, nil
	}
	if result, ok := decoded["result"].(map[string]any); ok {
		return result, nil
	}
	return decoded, nil
}

func (i *Invoker) invokeOpenAPIImport(ctx context.Context, binding tool.ProviderBinding, auth tool.AuthBinding, meta map[string]any, input map[string]any) (map[string]any, error) {
	method, _ := meta["method"].(string)
	path, _ := meta["path"].(string)
	if method == "" || path == "" {
		return nil, fmt.Errorf("openapi metadata missing method or path")
	}
	target, err := resolveURL(binding.URI, path)
	if err != nil {
		return nil, err
	}
	remaining, err := substitutePathParams(target, input)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if strings.EqualFold(method, http.MethodGet) {
		q := target.Query()
		for key, value := range remaining {
			q.Set(key, fmt.Sprint(value))
		}
		target.RawQuery = q.Encode()
	} else if len(remaining) > 0 {
		raw, err := json.Marshal(remaining)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), target.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyAuth(req, auth)
	resp, err := i.httpClient().Do(req)
	if err != nil {
		return nil, classifyInvokeFailure(err, 0)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyInvokeFailure(err, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, classifyHTTPStatus(resp.StatusCode, string(raw))
	}

	var out any
	if len(raw) > 0 && json.Unmarshal(raw, &out) == nil {
		return map[string]any{
			"status": resp.StatusCode,
			"body":   out,
		}, nil
	}
	return map[string]any{
		"status": resp.StatusCode,
		"body":   string(raw),
	}, nil
}

func parseMetadata(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func resolveURL(base, path string) (*url.URL, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	pathURL, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	return baseURL.ResolveReference(pathURL), nil
}

func substitutePathParams(target *url.URL, input map[string]any) (map[string]any, error) {
	remaining := map[string]any{}
	for key, value := range input {
		remaining[key] = value
	}
	path := target.Path
	for _, segment := range strings.Split(target.Path, "/") {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		value, ok := remaining[name]
		if !ok {
			return nil, &InvokeError{
				Class:     ErrorInvalidArguments,
				Retryable: false,
				Message:   fmt.Sprintf("tool invocation failed: missing path parameter %q", name),
			}
		}
		replacement := url.PathEscape(fmt.Sprint(value))
		path = strings.ReplaceAll(path, "{"+name+"}", replacement)
		delete(remaining, name)
	}
	target.Path = path
	return remaining, nil
}

func applyAuth(req *http.Request, auth tool.AuthBinding) {
	switch auth.Type {
	case tool.AuthBearer:
		if strings.TrimSpace(auth.Secret) != "" {
			req.Header.Set("Authorization", "Bearer "+auth.Secret)
		}
	case tool.AuthHeader:
		headerName := strings.TrimSpace(auth.HeaderName)
		if headerName == "" {
			headerName = "Authorization"
		}
		if strings.TrimSpace(auth.Secret) != "" {
			req.Header.Set(headerName, auth.Secret)
		}
	}
}

func classifyInvokeFailure(err error, status int) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &InvokeError{
			Class:     ErrorRetryable,
			Retryable: true,
			Message:   urlErr.Error(),
			Status:    status,
		}
	}
	return &InvokeError{
		Class:     ErrorRetryable,
		Retryable: true,
		Message:   err.Error(),
		Status:    status,
	}
}

func classifyHTTPStatus(status int, body string) error {
	msg := fmt.Sprintf("tool invocation failed: status=%d body=%s", status, body)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &InvokeError{Class: ErrorAuthFailed, Retryable: false, Message: msg, Status: status}
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return &InvokeError{Class: ErrorInvalidArguments, Retryable: false, Message: msg, Status: status}
	case status == http.StatusTooManyRequests || status >= 500:
		return &InvokeError{Class: ErrorRetryable, Retryable: true, Message: msg, Status: status}
	default:
		return &InvokeError{Class: ErrorPermanent, Retryable: false, Message: msg, Status: status}
	}
}

func (i *Invoker) httpClient() *http.Client {
	policy := i.policy
	if policy.RequestTimeout <= 0 {
		policy.RequestTimeout = 20 * time.Second
	}
	return policy.HTTPClient()
}
