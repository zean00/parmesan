# Policies

Policies are the main behavioral control layer in Parmesan.

## What Policies Define

Policy bundles can define:

- guidelines
- journeys
- templates
- style / SOUL guidance
- tool exposure
- delegated-agent exposure
- capability isolation

## Authoring Model

Policies are authored in YAML and compiled into typed records. YAML is an
authoring format, not the hot-path runtime representation.

Key example:

- `examples/live_support_policy.yaml`

## Runtime Role Of Policies

Policies determine:

- what the agent is allowed to do
- what the agent must do in a given context
- when tools are visible
- when approvals are required
- how tone and response style should be shaped

## Capability Exposure

A registered tool, MCP provider, or ACP peer agent is not automatically
available to the runtime.

Policy must expose it first.

This is a deliberate safety boundary:

- discovery populates the catalog
- policy controls exposure

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

## Rollouts And Governance

Policy changes are governed through rollout primitives rather than silent
in-place mutation. Draft policy changes from feedback or learning become review
artifacts first.

## Related Surfaces

- operator composed-state endpoints
- control-state views in the dashboard
- proposal and rollout endpoints

## Implementation References

- YAML policy compiler: `internal/policyyaml/compiler.go`
- runtime policy matcher and stages: `internal/runtime/policy/`
- rollout selection: `internal/rollout/select.go`
- control graph materialization: `internal/controlgraph/materialize.go`
- control change handling: `internal/api/http/control_changes.go`
