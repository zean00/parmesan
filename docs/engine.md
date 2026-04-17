# Engine

This document describes how the live runtime engine behaves.

## What This Page Covers

Use this page when you need to understand:

- what happens after an ACP message arrives
- where executions and traces come from
- when policy, retrieval, tools, approvals, and delegated agents are involved
- what the runtime intentionally does not do

If you need the simpler mental model for `session` vs `turn execution` vs
`worker`, start with [Execution Model](./execution-model.md).

```mermaid
flowchart TD
    Ingress["ACP Message Ingress"]
    Manual{"Manual Session?"}
    Persist["Persist Customer Event"]
    Exec["Create / Coalesce Execution"]
    Policy["Resolve Policy"]
    Retrieve["Run Retrieval"]
    Decide["Select Tool / Agent / Response Path"]
    Compose["Compose Response"]
    Deliver["Deliver Events"]
    Trace["Persist Trace / Audit / Runs"]

    Ingress --> Persist --> Manual
    Manual -- Yes --> Trace
    Manual -- No --> Exec --> Policy --> Retrieve --> Decide --> Compose --> Deliver --> Trace
```

## Turn Lifecycle

The runtime is easier to understand if you read the turn in seven stages:

### 1. Ingress

ACP is the primary conversation edge. A customer message enters through:

- `POST /v1/acp/sessions/{id}/messages`
- or the agent-scoped ACP equivalent

If the session is in manual mode, the message is persisted but no automated
execution is started.

That distinction matters operationally: manual mode changes execution behavior
without losing the durable session history.

### 2. Execution Creation

Parmesan creates or coalesces a durable execution. Coalescing is controlled by
`acp.response_coalesce_ms`.

Every execution gets:

- execution id
- trace id
- persisted execution steps
- resumable status

Executions are durable first-class records, not transient internal objects.

### 3. Policy Resolution

The runtime resolves the effective policy for the turn:

- guidelines
- journeys
- templates
- capability isolation
- allowed tools
- allowed delegated agents
- retrieval scopes

This stage decides the behavioral envelope for the turn before generation or
tool use proceeds.

### 4. Retrieval

Retrieval is response-scoped grounding. It uses compiled knowledge snapshots and
does not directly mutate active policy or knowledge state.

Retrieval improves the turn. It is not itself a learning operation.

### 5. Tool / Agent Selection

The runtime may stage:

- tools
- approvals
- delegated ACP peer agents

Capability exposure is controlled by policy. Discovery is not exposure.

That means global catalogs can be large while each agent still operates inside
an explicit behavioral boundary.

### 6. Response Composition

Responses may be:

- strict template outputs
- generated outputs
- multiple ordered messages when the policy/template requires them

Templates, tool output, and generation are all part of the same response path,
but policy determines which one wins.

### 7. Delivery And Tracing

The engine persists:

- session events
- audit records
- response records
- response trace spans
- tool runs
- delivery attempts

This is what powers replay, debugging, and operator trace inspection.

If an operator needs to understand why a reply happened, these durable records
are the evidence trail.

## Durability Model

Executions are durable and operator-recoverable. Operators can:

- retry
- retry with a configured fallback model profile
- unblock
- abandon
- take over the session

This is a core design choice: runtime state is not treated as ephemeral best
effort state.

That design is what makes retries, operator recovery, and trace inspection
work reliably.

### Retryable Dependency Failures

Durability also applies to downstream dependency outages during a turn.

If a retryable MCP or tool call fails while a step is running:

- the error is written onto the durable execution step
- the execution can move into `waiting`
- the same execution id can resume later on a new attempt
- the runtime does not need to create a replacement turn to recover

This was validated against the live Orbyte + Nexus stack by stopping
`orbyte_full` during the product flow, observing retryable MCP failures in
`compose_response`, restarting Orbyte, and then watching the same execution
complete successfully on a later attempt.

This is different from graceful fail-soft behavior. Some delegated flows may
choose to degrade gracefully instead of leaving the execution in a retryable
blocked state. That is integration behavior layered on top of the same durable
engine.

## Runtime Constraints

The engine is intentionally constrained:

- policies are explicit
- customer preferences are not policy overrides
- retrieval is not learning
- runtime turns do not silently mutate active policy
- only prompt-safe customer fields enter the runtime prompt

These are product constraints, not implementation accidents.

## External Capability Model

Parmesan supports:

- MCP-backed tools
- external ACP peer agents

Peer agents compete as capabilities inside policy selection; they are not an
implicit orchestration layer outside policy.

Practical implication:

- a delegated ACP peer is one possible policy-selected capability
- it is not a hidden planner/executor layer running outside policy governance

## Delegation Contracts In The Engine

Delegation and verification are now separate runtime concerns.

The engine can:

1. delegate a turn to an ACP peer
2. receive structured delegated output
3. match that output to a policy-defined `delegation_contract`
4. verify the delegated resource through configured tools
5. create a watch only after verification succeeds

So a delegated peer is not automatically trusted just because it returned a
plausible answer. The engine can require confirmation before it treats the
result as a durable resource for follow-up behavior.

This is especially important for flows such as:

- complaint or support tickets
- orders or shipments
- approvals
- external jobs

Read [Delegation Contracts](./delegation-contracts.md) for the exact contract
model.

## Extraction Surfaces

There are now three different kinds of extraction in the engine, and they
should not be confused with each other.

### 1. Learning Extraction

This is post-turn extraction that produces durable learning artifacts such as:

- `preferred_name`
- `contact_channel`

It feeds the learning system and customer preferences. It does not directly run
tools or verify delegated resources.

### 2. Tool-Argument Extraction

This is runtime argument inference used to help populate tool inputs from the
current turn context.

Examples:

- inferring a product name for a product lookup tool
- inferring a customer name for a CRM follow-up tool

This happens during planning/runtime execution and is adapter-aware through the
tool argument resolver boundary.

### 3. Delegation-Contract Extraction

This is post-delegation extraction that maps delegated result fields and
verification output into a canonical resource shape such as:

- `resource.id`
- `resource.display_id`
- `resource.status`

This drives delegated verification and watch creation, not general learning or
tool argument inference.

The distinction matters:

- learning extraction creates durable memory/proposals
- tool-argument extraction helps run tools
- delegation-contract extraction helps verify delegated resources

```mermaid
flowchart LR
    Policy["Resolved Policy"]
    Tool["Tool Capability"]
    Peer["ACP Peer Agent"]
    Template["Strict Template"]
    Generation["Generated Response"]

    Policy --> Tool
    Policy --> Peer
    Policy --> Template
    Policy --> Generation
```

## Implementation References

- turn ingress and ACP message handling: `internal/api/http/server.go`
- execution creation and turn enqueueing: `internal/api/http/server.go`
- runner orchestration: `internal/engine/runner/runner.go`
- policy stages: `internal/engine/policy/`
- tool invocation: `internal/toolruntime/invoker.go`
- response rendering: `internal/engine/runner/render.go`
- moderation path: `internal/moderation/moderation.go`
