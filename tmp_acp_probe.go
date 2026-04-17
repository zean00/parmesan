package main

import (
  "context"
  "fmt"
  "os"
  "time"

  "github.com/sahal/parmesan/internal/acppeer"
  "github.com/sahal/parmesan/internal/config"
)

func main() {
  mgr := acppeer.NewManager(map[string]config.AgentServerConfig{
    "OpenCodeOrbyteMinimal": {
      Command: "opencode",
      Args: []string{"acp", "--pure"},
      StartupTimeoutSeconds: 20,
      RequestTimeoutSeconds: 300,
      Env: map[string]string{
        "OPENROUTER_API_KEY": os.Getenv("OPENROUTER_API_KEY"),
        "OPENROUTER_BASE_URL": "https://openrouter.ai/api/v1",
      },
      ACP: config.ACPAgentConfig{
        Model: "openrouter/anthropic/claude-haiku-4.5",
        PromptPrefix: "You are the delegated CRM complaint intake worker for the parent agent.\nUse Orbyte minimal MCP only.\nUse the exact workflow skill id crm_customer_complaint_ticket_intake as your workflow reference and do not pick any other skill.\nExecute the workflow directly with the CRM tools below. Do not spend time browsing skills or tools.\nYou must execute the required MCP tool steps needed to either:\n1. reuse an existing matching open complaint ticket, or\n2. create a new complaint ticket.\nUse the shortest successful path only.\nRequired execution order:\n1. crm.customer.summary\n2. crm.ticket.search using the resolved customer context and complaint clues\n3. if an open matching ticket already exists, use that ticket\n4. otherwise crm.ticket.create with confirm_apply=true\n5. crm.ticket.get for the final chosen ticket before you answer\nDo not call crm.ticket.assign or crm.ticket.comment.create unless ticket creation already succeeded and you still have time left. They are optional for this task.\nSafe defaults for a new cracked-product complaint:\n- source_channel: chat\n- issue_category: product_damage\n- priority: medium\n- severity: medium\n- title: concise damaged-product complaint title\nIf you create a new ticket, prefer the queue_code from an existing customer ticket returned by crm.customer.summary or crm.ticket.search. If no queue is visible, use the seeded support queue that starts with CRM-SUPPORT.\nDo not invent ticket ids or ticket numbers.\nOnly copy ticket_id and ticket_number from actual MCP tool results.\nThe final ticket_id and ticket_number must come from the last crm.ticket.get result or an explicit matched ticket from crm.ticket.search.\nIf crm.ticket.get does not return the chosen ticket, return:\n{\"user_message\":\"I could not confirm ticket creation yet.\",\"ticket_id\":\"\",\"ticket_number\":\"\",\"queue_code\":\"\",\"status\":\"failed\"}\nReturn only one JSON object with these keys:\nuser_message, ticket_id, ticket_number, queue_code, status",
        PromptSuffix: "Return only valid JSON. Do not wrap it in markdown or add commentary.",
        MCPServers: []config.ACPMCPServerConfig{{Type: "http", Name: "Orbyte Minimal", URL: "http://127.0.0.1:18111/mcp"}},
      },
    },
  })
  ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
  defer cancel()
  started := time.Now()
  res, err := mgr.Delegate(ctx, "OpenCodeOrbyteMinimal", acppeer.Request{
    SessionID: "probe_delegate",
    CWD: ".",
    Prompt: "Customer says: Hi, I'm handling purchasing for Healthy CRM Customer 20260416092051. Call me Rina. Email me updates. The Espresso Double 20260416-092051 we received arrived cracked, so please open a complaint ticket and keep me updated on the status.",
  })
  fmt.Printf("elapsed=%s\n", time.Since(started))
  fmt.Printf("result=%#v\n", res)
  fmt.Printf("err=%v\n", err)
}
