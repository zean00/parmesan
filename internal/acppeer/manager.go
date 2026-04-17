package acppeer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sahal/parmesan/internal/config"
)

type Request struct {
	SessionID string
	CWD       string
	Prompt    string
	Metadata  map[string]any
}

type Result struct {
	ServerID            string
	SessionID           string
	Status              string
	Text                string
	Error               string
	Model               string
	MCPServerNames      []string
	PromptPrefixApplied bool
	PromptSuffixApplied bool
}

type Manager struct {
	configs map[string]config.AgentServerConfig
	mu      sync.Mutex
	peers   map[string]*peer
}

func NewManager(configs map[string]config.AgentServerConfig) *Manager {
	cloned := map[string]config.AgentServerConfig{}
	for key, value := range configs {
		cloned[key] = value
	}
	return &Manager{
		configs: cloned,
		peers:   map[string]*peer{},
	}
}

func (m *Manager) Has(serverID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.configs[strings.TrimSpace(serverID)]
	return ok
}

func (m *Manager) Delegate(ctx context.Context, serverID string, req Request) (Result, error) {
	serverID = strings.TrimSpace(serverID)
	p, err := m.peer(serverID)
	if err != nil {
		return Result{ServerID: serverID, SessionID: req.SessionID, Status: "failed", Error: err.Error()}, err
	}
	result, err := p.delegate(ctx, req)
	if err != nil {
		if result.Status != "" {
			return result, err
		}
		return Result{ServerID: serverID, SessionID: req.SessionID, Status: "failed", Error: err.Error()}, err
	}
	return result, nil
}

func (m *Manager) peer(serverID string) (*peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.peers[serverID]; p != nil {
		return p, nil
	}
	cfg, ok := m.configs[serverID]
	if !ok {
		return nil, fmt.Errorf("unknown agent server %q", serverID)
	}
	p := &peer{
		serverID:    serverID,
		config:      cfg,
		pending:     map[int64]chan rpcResponse{},
		subscribers: map[string][]chan updateNotification{},
	}
	if err := p.start(); err != nil {
		return nil, err
	}
	m.peers[serverID] = p
	return p, nil
}

type peer struct {
	serverID string
	config   config.AgentServerConfig
	info     peerInfo

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	pending     map[int64]chan rpcResponse
	subscribers map[string][]chan updateNotification
	nextID      int64
	started     bool
}

type peerInfo struct {
	mcpHTTPKnown  bool
	mcpHTTP       bool
	mcpSSEKnown   bool
	mcpSSE        bool
	modelConfigID string
	modelValueIDs map[string]string
	initialized   bool
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

type updateNotification struct {
	SessionID string
	Update    map[string]any
}

func (p *peer) start() error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return nil
	}
	if strings.TrimSpace(p.config.Command) == "" {
		p.mu.Unlock()
		return errors.New("agent server command is required")
	}
	cmd := exec.Command(p.config.Command, p.config.Args...)
	cmd.Env = os.Environ()
	for key, value := range p.config.Env {
		cmd.Env = append(cmd.Env, key+"="+os.ExpandEnv(value))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.cmd = cmd
	p.stdin = stdin
	p.started = true
	p.mu.Unlock()

	go p.readLoop(stdout)
	go io.Copy(io.Discard, stderr)
	startCtx, cancel := context.WithTimeout(context.Background(), time.Duration(defaultPositive(p.config.StartupTimeoutSeconds, 10))*time.Second)
	defer cancel()
	if err := p.initialize(startCtx); err != nil {
		p.stop()
		return err
	}
	return nil
}

func (p *peer) stop() {
	p.mu.Lock()
	cmd := p.cmd
	stdin := p.stdin
	p.cmd = nil
	p.stdin = nil
	p.started = false
	p.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}

func (p *peer) initialize(ctx context.Context) error {
	var out map[string]any
	if err := p.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "parmesan",
			"version": "0.1.0",
		},
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"capabilities": map[string]any{},
	}, &out); err != nil {
		return err
	}
	p.info = parsePeerInfo(out)
	return nil
}

