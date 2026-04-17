package policyyaml

import "testing"

func TestParseBundle(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: greet
    when: customer says hello
    then: greet them back
    mcp:
      server: crm
      tool: create_contact
journeys:
  - id: flow_1
    when: [customer asks for help]
    states:
      - id: lookup
        type: tool
        mcp:
          server: commerce
          tool: get_order
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}

	if bundle.ID != "bundle-1" {
		t.Fatalf("bundle ID = %q, want bundle-1", bundle.ID)
	}

	if len(bundle.Guidelines) != 1 {
		t.Fatalf("guidelines len = %d, want 1", len(bundle.Guidelines))
	}
	if len(bundle.GuidelineToolAssociations) != 2 {
		t.Fatalf("guideline tool associations = %#v, want 2 compiled associations", bundle.GuidelineToolAssociations)
	}
}

func TestParseBundleCompilesGuidelineAgentAssociations(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: orchestrate
    when: customer asks for a multi-step workflow
    then: delegate the workflow
    agents: [OpenCode]
journeys:
  - id: flow_1
    when: [customer asks for help]
    guidelines:
      - id: orchestrate_more
        when: customer needs deeper implementation
        then: delegate deeper implementation
        agents: [OpenCode]
    states:
      - id: orchestrate
        type: agent
        agent: OpenCode
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.GuidelineAgentAssociations) != 2 {
		t.Fatalf("guideline agent associations = %#v, want 2 compiled associations", bundle.GuidelineAgentAssociations)
	}
	if bundle.GuidelineAgentAssociations[0].AgentID != "OpenCode" {
		t.Fatalf("guideline agent associations = %#v, want OpenCode target", bundle.GuidelineAgentAssociations)
	}
	if bundle.Journeys[0].States[0].Agent != "OpenCode" {
		t.Fatalf("journey state agent = %q, want OpenCode", bundle.Journeys[0].States[0].Agent)
	}
}

func TestParseBundleCompilesGuidelineAgentWorkflowAssociations(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
delegation_workflows:
  - id: wf_ticket
    goal: create and verify a complaint ticket
    steps:
      - id: resolve
        instruction: resolve the customer
        tool_ids: [orbyte_full.crm.customer.summary]
guidelines:
  - id: orchestrate
    when: customer asks for a complaint workflow
    then: delegate the complaint workflow
    agent_bindings:
      - agent_id: OpenCode
        workflow_id: wf_ticket
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.GuidelineAgentAssociations) != 1 {
		t.Fatalf("guideline agent associations = %#v, want 1 compiled association", bundle.GuidelineAgentAssociations)
	}
	if bundle.GuidelineAgentAssociations[0].WorkflowID != "wf_ticket" {
		t.Fatalf("guideline agent association = %#v, want workflow binding", bundle.GuidelineAgentAssociations[0])
	}
}

func TestParseBundleRejectsUnknownGuidelineWorkflowBinding(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: orchestrate
    when: customer asks for a complaint workflow
    then: delegate the complaint workflow
    agent_bindings:
      - agent_id: OpenCode
        workflow_id: missing
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want unknown workflow_id error")
	}
}

func TestValidateBundleRejectsDuplicateIDs(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
guidelines:
  - id: dup
    when: one
    then: one
templates:
  - id: dup
    mode: strict
    text: hi
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want duplicate id error")
	}
}

func TestParseBundleAllowsTemplateMessageSequence(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
templates:
  - id: two_step_reply
    mode: strict
    messages:
      - I can help with that.
      - First, please share your order number.
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.Templates) != 1 || len(bundle.Templates[0].Messages) != 2 {
		t.Fatalf("templates = %#v, want parsed message sequence", bundle.Templates)
	}
}

