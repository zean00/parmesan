# Architecture

This document describes the current repository architecture, not just the long
range design target.

## What This Page Is For

Use this page when you need to answer questions like:

- which deployables exist right now
- what the main runtime surfaces are
- where data is persisted
- how ACP, the dashboard, the worker, and bootstrap fit together

If you specifically need the difference between `session`, `turn execution`,
`worker`, and `async writes`, read [Execution Model](./execution-model.md)
first.

## System Shape

Parmesan is currently a modular Go monolith with multiple deployables:

- `api`
- `worker`
- `migrate`
- `bootstrap`
- `dashboard` frontend

Supporting infrastructure:

- PostgreSQL with pgvector
- optional Nexus as an ACP-facing channel layer

## Quick Map

| Component | Main responsibility |
| --- | --- |
| `api` | ACP ingress, operator API, trace/control inspection, synchronous mutations |
| `worker` | asynchronous jobs such as maintainer work and background processing |
| `bootstrap` | load file-backed agents, policy references, and seeded knowledge |
| `migrate` | apply SQL schema migrations |
| `dashboard` | operator-facing UI |
| Postgres | system of record for sessions, executions, traces, knowledge, and governance state |

```mermaid
flowchart LR
    Client["ACP Client / Channel"]
    Nexus["Nexus or Adapter"]
    Dashboard["Dashboard"]
    API["API"]
    Worker["Worker"]
    Bootstrap["Bootstrap"]
    Migrate["Migrate"]
    DB["Postgres + pgvector"]
    Bundle["Config / Agents / Knowledge"]

    Client --> Nexus --> API
    Dashboard --> API
    API --> DB
    Worker --> DB
    Bootstrap --> DB
    Migrate --> DB
    Bundle --> Bootstrap
    Bundle --> API
    Bundle --> Worker
```

## Main Runtime Surfaces

### API

The API exposes:

- ACP conversation endpoints
- operator endpoints
- policy/control endpoints
- trace and execution inspection endpoints
- SSE streams for sessions and notifications

This is the primary control and conversation edge for the platform.

### Worker

The worker handles asynchronous work such as:

- knowledge compilation and sync
- maintainer and learning jobs
- media enrichment
- background evaluation and replay support

The worker exists so the runtime path does not need to inline all slower or
backgroundable work inside customer-facing request handling.

### Bootstrap

Bootstrap loads file-backed startup data:

- agent definitions
- policy bundle references
- seeded knowledge sources
- configured MCP providers

Bootstrap is the bridge between file-backed repo assets and durable runtime
state in Postgres.

### Dashboard

The dashboard is the operator surface for:

- session inbox
- session intervention
- trace inspection
- notifications
- control-state inspection
- agent testing

The dashboard is not a separate backend. It is a frontend over the operator API
served by the main platform.

## High-Level Flow

1. A client creates an ACP session.
2. The session gets normalized customer context.
3. A customer message enters ACP.
4. Parmesan creates or coalesces a durable execution.
5. The runtime resolves policy, retrieval, tools, and response composition.
6. Events, audit records, response records, and trace spans are persisted.
7. Operators can inspect the session, execution, and trace.
8. Feedback can produce customer preferences, knowledge proposals, and draft
   policy changes.

This is the main reason the architecture is split the way it is: live customer
turns, durable inspection, and governed learning all share the same system of
record, but they are not the same workflow.

```mermaid
sequenceDiagram
    participant C as ACP Client
    participant A as API
    participant R as Runtime
    participant D as Database
    participant O as Operator

    C->>A: create session / send message
    A->>D: persist event
    A->>R: enqueue turn
    R->>D: persist execution, response, trace
    O->>A: inspect session / trace / feedback
    A->>D: persist feedback and learning artifacts
```

## Storage Model

Postgres is the primary system of record for:

- sessions
- events
- executions and execution steps
- responses and trace spans
- approvals
- tool runs
- delivery attempts
- audit records
- policy / rollout state
- knowledge sources / snapshots / proposals
- customer preferences
- maintainer jobs and workspaces

In other words, Parmesan is operationally stateful by design. The runtime does
not treat these artifacts as disposable logs.

## Current Deployable Topology

Default compose startup:

- `postgres`
- `migrate`
- `bootstrap`
- `api`
- `worker`
- `dashboard`

The backend image now contains the stock file-backed deployment bundle:

- `/config`
- `/agents`
- `/knowledge`
- `/examples`

This keeps the default release-style deployment self-contained, while still
allowing local development to point at repo files directly.

## Implementation References

- application wiring: `internal/app/app.go`
- HTTP API server: `internal/api/http/server.go`
- operator notification API: `internal/api/http/operator_dashboard.go`
- worker entrypoint: `internal/worker/server.go`
- store interfaces: `internal/store/interfaces.go`
- Postgres repository: `internal/store/postgres/repository.go`
- memory store: `internal/store/memory/store.go`
