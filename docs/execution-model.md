# Execution Model

This page explains the difference between `session`, `turn execution`,
`worker`, and `async writes`.

Use it when you understand the conversation model already, but want the correct
runtime and operations mental model.

## Short Version

- `session` = the conversation container
- `event` = one message or system event inside the session
- `turn execution` = one durable processing unit created for a customer turn
- `execution step` = one stage inside that turn execution
- `worker` = the background process that runs executions
- `async writes` = the background persistence queue used to flush many writes

So:

- `async execution` is the job model
- `worker` is the process that performs the job

They are related, but they are not the same thing.

## Session vs Turn

A `session` is the durable conversation.

Each time the customer sends a message, Parmesan persists that message as a
session event. That incoming event may create a new turn execution, or it may
be coalesced into an existing pending turn depending on the ACP coalescing
window.

Useful distinction:

- the `session` lives for the whole conversation
- the `turn execution` is one processing attempt for one customer turn

One session can therefore have many executions over time.

## What A Turn Execution Is

A turn execution is the durable record of the runtime trying to produce a
response after a customer message.

It is not just an in-memory function call. It is stored in Postgres together
with:

- execution id
- trace id
- status
- lease state
- retry/blocking state
- execution steps

That durability is what makes the runtime resumable and operator-recoverable.
If the process dies halfway through a turn, another worker can pick the turn up
from stored state rather than losing it.

## What Execution Steps Are

One turn execution usually contains multiple steps. In the current runtime that
often includes stages such as:

- ingest
- resolve policy
- match and plan
- compose response
- deliver response

Those steps are tracked individually with their own status and lease metadata.
That means a turn is not just “running or not running”; Parmesan knows which
part of the turn is in progress, blocked, waiting for retry, or finished.

## What The Worker Is

`cmd/worker` is a separate background process.

Its job is to look for runnable durable work and execute it. In the current
application wiring, the worker process starts:

- the runtime execution runner
- maintainer jobs
- lifecycle jobs
- replay jobs

So “worker” does not mean “one execution”. It means “the service process that
runs background work”.

One worker process can now run multiple executions concurrently.

The code that implements this now lives under the generic engine tree:

- policy resolution and matching: `internal/engine/policy/`
- execution runner and delegated capability handling: `internal/engine/runner/`
- semantic helpers used by planning and stage evaluation: `internal/engine/semantics/`

That split is intentional. The worker process is a deployable shell around the
engine, not the engine itself.

## What “Async Execution” Means Here

When a customer message arrives, the API does not try to do all response work
inline inside the HTTP request.

Instead, the usual pattern is:

1. persist the inbound event
2. create or coalesce a durable turn execution
3. return control to the caller
4. let a background worker pick up the execution
5. run the execution steps
6. persist the response and follow-up records

That is what “async execution” means in Parmesan:

- the turn is modeled as durable background work
- a separate worker process executes it later

The turn is asynchronous relative to the ingress request, but still durable and
observable.

## End-To-End Flow

```mermaid
sequenceDiagram
    participant U as User
    participant API as API
    participant DB as Postgres
    participant W as Worker

    U->>API: send message
    API->>DB: persist session event
    API->>DB: create or coalesce turn execution
    API-->>U: request accepted
    W->>DB: list runnable executions
    W->>DB: claim execution lease
    W->>DB: load execution + steps
    W->>DB: update step and response state
    W->>DB: persist output events, tool runs, traces
```

Another way to read the flow:

`message -> event -> turn execution -> execution steps -> response -> delivery`

## Worker vs Async Writes

This is another distinction that is easy to blur.

The execution worker runs the runtime logic. It decides policy, tools,
responses, approvals, and delivery state.

The async write queue is a persistence helper used to flush many repository
writes in the background.

So:

- execution workers run turn logic
- async write workers flush queued writes

These are different worker pools with different roles.

## Blocking vs Concurrent

The current runtime is:

- concurrent across executions
- blocking within one execution

That means:

- one execution worker can run one execution at a time
- if that execution is waiting on an LLM call or tool call, that worker is
  occupied until the call returns
- other executions can still proceed if other execution workers are free

So Parmesan is no longer a single global execution lane, but it is also not a
fully async event-loop runtime inside one turn.

## Worker Concurrency In Practice

The worker now has two distinct concurrency controls:

- `runtime.execution_concurrency`
- `runtime.async_write_workers`

These govern different bottlenecks:

- execution concurrency controls how many turn executions a worker process can
  process at once
- async write workers control how many queued persistence jobs can be flushed in
  parallel

This matters because increasing execution concurrency without enough async write
capacity can simply move the bottleneck from execution to persistence.

## Durable Retry And Resume

Durability in Parmesan is not limited to process restarts. It also covers
retryable downstream failures during execution, such as MCP/tool providers being
temporarily unavailable.

The important behavior is:

- the execution keeps the same `execution_id`
- the failing step records its retryable error durably
- the execution can move into `waiting` until the next retry window
- once the dependency recovers, the same execution can continue from stored
  state instead of creating a new turn

In the live Orbyte + Nexus validation, this was exercised by:

1. sending the product inquiry flow through Nexus
2. stopping `orbyte_full` before the direct MCP product tools executed
3. observing `compose_response` enter `waiting` with a retryable MCP
   connection-refused error
4. restarting `orbyte_full`
5. observing the same execution resume and complete successfully on a later
   attempt

## Retrieval Grounding During Execution

Retrieved knowledge is now tracked with an explicit outcome, not just a raw
list of retriever results.

