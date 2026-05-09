package acppeer

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/toolsecurity"
)

func TestManagerDelegate(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegate"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER": "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				Model:        "anthropic/claude-3.7-sonnet",
				PromptPrefix: "Prefix instruction.",
				PromptSuffix: "Suffix instruction.",
				MCPServers: []config.ACPMCPServerConfig{
					{
						Type:    "stdio",
						Name:    "Repo Tools",
						Command: "npx",
						Args:    []string{"-y", "@acme/repo-mcp"},
						Env:     map[string]string{"REPO_TOKEN": "secret"},
					},
					{
						Type:    "sse",
						Name:    "Docs",
						URL:     "https://docs.example/sse",
						Headers: map[string]string{"Authorization": "Bearer secret"},
					},
				},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.Text != "Delegated answer" {
		t.Fatalf("result text = %q, want delegated answer", result.Text)
	}
	if result.Model != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("result model = %q, want configured model", result.Model)
	}
	if len(result.MCPServerNames) != 2 || result.MCPServerNames[0] != "Repo Tools" || result.MCPServerNames[1] != "Docs" {
		t.Fatalf("result MCP servers = %#v, want configured names", result.MCPServerNames)
	}
	if !result.PromptPrefixApplied || !result.PromptSuffixApplied {
		t.Fatalf("result prompt flags = %#v, want prefix and suffix applied", result)
	}
}

func TestManagerDelegateA2AWithAgentCardAndPolling(t *testing.T) {
	var serverURL string
	var sendSeen bool
	var getSeen bool
	var authSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-token" {
			authSeen = true
		}
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_ = json.NewEncoder(w).Encode(a2aAgentCard{
				ProtocolVersion: "0.3.0",
				Name:            "pigo-test",
				URL:             serverURL + "/a2a",
				Capabilities:    a2aAgentCapabilities{},
			})
		case "/a2a":
			var rpcReq a2aRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
				t.Errorf("decode rpc request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch rpcReq.Method {
			case "message/send":
				sendSeen = true
				var params a2aMessageSendParams
				if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
					t.Errorf("decode message/send params: %v", err)
				}
				if params.Message.Role != "user" {
					t.Errorf("message role = %q, want user", params.Message.Role)
				}
				if got := a2aTextFromParts(params.Message.Parts); got != "help with this task" {
					t.Errorf("message text = %q, want prompt", got)
				}
				writeA2AResult(t, w, rpcReq.ID, a2aTask{
					Kind:      "task",
					ID:        "task_1",
					ContextID: "sess_delegate_test",
					Status:    a2aTaskStatus{State: a2aTaskStateWorking},
				})
			case "tasks/get":
				getSeen = true
				writeA2AResult(t, w, rpcReq.ID, a2aTask{
					Kind:      "task",
					ID:        "task_1",
					ContextID: "sess_delegate_test",
					Status:    a2aTaskStatus{State: a2aTaskStateCompleted},
					Artifacts: []a2aArtifact{{
						ArtifactID: "artifact_1",
						Parts:      []a2aPart{{Kind: "text", Text: "Delegated A2A answer"}},
					}},
				})
			default:
				t.Errorf("unexpected method %q", rpcReq.Method)
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	manager := NewManager(map[string]config.AgentServerConfig{
		"Pigo": {
			Protocol:              "a2a",
			RequestTimeoutSeconds: 2,
			A2A: config.A2AAgentConfig{
				CardURL:        server.URL + "/.well-known/agent-card.json",
				BearerToken:    "test-token",
				PollIntervalMS: 1,
			},
		},
	}).WithProviderURLPolicy(toolsecurity.ProviderURLPolicy{AllowLocalDev: true})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "Pigo", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" || result.Text != "Delegated A2A answer" || result.Protocol != "a2a" {
		t.Fatalf("result = %#v, want completed A2A answer", result)
	}
	if result.SessionID != "sess_delegate_test" {
		t.Fatalf("result session id = %q, want context id", result.SessionID)
	}
	if !sendSeen || !getSeen {
		t.Fatalf("sendSeen=%v getSeen=%v, want both message/send and tasks/get", sendSeen, getSeen)
	}
	if !authSeen {
		t.Fatal("Authorization header was not sent")
	}
}

