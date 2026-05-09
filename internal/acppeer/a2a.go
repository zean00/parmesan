package acppeer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/toolsecurity"
)

const (
	a2aTaskStateSubmitted = "submitted"
	a2aTaskStateWorking   = "working"
	a2aTaskStateCompleted = "completed"
	a2aTaskStateFailed    = "failed"
	a2aTaskStateCanceled  = "canceled"
	a2aTaskStateRejected  = "rejected"
)

type a2aPeer struct {
	serverID       string
	config         config.AgentServerConfig
	providerPolicy toolsecurity.ProviderURLPolicy
	nextID         int64
}

type a2aAgentCard struct {
	ProtocolVersion string               `json:"protocolVersion"`
	Name            string               `json:"name"`
	URL             string               `json:"url"`
	Capabilities    a2aAgentCapabilities `json:"capabilities"`
}

type a2aAgentCapabilities struct {
	Streaming bool `json:"streaming,omitempty"`
}

type a2aMessageSendParams struct {
	Message       a2aMessage            `json:"message"`
	Configuration *a2aMessageSendConfig `json:"configuration,omitempty"`
	Metadata      map[string]any        `json:"metadata,omitempty"`
}

type a2aMessageSendConfig struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
	Blocking            *bool    `json:"blocking,omitempty"`
}

type a2aTaskQueryParams struct {
	ID string `json:"id"`
}

type a2aMessage struct {
	Kind      string         `json:"kind"`
	MessageID string         `json:"messageId"`
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aPart struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

type a2aTask struct {
	Kind      string        `json:"kind"`
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    a2aTaskStatus `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
	History   []a2aMessage  `json:"history,omitempty"`
}

type a2aTaskStatus struct {
	State   string      `json:"state"`
	Message *a2aMessage `json:"message,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Parts      []a2aPart `json:"parts"`
}

type a2aRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type a2aRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *a2aRPCError    `json:"error,omitempty"`
}

type a2aRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (p *a2aPeer) validate() error {
	cfg := p.config.A2A
	if strings.TrimSpace(cfg.URL) == "" && strings.TrimSpace(cfg.CardURL) == "" {
		return fmt.Errorf("agent server %q a2a.url or a2a.card_url is required", p.serverID)
	}
	for _, rawURL := range []string{cfg.URL, cfg.CardURL} {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		if err := p.providerPolicy.Validate(rawURL); err != nil {
			return fmt.Errorf("agent server %q invalid A2A URL: %w", p.serverID, err)
		}
	}
	return nil
}

func (p *a2aPeer) delegate(ctx context.Context, req Request) (Result, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return Result{ServerID: p.serverID, Protocol: "a2a", Status: "failed", Error: "session id is required"}, errors.New("session id is required")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return Result{ServerID: p.serverID, SessionID: sessionID, Protocol: "a2a", Status: "failed", Error: "prompt is required"}, errors.New("prompt is required")
	}
	timeout := time.Duration(defaultPositive(p.config.RequestTimeoutSeconds, 30)) * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	blocking := true
	params := a2aMessageSendParams{
		Message: a2aMessage{
			Kind:      "message",
			MessageID: stableA2AID("msg", sessionID, atomic.AddInt64(&p.nextID, 1)),
			Role:      "user",
			Parts:     []a2aPart{{Kind: "text", Text: prompt}},
			ContextID: sessionID,
			Metadata:  cloneAnyMap(req.Metadata),
		},
		Configuration: &a2aMessageSendConfig{
			AcceptedOutputModes: []string{"text/plain", "text"},
			Blocking:            &blocking,
		},
		Metadata: cloneAnyMap(req.Metadata),
	}
	raw, err := p.callRaw(waitCtx, "message/send", params)
	if err != nil {
		return Result{ServerID: p.serverID, SessionID: sessionID, Protocol: "a2a", Status: "failed", Error: err.Error()}, err
	}
	task, err := a2aTaskFromSendResult(raw)
	if err != nil {
		return Result{ServerID: p.serverID, SessionID: sessionID, Protocol: "a2a", Status: "failed", Error: err.Error()}, err
	}
	task, err = p.waitForTerminalTask(waitCtx, task)
	if err != nil {
		status := strings.TrimSpace(task.Status.State)
		if status == "" {
			status = "failed"
		}
		return Result{ServerID: p.serverID, SessionID: firstNonEmpty(task.ContextID, sessionID), Protocol: "a2a", Status: status, Error: err.Error()}, err
	}
	text := strings.TrimSpace(a2aTaskText(task))
	if text == "" {
		text = fmt.Sprintf("A2A task %s finished with state %s", task.ID, task.Status.State)
	}
	status := a2aResultStatus(task.Status.State)
	result := Result{
		ServerID:  p.serverID,
		SessionID: firstNonEmpty(task.ContextID, sessionID),
		Status:    status,
		Text:      text,
		Protocol:  "a2a",
	}
	if status != "completed" {
		result.Error = text
		return result, errors.New(text)
	}
	return result, nil
}