That matters during response composition:

- `evidence_available` keeps the turn on the grounded-answer path
- `guidance_available` allows transient retriever guidance to drive the answer
  without being misread as a retrieval miss
- `insufficient` or `no_results` allow the composer to produce an honest miss
  instead of falling straight back to generic guideline text

So retrieval-aware execution now distinguishes between:

- grounded evidence
- transient retriever guidance
- retrieval miss or insufficient evidence

This is the runtime model to expect for retryable dependency outages: durable
state, resumable execution, and the same execution record carrying the turn to
completion.

## Multi-Instance Deployment

Parmesan supports running multiple `cmd/worker` instances against one shared
Postgres database.

Postgres is the coordination point. Workers do not coordinate through memory,
process-local locks, or leader election. Every autonomous background worker
path must claim durable work in the database before performing side effects.

The shared-database model covers:

- turn executions
- execution steps
- session watches
- replay eval runs
- maintainer jobs
- knowledge sync jobs
- idle lifecycle session actions

For turn execution, a worker atomically claims the `turn_executions` row, then
atomically claims the next runnable `execution_steps` row. While the step is
running, the worker renews both leases. State transitions from that runner are
fenced by `lease_owner`, so a stale worker cannot overwrite a newer worker that
resumed the same execution after lease expiry.

For the other background loops, the worker claims the specific runnable row
before acting:

- session watches use a watch lease before polling or sending reminder updates
- replay evals use an eval-run lease before scoring
- maintainer and knowledge-sync jobs use job leases before processing
- idle lifecycle handling claims the session by advancing `idle_checked_at`

This means it is safe to horizontally scale workers as long as all instances
share the same migrated Postgres database.

What this guarantees:

- two healthy workers should not execute the same claimed turn step at the same
  time
- expired work can be picked up by another worker after a crash or lost lease
- stale workers are prevented from committing runner-owned execution state once
  ownership has moved
- local records for tool runs and delivery attempts are deduplicated by
  idempotency key

What this does not guarantee by itself:

- an external MCP/OpenAPI provider will not repeat a side effect unless that
  provider honors the idempotency key Parmesan sends
- a non-idempotent remote operation cannot be made exactly-once by Parmesan
  alone if the process crashes after the remote operation succeeds but before
  the result is persisted

## Side-Effect Idempotency

Parmesan treats external side effects as at-least-once unless the downstream
provider participates in idempotency.

For tool calls, Parmesan computes a stable idempotency key from:

- execution id
- provider-qualified tool id
- normalized tool arguments

The runtime stores tool runs by that key. If a completed tool run already
exists for the same execution/tool/arguments, Parmesan reuses the stored output
instead of invoking the provider again.

When invoking external tools, Parmesan also passes the same key to the provider:

- HTTP header: `Idempotency-Key`
- HTTP header: `X-Idempotency-Key`
- MCP request metadata: `params._meta.idempotency_key`

For OpenAPI-imported tools, the headers are attached to the outgoing HTTP
request. For JSON-RPC/MCP tools, the headers are attached to the provider call
and the key is also included in MCP `_meta`.

Provider requirement:

- MCP/OpenAPI providers that create or mutate remote resources should persist
  and honor these idempotency keys.
- If the same idempotency key is received again, the provider should return the
  original result instead of creating a second resource.
- Read-only tools may ignore the key safely, but should still tolerate it.

Delivery attempts are also deduplicated by idempotency key. The key is derived
from the gateway conversation binding and event id, so a restarted gateway does
not create duplicate local delivery-attempt records for the same event.

## Where Delegated Verification Sits

Delegated result verification and watch creation happen inside worker/runtime
execution, not at ingress time.

That means the flow is:

1. ingress persists the customer event
2. a worker picks up the execution
3. the runtime may delegate to an ACP peer
4. the runtime may verify the delegated result through a contract
5. the runtime may create a watch from the verified resource

So delegated follow-up behavior is part of durable background execution, not a
special synchronous ingress shortcut.

The same execution path now also covers:

- workflow-bound ACP delegation, where policy attaches a specific workflow brief
  to the selected delegated agent
- response-capability rendering, where tool-backed direct responses are turned
  into normalized facts, example-guided model prompts, and deterministic
  fallbacks

One useful distinction from the live integration tests:

- direct MCP/tool flows currently provide the clearest proof of durable
  retry/resume
- delegated complaint intake in the Orbyte integration currently tends to fail
  soft when Orbyte minimal is unavailable, so it is not the best durability
  proof path

## Runtime Knobs

The two most important runtime knobs are:

- `runtime.execution_concurrency`
- `runtime.async_write_workers`

They mean:

- `execution_concurrency`: how many turn executions one worker process can run
  concurrently
- `async_write_workers`: how many background write workers flush queued writes

These are different capacities. Increasing one does not automatically increase
the other.

## Why This Model Exists

This split is deliberate.

Parmesan wants:

- durable turn state
- resumable background execution
- operator recovery
- traceability
- clear control between live runtime work and background learning work

If the runtime only handled turns inline inside the request path, you would
lose much of that durability and recovery model.

## Practical Mental Model

If you remember only one thing, use this:

- a `session` is the conversation
- a `turn execution` is one durable attempt to answer a turn
- `steps` are the sub-stages of that attempt
- the `worker` is the background process that runs those executions
- `async writes` are just the persistence queue, not the turn runner itself

## Related Documents

- [Concepts](./concepts.md)
- [Architecture](./architecture.md)
- [Engine](./engine.md)