func TestManagerDelegateA2AFailedTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2aRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Errorf("decode rpc request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeA2AResult(t, w, rpcReq.ID, a2aTask{
			Kind:      "task",
			ID:        "task_failed",
			ContextID: "sess_delegate_test",
			Status: a2aTaskStatus{
				State:   a2aTaskStateRejected,
				Message: &a2aMessage{Kind: "message", MessageID: "msg_failed", Role: "agent", Parts: []a2aPart{{Kind: "text", Text: "cannot handle task"}}},
			},
		})
	}))
	defer server.Close()

	manager := NewManager(map[string]config.AgentServerConfig{
		"Pigo": {
			Protocol:              "a2a",
			RequestTimeoutSeconds: 2,
			A2A:                   config.A2AAgentConfig{URL: server.URL},
		},
	}).WithProviderURLPolicy(toolsecurity.ProviderURLPolicy{AllowLocalDev: true})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "Pigo", Request{
		SessionID: "sess_delegate_test",
		Prompt:    "help with this task",
	})
	if err == nil {
		t.Fatal("Delegate() error = nil, want rejected task error")
	}
	if result.Status != "rejected" || result.Error != "cannot handle task" || result.Protocol != "a2a" {
		t.Fatalf("result = %#v, want rejected A2A result", result)
	}
}

func TestManagerDelegateA2ADirectMessageResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2aRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Errorf("decode rpc request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if rpcReq.Method != "message/send" {
			t.Errorf("method = %q, want message/send", rpcReq.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeA2AResult(t, w, rpcReq.ID, a2aMessage{
			Kind:      "message",
			MessageID: "msg_direct",
			Role:      "agent",
			ContextID: "sess_delegate_test",
			Parts:     []a2aPart{{Kind: "text", Text: "Direct A2A message answer"}},
		})
	}))
	defer server.Close()

	manager := NewManager(map[string]config.AgentServerConfig{
		"Pigo": {
			Protocol:              "a2a",
			RequestTimeoutSeconds: 2,
			A2A:                   config.A2AAgentConfig{URL: server.URL},
		},
	}).WithProviderURLPolicy(toolsecurity.ProviderURLPolicy{AllowLocalDev: true})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "Pigo", Request{
		SessionID: "sess_delegate_test",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" || result.Text != "Direct A2A message answer" || result.Protocol != "a2a" {
		t.Fatalf("result = %#v, want completed direct A2A message answer", result)
	}
}

func writeA2AResult(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a2aRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	})
}

func TestManagerDelegateSkipsModelOverrideWhenPeerDoesNotAdvertiseConfigOptions(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateSkipsModelOverrideWhenPeerDoesNotAdvertiseConfigOptions"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":      "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":   "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS": "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				Model:      "anthropic/claude-3.7-sonnet",
				MCPServers: []config.ACPMCPServerConfig{{Type: "sse", Name: "Docs", URL: "https://docs.example/sse"}},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.Model != "" {
		t.Fatalf("result model = %q, want empty when model override is skipped", result.Model)
	}
	if len(result.MCPServerNames) != 1 || result.MCPServerNames[0] != "Docs" {
		t.Fatalf("result MCP servers = %#v, want configured names", result.MCPServerNames)
	}
}

func TestManagerDelegateHandlesReturnedSessionIDAndOpenCodeStyleUpdates(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateHandlesReturnedSessionIDAndOpenCodeStyleUpdates"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":           "1",
				"PARMESAN_TEST_ACP_RET_SESSION_ID":   "ses_remote_generated",
				"PARMESAN_TEST_ACP_UPDATE_STYLE":     "opencode",
				"PARMESAN_TEST_ACP_PROMPT_COMPLETES": "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":        "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":      "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 1,
			ACP: config.ACPAgentConfig{
				MCPServers: []config.ACPMCPServerConfig{{Type: "sse", Name: "Docs", URL: "https://docs.example/sse"}},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.SessionID != "ses_remote_generated" {
		t.Fatalf("result session id = %q, want returned session id", result.SessionID)
	}
	if result.Text != "Delegated answer" {
		t.Fatalf("result text = %q, want delegated answer", result.Text)
	}
}

func TestManagerDelegateWaitsForStreamCompletionAfterPromptCompletion(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateWaitsForStreamCompletionAfterPromptCompletion"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":           "1",
				"PARMESAN_TEST_ACP_UPDATE_STYLE":     "opencode",
				"PARMESAN_TEST_ACP_PROMPT_COMPLETES": "1",
				"PARMESAN_TEST_ACP_STREAM_CHUNKS":    "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":        "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":      "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				MCPServers: []config.ACPMCPServerConfig{{Type: "sse", Name: "Docs", URL: "https://docs.example/sse"}},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.Text != "Delegated answercontinued" {
		t.Fatalf("result text = %q, want full streamed delegated answer", result.Text)
	}
}