func (p *peer) delegate(ctx context.Context, req Request) (Result, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return Result{}, errors.New("session id is required")
	}
	if strings.TrimSpace(req.CWD) == "" {
		req.CWD = "."
	}
	if err := p.validateMCPServers(); err != nil {
		return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: err.Error()}, err
	}
	updates := make(chan updateNotification, 32)
	p.subscribe(sessionID, updates)
	defer func() {
		p.unsubscribe(sessionID, updates)
	}()

	sessionParams := map[string]any{
		"sessionId":  sessionID,
		"cwd":        req.CWD,
		"mcpServers": p.sessionMCPServers(),
		"_meta":      cloneAnyMap(req.Metadata),
		"metadata":   req.Metadata,
	}
	var sessionResp map[string]any
	if err := p.call(ctx, "session/new", sessionParams, &sessionResp); err != nil {
		return Result{}, err
	}
	if returnedSessionID := firstNonEmpty(stringField(sessionResp, "sessionId"), stringField(sessionResp, "session_id")); returnedSessionID != "" {
		if returnedSessionID != sessionID {
			p.unsubscribe(sessionID, updates)
			sessionID = returnedSessionID
			p.subscribe(sessionID, updates)
		}
		sessionID = returnedSessionID
	}
	modelName := strings.TrimSpace(p.config.ACP.Model)
	appliedModel := ""
	if modelName != "" {
		configID, valueID, ok := p.resolveModelConfig(sessionResp, modelName)
		if ok {
			var configResp map[string]any
			if err := p.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sessionID,
				"configId":  configID,
				"value":     valueID,
			}, &configResp); err != nil {
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: err.Error(), Model: modelName, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, err
			}
			appliedModel = modelName
		}
	}
	finalPrompt := p.wrapPrompt(req.Prompt)
	timeout := time.Duration(defaultPositive(p.config.RequestTimeoutSeconds, 30)) * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type promptResult struct {
		resp map[string]any
		err  error
	}
	promptDone := make(chan promptResult, 1)
	go func() {
		var promptResp map[string]any
		err := p.call(waitCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": finalPrompt},
			},
		}, &promptResp)
		promptDone <- promptResult{resp: promptResp, err: err}
	}()

	var chunks []string
	var promptResp map[string]any
	turnCompleted := false
	const promptCompletionStreamGrace = 150 * time.Millisecond
	var completionTimer *time.Timer
	var completionTimerC <-chan time.Time
	stopCompletionTimer := func() {
		if completionTimer == nil {
			return
		}
		if !completionTimer.Stop() {
			select {
			case <-completionTimer.C:
			default:
			}
		}
		completionTimer = nil
		completionTimerC = nil
	}
	resetCompletionTimer := func() {
		text := strings.TrimSpace(strings.Join(chunks, ""))
		if !promptCallCompleted(promptResp) || turnCompleted || text == "" || promptResponseText(promptResp) != "" {
			stopCompletionTimer()
			return
		}
		if completionTimer == nil {
			completionTimer = time.NewTimer(promptCompletionStreamGrace)
		} else {
			if !completionTimer.Stop() {
				select {
				case <-completionTimer.C:
				default:
				}
			}
			completionTimer.Reset(promptCompletionStreamGrace)
		}
		completionTimerC = completionTimer.C
	}
	defer stopCompletionTimer()
	for {
		select {
		case <-waitCtx.Done():
			if text := strings.TrimSpace(strings.Join(chunks, "")); text != "" && promptCallCompleted(promptResp) {
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, nil
			}
			return Result{ServerID: p.serverID, SessionID: sessionID, Status: "timeout", Error: waitCtx.Err().Error(), Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, waitCtx.Err()
		case outcome := <-promptDone:
			if outcome.err != nil {
				return Result{}, outcome.err
			}
			promptResp = outcome.resp
			if turnCompleted {
				if text := strings.TrimSpace(strings.Join(chunks, "")); text != "" {
					return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, nil
				}
			}
			if text := promptResponseText(promptResp); text != "" {
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, nil
			}
			resetCompletionTimer()
		case item := <-updates:
			typ := firstNonEmpty(stringField(item.Update, "type"), stringField(item.Update, "sessionUpdate"))
			switch typ {
			case "agent_message_chunk":
				if text := updateText(item.Update); strings.TrimSpace(text) != "" {
					chunks = append(chunks, text)
					resetCompletionTimer()
				}
			case "agent_turn_complete", "turn_complete", "agent_message":
				stopCompletionTimer()
				turnCompleted = true
				text := strings.TrimSpace(strings.Join(chunks, ""))
				if text == "" {
					text = updateText(item.Update)
				}
				if text == "" {
					return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: "delegated agent produced no message", Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, errors.New("delegated agent produced no message")
				}
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, nil
			case "error", "agent_turn_error":
				stopCompletionTimer()
				msg := firstNonEmpty(stringField(item.Update, "message"), stringField(item.Update, "error"), updateText(item.Update), "delegated agent failed")
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: msg, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, errors.New(msg)
			}
		case <-completionTimerC:
			stopCompletionTimer()
			if text := strings.TrimSpace(strings.Join(chunks, "")); text != "" && promptCallCompleted(promptResp) {
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text, Model: appliedModel, MCPServerNames: p.sessionMCPServerNames(), PromptPrefixApplied: strings.TrimSpace(p.config.ACP.PromptPrefix) != "", PromptSuffixApplied: strings.TrimSpace(p.config.ACP.PromptSuffix) != ""}, nil
			}
		}
	}
}

