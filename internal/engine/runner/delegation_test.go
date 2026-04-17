package runner

import (
	"testing"
	"time"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func TestDelegatedAgentOutputUsesStructuredEnvelope(t *testing.T) {
	output := delegatedAgentOutput(`{"user_message":"I created ticket CRM-TKT-42 and will keep you updated.","ticket_id":"ticket_42","ticket_number":"CRM-TKT-42","queue_code":"CRM-SUPPORT","status":"open"}`)
	if got := output["result_text"]; got != "I created ticket CRM-TKT-42 and will keep you updated." {
		t.Fatalf("result_text = %#v, want structured user_message", got)
	}
	if got := output["ticket_id"]; got != "ticket_42" {
		t.Fatalf("ticket_id = %#v, want ticket_42", got)
	}
	if got := output["ticket_number"]; got != "CRM-TKT-42" {
		t.Fatalf("ticket_number = %#v, want CRM-TKT-42", got)
	}
	result, _ := output["result"].(map[string]any)
	if result == nil || result["queue_code"] != "CRM-SUPPORT" {
		t.Fatalf("result = %#v, want parsed envelope fields", result)
	}
}

func TestDelegatedApplyContractBackfillsAliases(t *testing.T) {
	contract := policy.DelegationContract{
		ID:              "complaint_ticket",
		AgentIDs:        []string{"OpenCodeOrbyteMinimal"},
		ResourceType:    "support_ticket",
		ResultTextField: "user_message",
		FieldAliases: []policy.DelegationFieldAlias{
			{Target: "resource.id", Sources: []string{"ticket_id"}},
			{Target: "resource.display_id", Sources: []string{"ticket_number"}},
			{Target: "resource.status", Sources: []string{"status"}},
			{Target: "resource.attributes.queue_code", Sources: []string{"queue_code"}},
		},
	}
	fields := delegatedAgentOutput(`{"user_message":"Ticket opened.","ticket_id":"ticket_42","ticket_number":"CRM-TKT-42","queue_code":"CRM-SUPPORT","status":"open"}`)
	delegatedApplyContract(contract, fields)
	resource, _ := fields["resource"].(map[string]any)
	if got := delegatedLookupString(resource, "type"); got != "support_ticket" {
		t.Fatalf("resource.type = %q, want support_ticket", got)
	}
	if got := delegatedLookupString(resource, "display_id"); got != "CRM-TKT-42" {
		t.Fatalf("resource.display_id = %q, want CRM-TKT-42", got)
	}
	if got := delegatedLookupString(resource, "attributes.queue_code"); got != "CRM-SUPPORT" {
		t.Fatalf("resource.attributes.queue_code = %q, want CRM-SUPPORT", got)
	}
	if got := delegatedLookupString(fields, "ticket_id"); got != "ticket_42" {
		t.Fatalf("ticket_id = %q, want ticket_42", got)
	}
	if got := delegatedLookupString(fields, "result.ticket_number"); got != "CRM-TKT-42" {
		t.Fatalf("result.ticket_number = %q, want CRM-TKT-42", got)
	}
	if got := delegatedLookupString(fields, "result_text"); got != "Ticket opened." {
		t.Fatalf("result_text = %q, want Ticket opened.", got)
	}
}

func TestDelegatedAgentWatchIntentBuildsContractWatch(t *testing.T) {
	now := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	view := resolvedView{
		Bundle: &policy.Bundle{
			DelegationContracts: []policy.DelegationContract{{
				ID:                "complaint_ticket",
				AgentIDs:          []string{"OpenCodeOrbyteMinimal"},
				ResourceType:      "support_ticket",
				WatchCapabilityID: "orbyte_minimal.crm.ticket.get",
				FieldAliases: []policy.DelegationFieldAlias{
					{Target: "resource.id", Sources: []string{"ticket_id"}},
					{Target: "resource.display_id", Sources: []string{"ticket_number"}},
					{Target: "resource.status", Sources: []string{"status"}},
				},
			}},
		},
		WatchCapabilities: []policy.WatchCapability{{
			ID:                  "orbyte_minimal.crm.ticket.get",
			Kind:                "crm_ticket_status",
			ScheduleStrategy:    "poll",
			SubjectKeys:         []string{"ticket_id"},
			StatusKeys:          []string{"status"},
			PollIntervalSeconds: 30,
			StopValues:          []string{"resolved", "closed"},
			DeliveryTemplate:    "I have an update on ticket {{ticket_number}}: status is now {{status}}.",
		}},
	}
	delegated := map[string]any{
		"server_id":   "OpenCodeOrbyteMinimal",
		"status":      "completed",
		"result_text": "I created ticket CRM-TKT-42.",
		"result": map[string]any{
			"ticket_id":     "ticket_42",
			"ticket_number": "CRM-TKT-42",
			"status":        "open",
		},
	}
	contract, ok := delegatedAgentContract(view, delegated)
	if !ok {
		t.Fatal("expected delegated contract")
	}
	delegatedApplyContract(contract, delegated)
	intent, ok := delegatedAgentWatchIntent(view, map[string]any{"delegated_agent": delegated}, now)
	if !ok {
		t.Fatal("expected delegated watch intent")
	}
	if intent.Kind != "crm_ticket_status" {
		t.Fatalf("intent.Kind = %q, want crm_ticket_status", intent.Kind)
	}
	if intent.ToolID != "orbyte_minimal.crm.ticket.get" {
		t.Fatalf("intent.ToolID = %q, want orbyte_minimal.crm.ticket.get", intent.ToolID)
	}
	if intent.SubjectRef != "ticket_42" {
		t.Fatalf("intent.SubjectRef = %q, want ticket_42", intent.SubjectRef)
	}
	if got := intent.Arguments["ticket_number"]; got != "CRM-TKT-42" {
		t.Fatalf("intent.Arguments[ticket_number] = %#v, want CRM-TKT-42", got)
	}
	if got := intent.Arguments["resource.display_id"]; got != "CRM-TKT-42" {
		t.Fatalf("intent.Arguments[resource.display_id] = %#v, want CRM-TKT-42", got)
	}
	if intent.PollInterval != 30*time.Second {
		t.Fatalf("intent.PollInterval = %s, want 30s", intent.PollInterval)
	}
}

func TestDelegatedAgentWatchIntentAcceptsVerifiedLifecycleStatus(t *testing.T) {
	now := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	view := resolvedView{
		Bundle: &policy.Bundle{
			DelegationContracts: []policy.DelegationContract{{
				ID:                "complaint_ticket",
				AgentIDs:          []string{"OpenCodeOrbyteMinimal"},
				ResourceType:      "support_ticket",
				WatchCapabilityID: "orbyte_minimal.crm.ticket.get",
				FieldAliases: []policy.DelegationFieldAlias{
					{Target: "resource.id", Sources: []string{"ticket_id"}},
					{Target: "resource.display_id", Sources: []string{"ticket_number"}},
					{Target: "resource.status", Sources: []string{"status"}},
				},
			}},
		},
		WatchCapabilities: []policy.WatchCapability{{
			ID:                  "orbyte_minimal.crm.ticket.get",
			Kind:                "crm_ticket_status",
			ScheduleStrategy:    "poll",
			SubjectKeys:         []string{"ticket_id"},
			StatusKeys:          []string{"status"},
			PollIntervalSeconds: 30,
			StopValues:          []string{"resolved", "closed"},
			DeliveryTemplate:    "I have an update on ticket {{ticket_number}}: status is now {{status}}.",
		}},
	}
	delegated := map[string]any{
		"server_id":    "OpenCodeOrbyteMinimal",
		"status":       "new",
		"result_text":  "Your complaint ticket CRM-TKT-42 has been opened.",
		"verification": map[string]any{"verified": true},
		"result": map[string]any{
			"ticket_id":     "ticket_42",
			"ticket_number": "CRM-TKT-42",
			"status":        "new",
		},
	}
	contract, ok := delegatedAgentContract(view, delegated)
	if !ok {
		t.Fatal("expected delegated contract")
	}
	delegatedApplyContract(contract, delegated)
	intent, ok := delegatedAgentWatchIntent(view, map[string]any{"delegated_agent": delegated}, now)
	if !ok {
		t.Fatal("expected delegated watch intent")
	}
	if intent.SubjectRef != "ticket_42" {
		t.Fatalf("intent.SubjectRef = %q, want ticket_42", intent.SubjectRef)
	}
}

func TestDelegatedVerifiedResourceIgnoresJSONRPCErrorEnvelope(t *testing.T) {
	contract := policy.DelegationContract{
		ID:           "complaint_ticket",
		ResourceType: "support_ticket",
		Verification: policy.DelegationVerification{
			ExtractPaths: []policy.DelegationFieldAlias{
				{Target: "resource.id", Sources: []string{"structuredContent.id", "id"}},
				{Target: "resource.display_id", Sources: []string{"structuredContent.values.ticket_number", "ticket_number"}},
			},
		},
	}
	output := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"error": map[string]any{
			"code":    -32602,
			"message": "not_found_error: record not found",
		},
	}
	if got := delegatedVerifiedResource(contract, output); len(got) != 0 {
		t.Fatalf("delegatedVerifiedResource() = %#v, want nil for error envelope", got)
	}
}
