# Orbyte + Nexus Full-MCP Workflow Validation

This integration pack validates the same customer-facing scenario as the
baseline `orbyte_nexus` pack, but changes the delegated complaint path:

- `Nexus` remains the webchat surface
- `Parmesan` still selects the delegated ACP capability
- `OpenCode` still executes the delegated turn
- `Orbyte full MCP` is attached to the delegated agent
- a Parmesan-owned `delegation_workflow` replaces the provider-native Orbyte
  minimal skill

## What This Variant Proves

This pack is meant to validate a different control shape:

1. policy selects a delegated agent
2. policy binds that agent to a specific workflow id
3. Parmesan renders that workflow into delegated execution context
4. the ACP peer executes over raw full-MCP tools
5. Parmesan still verifies the result and creates the watch

So this variant demonstrates cross-provider-style workflow guidance owned by
Parmesan rather than provider-local skill discovery.

## Included Assets

- `agents/orbyte_nexus_full_workflow_validation.yaml`
- `policy/orbyte_nexus_full_workflow_validation_policy.yaml`
- `config/parmesan.orbyte_nexus_full_workflow.yaml`
- `conversations/integrated_validation.json.tmpl`

## Runtime Difference From The Baseline Pack

The complaint flow does not depend on `Orbyte minimal` or an Orbyte-native
complaint skill. Instead:

- the delegated agent is `OpenCodeOrbyteFull`
- the workflow is `orbyte_full_complaint_ticket_intake`
- the contract and watch verification use `orbyte_full.crm.ticket.*`

The product path remains the same direct full-MCP flow.

The current version of this pack also uses a policy-owned
`response_capability` for the product leg. That means:

- the product tools run directly against `orbyte_full`
- Parmesan extracts normalized product and lead facts from tool output
- the final product reply is rendered through the generic response-capability
  path instead of core hardcoded Orbyte logic

## Companion Command

- live wrapper: `scripts/live_orbyte_nexus_full_workflow_validation.sh`

That wrapper reuses the baseline orchestration script but swaps in:

- this pack's config
- this pack's agent directory
- this pack's conversation template
- this pack's agent id
- full MCP as the complaint resolution surface for validation