func (p *peer) call(ctx context.Context, method string, params any, out any) error {
	id := atomic.AddInt64(&p.nextID, 1)
	ch := make(chan rpcResponse, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		p.removePending(id)
		return err
	}
	if err := p.writeLine(raw); err != nil {
		p.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		p.removePending(id)
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func (p *peer) removePending(id int64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func (p *peer) writeLine(raw []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.stdin == nil {
		return errors.New("agent peer not started")
	}
	_, err := p.stdin.Write(append(raw, '\n'))
	return err
}

func (p *peer) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			p.resolve(*msg.ID, rpcResponse{Result: msg.Result, Error: msg.Error})
		case msg.Method == "session/update":
			p.handleUpdate(msg.Params)
		case msg.ID != nil && msg.Method != "":
			p.replyMethodNotFound(*msg.ID, msg.Method)
		}
	}
}

func (p *peer) resolve(id int64, resp rpcResponse) {
	p.mu.Lock()
	ch := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

func (p *peer) handleUpdate(raw json.RawMessage) {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return
	}
	sessionID := firstNonEmpty(stringField(params, "sessionId"), stringField(params, "session_id"))
	if sessionID == "" {
		return
	}
	update, _ := params["update"].(map[string]any)
	if update == nil {
		update = params
	}
	p.mu.Lock()
	subs := append([]chan updateNotification(nil), p.subscribers[sessionID]...)
	p.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub <- updateNotification{SessionID: sessionID, Update: update}:
		default:
		}
	}
}

func updateText(update map[string]any) string {
	if text := stringField(update, "text"); text != "" {
		return text
	}
	content, _ := update["content"].(map[string]any)
	if content == nil {
		return ""
	}
	return stringField(content, "text")
}

func promptResponseText(promptResp map[string]any) string {
	if promptResp == nil {
		return ""
	}
	if text := stringField(promptResp, "text"); text != "" {
		return text
	}
	message, _ := promptResp["message"].(map[string]any)
	if message != nil {
		if text := stringField(message, "text"); text != "" {
			return text
		}
	}
	content, _ := promptResp["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if block == nil {
			continue
		}
		if text := stringField(block, "text"); text != "" {
			return text
		}
	}
	return ""
}

func promptCallCompleted(promptResp map[string]any) bool {
	stopReason := strings.TrimSpace(firstNonEmpty(stringField(promptResp, "stopReason"), stringField(promptResp, "stop_reason")))
	switch stopReason {
	case "", "error":
		return false
	default:
		return true
	}
}

func (p *peer) replyMethodNotFound(id int64, method string) {
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": "unsupported method " + method,
		},
	})
	if err != nil {
		return
	}
	_ = p.writeLine(raw)
}

func (p *peer) subscribe(sessionID string, ch chan updateNotification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscribers[sessionID] = append(p.subscribers[sessionID], ch)
}

func (p *peer) unsubscribe(sessionID string, ch chan updateNotification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := p.subscribers[sessionID]
	out := items[:0]
	for _, item := range items {
		if item != ch {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		delete(p.subscribers, sessionID)
		return
	}
	p.subscribers[sessionID] = out
}

func (p *peer) wrapPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prefix := strings.TrimSpace(p.config.ACP.PromptPrefix); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}
	if suffix := strings.TrimSpace(p.config.ACP.PromptSuffix); suffix != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += suffix
	}
	return strings.TrimSpace(prompt)
}

func (p *peer) validateMCPServers() error {
	for _, server := range p.config.ACP.MCPServers {
		switch strings.ToLower(strings.TrimSpace(server.Type)) {
		case "", "stdio":
			continue
		case "http":
			if p.info.initialized && p.info.mcpHTTPKnown && !p.info.mcpHTTP {
				return fmt.Errorf("delegated agent %q does not advertise ACP HTTP MCP support", p.serverID)
			}
		case "sse":
			if p.info.initialized && p.info.mcpSSEKnown && !p.info.mcpSSE {
				return fmt.Errorf("delegated agent %q does not advertise ACP SSE MCP support", p.serverID)
			}
		default:
			return fmt.Errorf("delegated agent %q has unsupported MCP server type %q", p.serverID, server.Type)
		}
	}
	return nil
}

