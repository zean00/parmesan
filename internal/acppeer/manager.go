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
	ServerID  string
	SessionID string
	Status    string
	Text      string
	Error     string
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

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	pending     map[int64]chan rpcResponse
	subscribers map[string][]chan updateNotification
	nextID      int64
	started     bool
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
	return p.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "parmesan",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{},
	}, &out)
}

func (p *peer) delegate(ctx context.Context, req Request) (Result, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return Result{}, errors.New("session id is required")
	}
	if strings.TrimSpace(req.CWD) == "" {
		req.CWD = "."
	}
	updates := make(chan updateNotification, 32)
	p.subscribe(sessionID, updates)
	defer p.unsubscribe(sessionID, updates)

	var sessionResp map[string]any
	if err := p.call(ctx, "session/new", map[string]any{
		"sessionId": sessionID,
		"cwd":       req.CWD,
		"metadata":  req.Metadata,
	}, &sessionResp); err != nil {
		return Result{}, err
	}
	var promptResp map[string]any
	if err := p.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": req.Prompt},
		},
	}, &promptResp); err != nil {
		return Result{}, err
	}

	timeout := time.Duration(defaultPositive(p.config.RequestTimeoutSeconds, 30)) * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var chunks []string
	for {
		select {
		case <-waitCtx.Done():
			return Result{ServerID: p.serverID, SessionID: sessionID, Status: "timeout", Error: waitCtx.Err().Error()}, waitCtx.Err()
		case item := <-updates:
			typ := stringField(item.Update, "type")
			switch typ {
			case "agent_message_chunk":
				if text := stringField(item.Update, "text"); strings.TrimSpace(text) != "" {
					chunks = append(chunks, text)
				}
			case "agent_turn_complete", "turn_complete":
				text := strings.TrimSpace(strings.Join(chunks, ""))
				if text == "" {
					text = stringField(item.Update, "text")
				}
				if text == "" {
					return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: "delegated agent produced no message"}, errors.New("delegated agent produced no message")
				}
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "completed", Text: text}, nil
			case "error", "agent_turn_error":
				msg := firstNonEmpty(stringField(item.Update, "message"), stringField(item.Update, "error"), "delegated agent failed")
				return Result{ServerID: p.serverID, SessionID: sessionID, Status: "failed", Error: msg}, errors.New(msg)
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