func TestManagerDelegateWaitsForLateStreamChunksAfterPromptCompletion(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateWaitsForLateStreamChunksAfterPromptCompletion"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":               "1",
				"PARMESAN_TEST_ACP_UPDATE_STYLE":         "opencode",
				"PARMESAN_TEST_ACP_PROMPT_COMPLETES":     "1",
				"PARMESAN_TEST_ACP_STREAM_CHUNKS":        "1",
				"PARMESAN_TEST_ACP_DELAY_FINAL_CHUNK_MS": "100",
				"PARMESAN_TEST_ACP_NO_CONFIG":            "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":          "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				MCPServers: []config.ACPMCPServerConfig{{Type: "sse", Name: "Docs", URL: "https://docs.example/sse"}},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.Text != "Delegated answercontinued" {
		t.Fatalf("result text = %q, want full late-stream delegated answer", result.Text)
	}
}

func TestManagerDelegateCompletesWhenPromptEndsAfterChunksWithoutTurnComplete(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateCompletesWhenPromptEndsAfterChunksWithoutTurnComplete"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":             "1",
				"PARMESAN_TEST_ACP_UPDATE_STYLE":       "opencode",
				"PARMESAN_TEST_ACP_PROMPT_COMPLETES":   "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":          "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":        "1",
				"PARMESAN_TEST_ACP_SKIP_TURN_COMPLETE": "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				MCPServers: []config.ACPMCPServerConfig{{Type: "sse", Name: "Docs", URL: "https://docs.example/sse"}},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
	if result.Text != "Delegated answer" {
		t.Fatalf("result text = %q, want delegated answer", result.Text)
	}
}

func TestManagerDelegatePreservesHTTPMCPTransport(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegatePreservesHTTPMCPTransport"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":        "1",
				"PARMESAN_TEST_ACP_VALIDATE_HTTP": "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":     "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":   "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP: config.ACPAgentConfig{
				MCPServers: []config.ACPMCPServerConfig{
					{
						Type:    "http",
						Name:    "HTTP Docs",
						URL:     "https://docs.example/http",
						Headers: map[string]string{"Authorization": "Bearer http-secret"},
					},
				},
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
}