func (p *peer) sessionMCPServerNames() []string {
	out := make([]string, 0, len(p.config.ACP.MCPServers))
	for _, item := range p.config.ACP.MCPServers {
		if name := strings.TrimSpace(item.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func (p *peer) sessionMCPServers() []map[string]any {
	if len(p.config.ACP.MCPServers) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(p.config.ACP.MCPServers))
	for _, item := range p.config.ACP.MCPServers {
		serverType := strings.ToLower(firstNonEmpty(item.Type, "stdio"))
		server := map[string]any{
			"type": serverType,
			"name": item.Name,
		}
		if len(item.Meta) > 0 {
			server["_meta"] = cloneAnyMap(item.Meta)
		}
		switch serverType {
		case "stdio":
			server["command"] = item.Command
			server["args"] = append([]string(nil), item.Args...)
			server["env"] = cloneStringMap(item.Env)
		case "http", "sse":
			server["url"] = item.URL
			if serverType == "http" {
				server["headers"] = cloneStringMap(item.Headers)
			} else {
				headers := make([]map[string]string, 0, len(item.Headers))
				for key, value := range item.Headers {
					headers = append(headers, map[string]string{
						"name":  key,
						"value": value,
					})
				}
				server["headers"] = headers
			}
		}
		out = append(out, server)
	}
	return out
}

func (p *peer) resolveModelConfig(sessionResp map[string]any, modelName string) (string, string, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", "", false
	}
	configID, values := modelConfigSelection(parseConfigOptions(sessionResp))
	valueID := ""
	if configID != "" && values != nil {
		valueID = values[modelName]
	}
	if configID == "" || valueID == "" {
		configID, valueID = p.info.selectModelValue(modelName)
	}
	if configID == "" || valueID == "" {
		return "", "", false
	}
	return configID, valueID, true
}

func parsePeerInfo(initResp map[string]any) peerInfo {
	info := peerInfo{modelValueIDs: map[string]string{}}
	root := mapValue(initResp["agentCapabilities"])
	if root == nil {
		root = mapValue(initResp["capabilities"])
	}
	if root != nil {
		mcp := mapValue(root["mcpCapabilities"])
		if mcp != nil {
			info.mcpHTTP, info.mcpHTTPKnown = boolValueKnown(mcp["http"])
			info.mcpSSE, info.mcpSSEKnown = boolValueKnown(mcp["sse"])
		}
	}
	configID, values := modelConfigSelection(parseConfigOptions(initResp))
	info.modelConfigID = configID
	info.modelValueIDs = values
	info.initialized = true
	return info
}

func (p peerInfo) selectModelValue(modelName string) (string, string) {
	if p.modelConfigID == "" || len(p.modelValueIDs) == 0 {
		return "", ""
	}
	valueID := p.modelValueIDs[strings.TrimSpace(modelName)]
	if valueID == "" {
		return "", ""
	}
	return p.modelConfigID, valueID
}

func parseConfigOptions(values map[string]any) []map[string]any {
	raw, ok := values["configOptions"]
	if !ok {
		return nil
	}
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if decoded := mapValue(item); decoded != nil {
			out = append(out, decoded)
		}
	}
	return out
}

func modelConfigSelection(options []map[string]any) (string, map[string]string) {
	for _, option := range options {
		category := strings.ToLower(stringField(option, "category"))
		if category != "model" {
			continue
		}
		configID := firstNonEmpty(stringField(option, "configId"), stringField(option, "id"))
		if configID == "" {
			continue
		}
		values := map[string]string{}
		for _, item := range sliceMaps(option["options"]) {
			valueID := firstNonEmpty(stringField(item, "value"), stringField(item, "valueId"), stringField(item, "id"))
			if valueID == "" {
				continue
			}
			for _, key := range []string{"label", "name", "description"} {
				label := strings.TrimSpace(fmt.Sprint(item[key]))
				if label != "" {
					values[label] = valueID
				}
			}
			values[valueID] = valueID
		}
		return configID, values
	}
	return "", nil
}

func sliceMaps(raw any) []map[string]any {
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if decoded := mapValue(item); decoded != nil {
			out = append(out, decoded)
		}
	}
	return out
}

func mapValue(raw any) map[string]any {
	typed, _ := raw.(map[string]any)
	return typed
}

func boolValue(raw any) bool {
	value, _ := raw.(bool)
	return value
}

func boolValueKnown(raw any) (bool, bool) {
	value, ok := raw.(bool)
	return value, ok
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultPositive(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
