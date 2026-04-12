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
- style / SOUL guidance
- tool exposure
- delegated-agent exposure
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
| `journeys` | structured step-based flows |
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

`journeys`
- step-oriented policy flows when the conversation must move through a sequence

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

For the connection layer, use:

- [Configuration](./configuration.md)

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
- runtime policy matcher and stages: `internal/runtime/policy/`
- rollout selection: `internal/rollout/select.go`
- control graph materialization: `internal/controlgraph/materialize.go`
- control change handling: `internal/api/http/control_changes.go`
