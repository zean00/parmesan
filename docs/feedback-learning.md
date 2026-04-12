# Feedback Loop / Learning

Parmesan has a closed-loop learning system, but live customer sessions do not
directly mutate active production behavior.

```mermaid
flowchart LR
    Session[Session + Trace]
    Feedback[Operator Feedback]
    Compiler[Learning Compiler]
    Prefs[Customer Preferences]
    Knowledge[Knowledge Proposals]
    Policy[Draft Policy / SOUL Proposals]
    Regressions[Regression Fixtures]
    Review[Operator Review]

    Session --> Feedback --> Compiler
    Compiler --> Prefs
    Compiler --> Knowledge
    Compiler --> Policy
    Compiler --> Regressions
    Prefs --> Review
    Knowledge --> Review
    Policy --> Review
```

## Inputs

The main learning inputs are:

- session-level operator feedback
- response-scoped feedback
- customer preference signals
- seeded and synced knowledge sources
- learning from conversation history

## What Feedback Can Produce

Feedback can compile into:

- customer preferences
- shared knowledge proposals
- customer memory
- draft policy / SOUL proposals
- regression fixture candidates

## Learning Boundaries

Parmesan keeps these boundaries explicit:

- retrieval is not learning
- runtime turns do not mutate active policy
- customer preferences do not override hard safety or business rules
- shared knowledge changes remain reviewable
- policy changes become proposals first

## Knowledge Loop

The current system supports:

- file-backed seeded knowledge
- typed compiled knowledge snapshots
- operator-visible knowledge sources, jobs, pages, proposals, and lint
- maintainer jobs that update knowledge workspaces and proposals

The long-range direction is documented in the repository discussions around a
more LLM-maintained evolving wiki, but the current implementation is still a
governed typed-knowledge system with proposal and apply steps.

## Preference Loop

Customer-specific learning flows into preference records with lifecycle actions:

- confirm
- reject
- expire

This keeps customer memory explicit and reviewable.

## Operator Role

Operators remain central to learning:

- they submit feedback
- they review proposals
- they confirm preferences
- they inspect teaching state
- they can export regression candidates

## Dashboard Surfaces

Relevant current operator surfaces include:

- session feedback
- teaching-state inspection
- control-state and recent change views
- regression and quality-related operator endpoints

```mermaid
sequenceDiagram
    participant O as Operator
    participant API as Operator API
    participant L as Learning Compiler
    participant M as Maintainer
    participant DB as Database

    O->>API: submit feedback
    API->>DB: persist feedback record
    API->>L: compile feedback outputs
    L->>DB: write preference / proposal / regression artifacts
    API->>M: queue maintainer work when needed
    M->>DB: update workspaces / runs / proposals
```

## Implementation References

- feedback ingest and lineage endpoints: `internal/api/http/server.go`
- feedback domain model: `internal/domain/feedback/types.go`
- learning compiler: `internal/knowledge/learning/learning.go`
- maintainer runner: `internal/maintainer/runner.go`
- retriever and compiled knowledge: `internal/knowledge/retriever/` and `internal/knowledge/compiler/`
- customer preference lifecycle: `internal/api/http/server.go`
