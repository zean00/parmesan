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
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  map[string]any{"ok": true},
			})
		case "session/new":
			params, _ := msg["params"].(map[string]any)
			writeHelperJSON(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      int(id),
				"result":  map[string]any{"sessionId": params["sessionId"]},
			})
		case "session/prompt":
			params, _ := msg["params"].(map[string]any)
			sessionID, _ := params["sessionId"].(string)
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