func TestParseBundleSupportsGenericRuntimeSections(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
watch_capabilities:
  - id: reminder_watch
    kind: appointment_reminder
    schedule_strategy: reminder
    trigger_signals:
      - scheduling
    tool_match_terms:
      - appointment
    subject_keys:
      - appointment_id
    required_fields:
      - appointment_at
    reminder_lead_seconds: 1800
    allow_lifecycle_fallback: true
quality_profile:
  id: support_quality
  risk_tier: high
  allowed_commitments:
    - evidence-backed support guidance
lifecycle_policy:
  id: support_lifecycle
  followup_message: Need anything else before I close this support request?
capability_isolation:
  allowed_provider_ids: [commerce]
  allowed_tool_ids: [commerce_schedule_appointment]
  allowed_retriever_ids: [kb_agent]
  allowed_knowledge_scopes:
    - kind: agent
      id: agent_support
semantics:
  signals:
    - id: scheduling
      tokens: [schedule, appointment]
delegation_contracts:
  - id: reminder_ticket
    agent_ids: [OpenCode]
    resource_type: support_ticket
    result_text_field: user_message
    required_result_fields: [ticket_id, ticket_number, status]
    field_aliases:
      - target: resource.id
        sources: [ticket_id]
    verification:
      primary_tool_id: ticket.get
      primary_args:
        ticket_id: "{{resource.id}}"
      fallback_tools:
        - tool_id: ticket.search
          args:
            query: "{{result.ticket_number}}"
      extract_paths:
        - target: resource.id
          sources: [structuredContent.id]
      require_match_on: [resource.id]
    watch_capability_id: reminder_watch
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.WatchCapabilities) != 1 || bundle.WatchCapabilities[0].ID != "reminder_watch" {
		t.Fatalf("watch capabilities = %#v, want parsed capability", bundle.WatchCapabilities)
	}
	if len(bundle.DelegationContracts) != 1 || bundle.DelegationContracts[0].ID != "reminder_ticket" {
		t.Fatalf("delegation contracts = %#v, want parsed contract", bundle.DelegationContracts)
	}
	if bundle.QualityProfile.ID != "support_quality" || bundle.LifecyclePolicy.ID != "support_lifecycle" {
		t.Fatalf("quality/lifecycle = %#v / %#v, want parsed profile and lifecycle policy", bundle.QualityProfile, bundle.LifecyclePolicy)
	}
	if len(bundle.CapabilityIsolation.AllowedProviderIDs) != 1 ||
		bundle.CapabilityIsolation.AllowedProviderIDs[0] != "commerce" ||
		len(bundle.CapabilityIsolation.AllowedKnowledgeScopes) != 1 ||
		bundle.CapabilityIsolation.AllowedKnowledgeScopes[0].ID != "agent_support" {
		t.Fatalf("capability isolation = %#v, want parsed allowlists", bundle.CapabilityIsolation)
	}
	if len(bundle.Semantics.Signals) != 1 || bundle.Semantics.Signals[0].ID != "scheduling" {
		t.Fatalf("semantics = %#v, want parsed semantics", bundle.Semantics)
	}
}

func TestParseBundleRejectsUnknownDelegationWatchCapability(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
delegation_contracts:
  - id: complaint_ticket
    agent_ids: [OpenCode]
    resource_type: support_ticket
    watch_capability_id: missing_watch
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want unknown watch capability error")
	}
}

