# Policies

Policies are the main behavioral control layer in Parmesan.

## At A Glance

Policies answer one question: given everything the runtime knows right now,
what is this agent allowed or required to do next.

Use this document when you are:

- authoring or reviewing a policy bundle
- exposing tools, MCP providers, or delegated agents
- changing response style or domain boundaries
- tracing why an agent took a specific path

## What Policies Define

Policy bundles can define:

- guidelines
- journeys
- templates
- response capabilities
- style / SOUL guidance
- tool exposure
- delegated-agent exposure
- delegation workflows
- delegation contracts
- capability isolation

In practice, a policy bundle is the place where you turn runtime inventory into
allowed behavior. Registered tools, MCP providers, or ACP peer agents are not
usable until policy exposes them.

## Quick Mental Model

| Section | Purpose |
| --- | --- |
| `soul` | voice, role, identity, style guardrails |
| `domain_boundary` | what the agent should stay inside or redirect away from |
| `guidelines` | conditional behavior rules |
| `templates` | deterministic direct replies |
| `response_capabilities` | tool-backed fact extraction plus example-guided response rendering |
| `journeys` | structured step-based flows |
| `delegation_workflows` | workflow briefs attached to delegated ACP turns |
| capability exposure | which tools, MCP servers, or delegated agents are even eligible |

## Authoring Model

Policies are authored in YAML and compiled into typed records. YAML is an
authoring format, not the hot-path runtime representation.

Key example:

- `examples/live_support_policy.yaml`

## Minimal Bundle Shape

```yaml
id: bundle_live_support_v2
version: v2
composition_mode: strict
no_match: I can help with customer support questions about orders, shipping, returns, refunds, and account help. Please tell me the support issue.
domain_boundary:
  mode: soft_redirect
  allowed_topics:
    - orders
    - shipping
soul:
  identity: Parmesan Support Agent
  role: Customer support assistant
guidelines:
  - id: truth_missing_details
    when: customer asks for information that is not present in the conversation or retrieved context
    then: say you do not have the missing detail yet and ask for the minimum detail needed to continue.
templates:
  - id: generic_support_open
    mode: strict
    when: customer asks for customer support help
    text: Hi, I can help with that. Please tell me the issue and any relevant order or account detail.
```

The stock example includes all of these shapes in a real bundle:

- `examples/live_support_policy.yaml`

## Runtime Role Of Policies

Policies determine:

- what the agent is allowed to do
- what the agent must do in a given context
- when tools are visible
- when approvals are required
- how tone and response style should be shaped

Put differently:

- config tells Parmesan what is available
- policy tells Parmesan what this agent may actually do with it

## Main Sections

`composition_mode`
- controls how strictly the bundle should drive the turn

`no_match`
- default fallback when no more specific policy path matches

`domain_boundary`
- defines the allowed topic boundary and redirect behavior

`soul`
- response identity and style constraints

`guidelines`
- conditional behavioral rules phrased as `when` and `then`

`templates`
- direct response paths for common cases, especially strict clarifications or
  deterministic replies

`response_capabilities`
- tool-backed response rendering that extracts normalized facts from tool
  outputs and can use examples plus deterministic fact fallback

`journeys`
- step-oriented policy flows when the conversation must move through a sequence

`delegation_workflows`
- structured execution briefs that Parmesan can attach to delegated ACP turns
  after policy already chose the agent

`delegation_contracts`
- post-delegation verification rules that turn delegated structured output into
  a verified resource and optional runtime watch

## Annotated Stock Example

In the current `live_support` sample:

- `domain_boundary.allowed_topics` constrains the agent to support topics
- `soul` keeps the tone concise and professional
- `guidelines` enforce truthfulness and scope redirect behavior
- `templates` provide deterministic clarification messages for tracking,
  refunds, and account help

That bundle is intentionally simple. It is a good starting point because it
shows the control model without burying it under a large workflow graph.

## Capability Exposure

A registered tool, MCP provider, or ACP peer agent is not automatically
available to the runtime.

Policy must expose it first.

This is a deliberate safety boundary:

- discovery populates the catalog
- policy controls exposure

Two important implications:

1. global runtime config tells Parmesan what exists
2. policy decides what the current agent may actually use

This separation is one of the main reasons the runtime stays governable as it
gains more tools and external peers.

## Delegated Agents And Tools

Parmesan currently chooses one capability kind for a turn. That means a
delegated ACP peer agent competes with tools rather than acting as an implicit
sub-workflow behind the scenes.

Typical pattern:

- configure a peer under `agent_servers` in the global config
- expose it from a guideline or journey node in policy
- optionally constrain it further with `capability_isolation`

Authoring rule:

- define the peer and its invocation defaults in global config
- expose or require the peer in policy
- do not duplicate connection details inside the bundle

The policy bundle still references delegated agents by string id only. External
ACP invocation defaults such as delegated model selection, delegated MCP
servers, and delegated prompt prefix/suffix are configured on the corresponding
`agent_servers.<id>` entry rather than inline in the policy bundle.