func TestManagerDelegateSendsEmptySessionMCPServersWhenAgentConfigHasNone(t *testing.T) {
	if os.Getenv("PARMESAN_TEST_ACP_HELPER") == "1" {
		runACPHelperProcess()
		return
	}

	manager := NewManager(map[string]config.AgentServerConfig{
		"OpenCode": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestManagerDelegateSendsEmptySessionMCPServersWhenAgentConfigHasNone"},
			Env: map[string]string{
				"PARMESAN_TEST_ACP_HELPER":           "1",
				"PARMESAN_TEST_ACP_EXPECT_EMPTY_MCP": "1",
				"PARMESAN_TEST_ACP_NO_CONFIG":        "1",
				"PARMESAN_TEST_ACP_NO_MCP_CAPS":      "1",
			},
			StartupTimeoutSeconds: 2,
			RequestTimeoutSeconds: 2,
			ACP:                   config.ACPAgentConfig{},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := manager.Delegate(ctx, "OpenCode", Request{
		SessionID: "sess_delegate_test",
		CWD:       ".",
		Prompt:    "help with this task",
	})
	if err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v, want completed", result)
	}
}

func runACPHelperProcess() {
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id, hasID := msg["id"].(float64)
		switch method {
		case "initialize":
			result := map[string]any{}
			if os.Getenv("PARMESAN_TEST_ACP_NO_MCP_CAPS") != "1" {
				result["agentCapabilities"] = map[string]any{
					"mcpCapabilities": map[string]any{
						"http": true,
						"sse":  true,
					},
				}
			}
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  result,
			})
		case "session/new":
			params, _ := msg["params"].(map[string]any)
			mcpServers, hasMCPServers := params["mcpServers"].([]any)
			expectedMCP := 2
			if os.Getenv("PARMESAN_TEST_ACP_NO_CONFIG") == "1" {
				expectedMCP = 1
			}
			if os.Getenv("PARMESAN_TEST_ACP_EXPECT_EMPTY_MCP") == "1" {
				if !hasMCPServers || len(mcpServers) != 0 {
					panic("expected empty mcpServers array")
				}
			} else if len(mcpServers) != expectedMCP {
				panic("expected two MCP servers")
			}
			if os.Getenv("PARMESAN_TEST_ACP_VALIDATE_HTTP") == "1" && hasMCPServers {
				server, _ := mcpServers[0].(map[string]any)
				if server["type"] != "http" {
					panic("expected http MCP server type")
				}
				headers, _ := server["headers"].(map[string]any)
				if headers == nil || headers["Authorization"] != "Bearer http-secret" {
					panic("expected object-shaped http headers")
				}
			}
			sessionID := params["sessionId"]
			if returned := os.Getenv("PARMESAN_TEST_ACP_RET_SESSION_ID"); returned != "" {
				sessionID = returned
			}
			result := map[string]any{"sessionId": sessionID}
			if os.Getenv("PARMESAN_TEST_ACP_NO_CONFIG") != "1" {
				result["configOptions"] = []map[string]any{
					{
						"configId": "model",
						"category": "model",
						"options": []map[string]any{
							{"value": "anthropic/claude-3.7-sonnet", "label": "anthropic/claude-3.7-sonnet"},
						},
					},
				}
			}
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  result,
			})
		case "session/set_config_option":
			if os.Getenv("PARMESAN_TEST_ACP_NO_CONFIG") == "1" {
				panic("session/set_config_option should not be called when peer does not advertise config options")
			}
			params, _ := msg["params"].(map[string]any)
			if params["configId"] != "model" || params["value"] != "anthropic/claude-3.7-sonnet" {
				panic("unexpected model config payload")
			}
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  map[string]any{"ok": true},
			})
		case "session/prompt":
			params, _ := msg["params"].(map[string]any)
			sessionID, _ := params["sessionId"].(string)
			prompt, _ := params["prompt"].([]any)
			if len(prompt) != 1 {
				panic("expected one prompt block")
			}
			block, _ := prompt[0].(map[string]any)
			text, _ := block["text"].(string)
			expectedPrompt := "Prefix instruction.\n\nhelp with this task\n\nSuffix instruction."
			if os.Getenv("PARMESAN_TEST_ACP_NO_CONFIG") == "1" {
				expectedPrompt = "help with this task"
			}
			if text != expectedPrompt {
				panic("unexpected delegated prompt text")
			}
			result := map[string]any{"accepted": true}
			if os.Getenv("PARMESAN_TEST_ACP_PROMPT_COMPLETES") == "1" {
				result["stopReason"] = "end_turn"
			}
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  result,
			})
			if os.Getenv("PARMESAN_TEST_ACP_UPDATE_STYLE") == "opencode" {
				text := "Delegated answer"
				if os.Getenv("PARMESAN_TEST_ACP_STREAM_CHUNKS") == "1" {
					text = "Delegated answer "
				}
				writeHelperJSON(writer, map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sessionID,
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"messageId":     "msg_remote",
							"content": map[string]any{
								"type": "text",
								"text": text,
							},
						},
					},
				})
				if os.Getenv("PARMESAN_TEST_ACP_STREAM_CHUNKS") == "1" {
					if delayMS := os.Getenv("PARMESAN_TEST_ACP_DELAY_FINAL_CHUNK_MS"); delayMS != "" {
						if parsed, err := strconv.Atoi(delayMS); err == nil && parsed > 0 {
							time.Sleep(time.Duration(parsed) * time.Millisecond)
						}
					}
					writeHelperJSON(writer, map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"sessionId": sessionID,
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"messageId":     "msg_remote",
								"content": map[string]any{
									"type": "text",
									"text": "continued",
								},
							},
						},
					})
					writeHelperJSON(writer, map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"sessionId": sessionID,
							"update": map[string]any{
								"sessionUpdate": "agent_turn_complete",
							},
						},
					})
				} else if os.Getenv("PARMESAN_TEST_ACP_SKIP_TURN_COMPLETE") != "1" {
					writeHelperJSON(writer, map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"sessionId": sessionID,
							"update": map[string]any{
								"sessionUpdate": "agent_turn_complete",
							},
						},
					})
				}
				break
			}
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"type": "agent_message_chunk",
						"text": "Delegated answer",
					},
				},
			})
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"type": "agent_turn_complete",
					},
				},
			})
		default:
			if hasID {
				writeHelperJSON(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      int(id),
					"error": map[string]any{
						"code":    -32601,
						"message": "unsupported",
					},
				})
			}
		}
	}
}

func writeHelperJSON(writer *bufio.Writer, value map[string]any) {
	raw, _ := json.Marshal(value)
	_, _ = writer.Write(append(raw, '\n'))
	_ = writer.Flush()
}