func (p *a2aPeer) waitForTerminalTask(ctx context.Context, task a2aTask) (a2aTask, error) {
	for {
		switch strings.ToLower(strings.TrimSpace(task.Status.State)) {
		case a2aTaskStateCompleted, a2aTaskStateFailed, a2aTaskStateCanceled, a2aTaskStateRejected:
			return task, nil
		case "":
			return task, errors.New("A2A task missing status")
		}
		if strings.TrimSpace(task.ID) == "" {
			return task, errors.New("A2A task is non-terminal and missing id")
		}
		interval := time.Duration(defaultPositive(p.config.A2A.PollIntervalMS, 500)) * time.Millisecond
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return task, ctx.Err()
		case <-timer.C:
		}
		var next a2aTask
		if err := p.call(ctx, "tasks/get", a2aTaskQueryParams{ID: task.ID}, &next); err != nil {
			return task, err
		}
		task = next
	}
}

func (p *a2aPeer) call(ctx context.Context, method string, params any, out any) error {
	raw, err := p.callRaw(ctx, method, params)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (p *a2aPeer) callRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	endpoint, err := p.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.providerPolicy.Validate(endpoint); err != nil {
		return nil, err
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(a2aRPCRequest{
		JSONRPC: "2.0",
		ID:      atomic.AddInt64(&p.nextID, 1),
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	p.applyHeaders(httpReq)
	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	var rpcResp a2aRPCResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, p.maxBytes())).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("a2a error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (p *a2aPeer) endpoint(ctx context.Context) (string, error) {
	if endpoint := strings.TrimSpace(p.config.A2A.URL); endpoint != "" {
		return endpoint, nil
	}
	card, err := p.fetchAgentCard(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(card.URL) == "" {
		return "", errors.New("A2A agent card missing url")
	}
	return strings.TrimSpace(card.URL), nil
}

func (p *a2aPeer) fetchAgentCard(ctx context.Context) (a2aAgentCard, error) {
	cardURL := strings.TrimSpace(p.config.A2A.CardURL)
	if cardURL == "" && strings.TrimSpace(p.config.A2A.URL) != "" {
		cardURL = a2aCardURLFromEndpoint(p.config.A2A.URL)
	}
	if cardURL == "" {
		return a2aAgentCard{}, errors.New("A2A agent card URL is required")
	}
	if err := p.providerPolicy.Validate(cardURL); err != nil {
		return a2aAgentCard{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return a2aAgentCard{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	p.applyHeaders(httpReq)
	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return a2aAgentCard{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return a2aAgentCard{}, fmt.Errorf("fetch agent card: HTTP %d", resp.StatusCode)
	}
	var card a2aAgentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, p.maxBytes())).Decode(&card); err != nil {
		return a2aAgentCard{}, err
	}
	return card, nil
}

func (p *a2aPeer) httpClient() *http.Client {
	return p.providerPolicy.HTTPClient()
}

func (p *a2aPeer) maxBytes() int64 {
	if p.config.A2A.MaxResponseBytes <= 0 {
		return 2 << 20
	}
	return int64(p.config.A2A.MaxResponseBytes)
}

func (p *a2aPeer) applyHeaders(req *http.Request) {
	for key, value := range p.config.A2A.Headers {
		if strings.TrimSpace(key) != "" && value != "" {
			req.Header.Set(key, os.ExpandEnv(value))
		}
	}
	if token := strings.TrimSpace(os.ExpandEnv(p.config.A2A.BearerToken)); token != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func a2aTaskText(task a2aTask) string {
	for i := len(task.Artifacts) - 1; i >= 0; i-- {
		if text := a2aTextFromParts(task.Artifacts[i].Parts); text != "" {
			return text
		}
	}
	if task.Status.Message != nil {
		if text := a2aTextFromParts(task.Status.Message.Parts); text != "" {
			return text
		}
	}
	for i := len(task.History) - 1; i >= 0; i-- {
		if text := a2aTextFromParts(task.History[i].Parts); text != "" {
			return text
		}
	}
	return ""
}

func a2aTaskFromSendResult(raw json.RawMessage) (a2aTask, error) {
	var discriminator struct {
		Kind      string          `json:"kind"`
		MessageID string          `json:"messageId"`
		Role      string          `json:"role"`
		Status    json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return a2aTask{}, err
	}
	switch strings.ToLower(strings.TrimSpace(discriminator.Kind)) {
	case "message":
		var message a2aMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return a2aTask{}, err
		}
		return a2aTaskFromMessage(message), nil
	case "task", "":
		if strings.TrimSpace(discriminator.MessageID) != "" || strings.TrimSpace(discriminator.Role) != "" {
			var message a2aMessage
			if err := json.Unmarshal(raw, &message); err != nil {
				return a2aTask{}, err
			}
			return a2aTaskFromMessage(message), nil
		}
		var task a2aTask
		if err := json.Unmarshal(raw, &task); err != nil {
			return a2aTask{}, err
		}
		return task, nil
	default:
		var task a2aTask
		if err := json.Unmarshal(raw, &task); err != nil {
			return a2aTask{}, err
		}
		if len(discriminator.Status) > 0 {
			return task, nil
		}
		return a2aTask{}, fmt.Errorf("unsupported A2A message/send result kind %q", discriminator.Kind)
	}
}

func a2aTaskFromMessage(message a2aMessage) a2aTask {
	return a2aTask{
		Kind:      "task",
		ID:        strings.TrimSpace(message.TaskID),
		ContextID: strings.TrimSpace(message.ContextID),
		Status: a2aTaskStatus{
			State:   a2aTaskStateCompleted,
			Message: &message,
		},
		History: []a2aMessage{message},
	}
}

func a2aTextFromParts(parts []a2aPart) string {
	var out []string
	for _, part := range parts {
		if part.Kind == "text" && strings.TrimSpace(part.Text) != "" {
			out = append(out, part.Text)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func a2aResultStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case a2aTaskStateCompleted:
		return "completed"
	case a2aTaskStateCanceled:
		return "canceled"
	case a2aTaskStateRejected:
		return "rejected"
	case a2aTaskStateFailed:
		return "failed"
	default:
		return "failed"
	}
}

func a2aCardURLFromEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(endpoint, "/") + "/.well-known/agent-card.json"
	}
	parsed.Path = "/.well-known/agent-card.json"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func stableA2AID(prefix, sessionID string, seq int64) string {
	base := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_").Replace(strings.TrimSpace(sessionID))
	if base == "" {
		base = "session"
	}
	return fmt.Sprintf("%s_%s_%d", prefix, base, seq)
}
