package acppeer

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/config"
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
			mcpServers, _ := params["mcpServers"].([]any)
			expectedMCP := 2
			if os.Getenv("PARMESAN_TEST_ACP_NO_CONFIG") == "1" {
				expectedMCP = 1
			}
			if len(mcpServers) != expectedMCP {
				panic("expected two MCP servers")
			}
			result := map[string]any{"sessionId": params["sessionId"]}
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
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  map[string]any{"accepted": true},
			})
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