func TestParseBundleNormalizesJourneyRootAndEdges(t *testing.T) {
	raw := []byte(`
id: bundle-1
version: v1
journeys:
  - id: flow_1
    when: [customer asks for help]
    states:
      - id: ask_name
        type: message
        instruction: What is your name?
        next: [ask_email]
      - id: ask_email
        type: message
        instruction: What is your email?
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.Journeys) != 1 {
		t.Fatalf("journeys len = %d, want 1", len(bundle.Journeys))
	}
	j := bundle.Journeys[0]
	if j.RootID != "ask_name" {
		t.Fatalf("journey root_id = %q, want ask_name", j.RootID)
	}
	if len(j.Edges) == 0 {
		t.Fatalf("journey edges = %#v, want compiled edges", j.Edges)
	}
	found := false
	for _, edge := range j.Edges {
		if edge.Source == "ask_name" && edge.Target == "ask_email" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("journey edges = %#v, want ask_name -> ask_email edge", j.Edges)
	}
}

func TestParseBundleSupportsDomainBoundary(t *testing.T) {
	raw := []byte(`
id: pet-store
version: v1
domain_boundary:
  mode: hard_refuse
  allowed_topics:
    - pet food
    - dog toys
  adjacent_topics:
    - pet-safe ingredients
  blocked_topics:
    - human food
    - cooking
  adjacent_action: redirect
  uncertainty_action: refuse
  out_of_scope_reply: I can help with pet-store questions, but I cannot help with cooking or human food.
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if bundle.DomainBoundary.Mode != "hard_refuse" || bundle.DomainBoundary.OutOfScopeReply == "" {
		t.Fatalf("domain boundary = %#v, want parsed boundary policy", bundle.DomainBoundary)
	}
}

func TestParseBundleRejectsInvalidDomainBoundary(t *testing.T) {
	raw := []byte(`
id: pet-store
version: v1
domain_boundary:
  mode: hard_refuse
  blocked_topics:
    - cooking
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want missing out_of_scope_reply error")
	}
}

func TestParseBundleRejectsBroadConciergeBlockedTopicsWithoutReply(t *testing.T) {
	raw := []byte(`
id: pet-store
version: v1
domain_boundary:
  mode: broad_concierge
  allowed_topics:
    - pet food
  blocked_topics:
    - human food
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want missing out_of_scope_reply error for blocked topics")
	}
}

func TestParseBundleSupportsResponseCapabilities(t *testing.T) {
	raw := []byte(`
id: response-capability-bundle
version: v1
response_capabilities:
  - id: product_response
    mode: always
    facts:
      - key: product_name
        required: true
        sources:
          - tool_id: orbyte_full.commercial_core.item.get
            path: structuredContent.name
      - key: lead_number
        sources:
          - tool_id: orbyte_full.crm.lead.find_or_create_for_product_interest
            path: structuredContent.lead.values.lead_number
    instructions:
      - Use only the provided facts.
    examples:
      - facts:
          product_name: Espresso Double
        messages:
          - Espresso Double is available.
    deterministic_fallback:
      messages:
        - text: "{{facts.product_name}} is available."
          when_present: [product_name]
guidelines:
  - id: product_lookup
    when: customer asks for product information
    then: answer with the tool-backed product details
    response_capability_id: product_response
`)

	bundle, err := ParseBundle(raw)
	if err != nil {
		t.Fatalf("ParseBundle() error = %v", err)
	}
	if len(bundle.ResponseCapabilities) != 1 {
		t.Fatalf("response capabilities len = %d, want 1", len(bundle.ResponseCapabilities))
	}
	if bundle.Guidelines[0].ResponseCapabilityID != "product_response" {
		t.Fatalf("guideline response_capability_id = %q, want product_response", bundle.Guidelines[0].ResponseCapabilityID)
	}
}

func TestParseBundleRejectsUnknownResponseCapabilityReference(t *testing.T) {
	raw := []byte(`
id: response-capability-bundle
version: v1
guidelines:
  - id: product_lookup
    when: customer asks for product information
    then: answer with the tool-backed product details
    response_capability_id: missing_capability
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want unknown response capability error")
	}
}

func TestParseBundleRejectsInvalidResponseCapabilityFallbackTemplate(t *testing.T) {
	raw := []byte(`
id: response-capability-bundle
version: v1
response_capabilities:
  - id: product_response
    facts:
      - key: product_name
        sources:
          - tool_id: orbyte_full.commercial_core.item.get
            path: structuredContent.name
    deterministic_fallback:
      messages:
        - text: "{{tool.orbyte_full.commercial_core.item.get.structuredContent.name}}"
`)

	if _, err := ParseBundle(raw); err == nil {
		t.Fatal("ParseBundle() error = nil, want unsupported template ref error")
	}
}