## Delegation Workflows

`delegation_workflows` are policy-owned execution briefs for delegated ACP
turns.

Use them when:

- policy already chose the delegated agent
- the delegated peer should not also discover which workflow to use
- the delegated turn needs ordered tool-use guidance across one or more MCP
  providers

The important distinction is:

- policy decides **whether** to delegate and **which agent** to use
- a delegation workflow tells that selected agent **how** to execute
- a delegation contract still tells Parmesan **what verified result must come
  back**

Typical workflow contents:

- workflow id and title
- goal
- ordered steps
- provider-qualified tool ids per step
- constraints
- success criteria

Guidelines can bind an agent directly to a workflow through
`agent_bindings`:

```yaml
guidelines:
  - id: complaint_delegate
    when: customer reports a damaged product complaint
    then: delegate the complaint intake workflow
    agent_bindings:
      - agent_id: OpenCodeOrbyteFull
        workflow_id: orbyte_full_complaint_ticket_intake
```

That means the delegated agent does not spend tokens choosing a workflow. The
workflow is attached by Parmesan as execution context during delegation.

## Response Capabilities

`response_capabilities` are the policy-owned response layer for tool-backed
direct replies.

They are designed for cases where:

- tools succeeded
- the answer should stay grounded in tool facts
- policy wants better authoring ergonomics than raw path-heavy templates
- no-model or stubbed environments still need a deterministic fallback

Each response capability combines three things:

1. a structured fact contract
2. sample-based response examples
3. a deterministic fact-key fallback

The fact contract maps tool output into a flat fact bag:

```yaml
response_capabilities:
  - id: product_lookup_and_lead_response
    mode: always
    facts:
      - key: product_name
        required: true
        sources:
          - tool_id: orbyte_full.commercial_core.item.get
            path: structuredContent.name
```

The sample-based response layer then gives the model:

- the current customer message
- normalized facts
- response instructions
- a few grounded examples

The deterministic fallback is limited to declared fact keys only:

```yaml
deterministic_fallback:
  messages:
    - text: "{{facts.product_name}} is available."
      when_present: [product_name]
```

This keeps core runtime generic:

- the engine knows how to extract facts and render examples
- policy owns the use-case-specific fact mapping and wording examples

Guidelines and journey states can both select a response capability:

```yaml
guidelines:
  - id: product_lookup_and_lead
    when: customer asks for product information
    then: retrieve the product details and follow up
    response_capability_id: product_lookup_and_lead_response
```

Resolution precedence is:

1. active journey state `response_capability_id`
2. first distinct matched guideline `response_capability_id`
3. none

Current v1 scope:

- tool-backed direct responses only
- not delegated-agent result text
- no advanced transforms beyond first-non-empty source resolution and
  `when_present`

## Delegation Contracts

`delegation_contracts` are the generic policy mechanism for post-delegation
verification.

They let Parmesan:

- accept structured result data from a delegated agent
- verify that result against real external tool output
- normalize it into a canonical resource shape
- create a watch only after the resource is verified

That means the engine does not need domain-specific logic such as “support
ticket verification” built into core runtime.

Typical contract responsibilities:

- require result fields such as ids or status
- map those fields into `resource.id`, `resource.display_id`, and
  `resource.status`
- call a verification tool and optional fallbacks
- bind the verified resource to a watch capability
- define the failure message if verification does not succeed

This is a generic engine feature. A complaint-ticket contract is just one
integration-specific instance of that mechanism.

Read [Delegation Contracts](./delegation-contracts.md) for the full model.

For the connection layer, use:

- [Configuration](./configuration.md)
- [Delegation Contracts](./delegation-contracts.md)

## SOUL And Style

Policy bundles can include SOUL-like guidance for:

- identity
- tone
- language
- formatting
- escalation posture

SOUL influences style, but it does not override:

- hard policy
- strict templates
- compliance/safety rules
- approval requirements

Treat SOUL as style and posture, not as a substitute for business logic.

## Rollouts And Governance

Policy changes are governed through rollout primitives rather than silent
in-place mutation. Draft policy changes from feedback or learning become review
artifacts first.

This means:

- customer turns read the active compiled policy state
- learning does not silently rewrite production behavior
- operator review remains the promotion gate for policy-oriented changes

That governance boundary is intentional. Parmesan is designed so feedback can
propose policy change without silently mutating production behavior.

## Related Surfaces

- operator composed-state endpoints
- control-state views in the dashboard
- proposal and rollout endpoints
- [Configuration](./configuration.md)

## Implementation References

- YAML policy compiler: `internal/policyyaml/compiler.go`
- runtime policy matcher and stages: `internal/engine/policy/`
- rollout selection: `internal/rollout/select.go`
- control graph materialization: `internal/controlgraph/materialize.go`
- control change handling: `internal/api/http/control_changes.go`
