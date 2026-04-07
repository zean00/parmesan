# Detailed System Architecture and Implementation Plan

## Overview

Build this as a **modular monolith in Go** with three main deployables:

- `gateway`
- `api`
- `worker`

The foundation should keep Parlant’s typed control objects:

- observations
- guidelines
- relationships
- journeys
- strict canned-response modes

At the same time, it should borrow Hermes-style runtime ideas:

- cached vs ephemeral prompt assembly
- compact injected memory
- cross-session search
- response-scoped retrievers for grounding data
- maintained agent knowledge workspaces
- progressive loading of reusable artifacts
- first-class messaging gateway

Also keep Parlant’s online loop semantics:

- iterative preparation
- tools exposed only when context justifies them
- full traceability via spans and audit logs

Behavioral artifacts should be **authored in YAML**, but runtime should execute against compiled typed records stored in Postgres.

---

## 1. System Shape

Build one product with **four planes**:

1. **Gateway plane**
   - channel/platform connectivity and delivery

2. **Runtime plane**
   - policy resolution, matching, journeys, tools, response planning

3. **Policy plane**
   - seeded policy, learned refinements, templates, tool policies, rollout state

4. **Learning plane**
   - admin teaching compiler, admin feedback compiler, preference compiler, replay/eval

Do **not** start with microservices.

Start with:

- one codebase
- one transactional database
- clean architectural boundaries

### Initial Production Topology

- **`gateway` service**
  - long-running channel-facing runtime
  - adapter lifecycle
  - inbound webhook/polling normalization
  - delivery and retries
  - pairing/access control
  - capability detection
  - scheduled outbound delivery

- **`api` service**
  - HTTP / gRPC / SSE
  - admin console backend
  - SDK endpoints
  - auth
  - tenant routing

- **`worker` service**
  - async jobs
  - media enrichment
  - replay
  - evals
  - proposal compilation
  - batch preference inference

- **PostgreSQL**
  - source of truth for sessions, policy, proposals, preferences, rollout metadata
  - primary relational store and primary vector store via `pgvector`

- **Object storage**
  - S3-compatible storage for raw images, audio, and attachments

- **Redis**
  - hot cache for session state, policy digests, rate limiting

- **Job queue**
  - start with Postgres-backed jobs or NATS JetStream

- **Observability**
  - OpenTelemetry
  - Prometheus
  - Tempo / Jaeger
  - Loki

---

## 2. Core Design Principles

Your product will work if these rules remain true:

- **Policy is explicit.**
  - Conversation is only an authoring interface.
  - Canonical truth is always typed artifacts.
  - YAML is an authoring and interchange format, not the hot-path runtime format.

- **Policy changes are patches, not in-place mutation.**
  - Seeded policy remains visible.
  - Learned refinements layer on top.

- **Preferences are not policy.**
  - Customer style preferences must never override safety, compliance, money, or identity rules.

- **Raw media is not runtime context.**
  - Runtime should reason mostly over derived structured signals, with links back to originals.

- **Retrieval is not learning.**
  - Retrievers may inject response-scoped grounding data into a turn.
  - They must not silently mutate policy, preferences, or the agent knowledge workspace.

- **The knowledge workspace is not active policy.**
  - LLM-maintained knowledge pages can synthesize documents and sessions.
  - Serving behavior changes still flow through typed proposals, replay, and rollout.

- **Live customer sessions do not directly mutate active policy.**
  - They can generate proposals, but never silently update production policy.

- **Every turn stores the exact policy and knowledge snapshot hashes used.**
  - This is what makes replay, rollback, and debugging real.

- **Gateway owns transport semantics.**
  - Runtime owns conversational semantics.

- **Discovery is not exposure.**
  - Registering MCP servers or OpenAPI specs only populates the tool catalog.
  - Tools become visible to the runtime only through policy gating.

- **MCP is the runtime tool protocol.**
  - Runtime tool invocation should use MCP as the single external tool protocol.
  - OpenAPI should be treated as an import/source format that is compiled into MCP-style tools.

- **Multi-provider from day one.**
  - The system must support multiple LLM and model providers in v1.
  - Provider choice must be configurable per capability, tenant, policy, or rollout.

- **Turns must be resumable with persisted checkpoints.**
  - Runtime execution state should survive crashes, restarts, and worker loss via persisted checkpoints.
  - Recovery may resume from the last persisted checkpoint rather than the exact in-memory step.

---

## 3. High-Level Architecture

```text
Channels / Platforms
  ├── Web chat
  ├── WhatsApp
  ├── Telegram
  ├── Slack
  ├── Email
  ├── Voice note / call ingress
  └── Admin console

                ┌──────────────────────────────┐
                │           Gateway            │
                │ adapters, routing, delivery, │
                │ pairing, retries, cron,      │
                │ voice/media transport        │
                └──────────────┬───────────────┘
                               │
                  ┌────────────▼────────────┐
                  │ Event Normalization     │
                  │ session/thread/user map │
                  │ content parts/capability│
                  └────────────┬────────────┘
                               │
          ┌────────────────────▼────────────────────┐
          │                 API                     │
          │ admin UI, control plane, SDK, auth     │
          └───────────────┬─────────────────────────┘
                          │
        ┌─────────────────▼─────────────────┐
        │           Runtime Engine          │
        │ policy resolve, matching,         │
        │ journeys, tools, response plan    │
        └───────────┬──────────────┬────────┘
                    │              │
          ┌─────────▼──────┐   ┌───▼──────────────┐
          │ Policy Graph   │   │ Tool Orchestrator│
          │ snapshots,     │   │ APIs/actions      │
          │ rollouts       │   │ approvals         │
          └─────────┬──────┘   └───┬──────────────┘
                    │              │
        ┌───────────▼──────────────▼───────────┐
        │                Worker                │
        │ ASR/OCR/VLM, replay, eval, learning, │
        │ preference inference, async media    │
        └───────────────────────────────────────┘
```

---

## 4. Canonical Domain Model

The most important decision is to make **policy first-class**.

### 4.1 Artifact Families

Use these canonical artifact types:

- `Observation`
- `Guideline`
- `Relationship`
- `Journey`
- `JourneyPatch`
- `Template`
- `TemplateVariant`
- `ToolPolicy`
- `GlossaryTerm`
- `Retriever`
- `KnowledgeSource`
- `KnowledgePage`
- `KnowledgeSnapshot`
- `KnowledgeUpdateProposal`
- `CustomerPreference`
- `PolicyProposal`
- `PolicySnapshot`

### 4.1.1 Artifact Authoring Format

Behavioral artifacts should be definable in YAML:

- `observations`
- `guidelines`
- `relationships`
- `journeys`
- `templates`
- `tool_policies`

Behavioral artifacts should also support MCP-native references in addition to concrete normalized tool IDs.

YAML should be treated as:

- authoring format
- import/export format
- audit and diff format

It should **not** be the runtime source of truth.

Use a compiler pipeline:

```text
YAML -> schema validation -> semantic validation -> normalization -> versioning -> Postgres storage -> snapshot materialization
```

Store both:

- original source YAML for lineage, review, and round-trip editing
- compiled typed records and graph edges for hot-path runtime use

When YAML uses MCP references, compile them into normalized internal tool exposure records while preserving the original MCP-scoped authoring form.

### 4.2 Artifact Metadata

Every artifact should carry:

- `id`
- `org_id`
- `kind`
- `source`
  - `admin_seeded`
  - `admin_teaching`
  - `admin_feedback`
  - `system_inferred`
- `scope`
  - org / team / brand / channel / product / region / locale / segment
- `risk_tier`
  - `low`
  - `medium`
  - `high`
  - `regulated`
- `status`
  - `draft`
  - `proposed`
  - `shadow`
  - `canary`
  - `active`
  - `retired`
- `lineage_root_id`
- `version`
- `evidence_refs`
- `created_by`
- `approved_by`
- `created_at`
- `effective_from`
- `effective_to`

### 4.3 Policy Graph

Store policy as a graph:

- `policy_artifacts`
- `policy_edges`
- `policy_snapshots`
- `policy_proposals`
- `policy_rollouts`

`policy_edges` should cover both behavior and lineage:

- `depends_on`
- `excludes`
- `overrides`
- `entails`
- `disambiguates`
- `transition_to`
- `refines`
- `derived_from`

### 4.4 Session Model

Use one event model for all session types:

- customer session
- admin teaching session
- admin review session

Each session contains ordered `session_events`.

Each event contains `content_parts`.

### Content Part Types

- `text`
- `image`
- `audio`
- `file`
- `tool_result`
- `system_notice`
- `admin_annotation`

Each part may reference:

- a `media_asset`
- zero or more `derived_signals`

### 4.5 Derived Signals

This is the core of native multimodal support.

`derived_signals` should include:

- `signal_type`
  - intent
  - language
  - OCR text
  - product class
  - damage severity
  - sentiment
  - prosody
  - refund preference
  - etc.
- `source_part_id`
- `extractor_type`
  - ASR
  - OCR
  - vision model
  - rule engine
  - tool
  - LLM
- `payload_json`
- `confidence`
- `latency_class`
  - `sync`
  - `async`
- `version`
- `created_at`

Runtime should mostly consume these signals, not raw media blobs.

### 4.6 Customer Preference Model

Preferences should live in their own store:

- `customer_preferences`
- `customer_preference_events`

Each preference record should contain:

- `customer_id`
- `key`
- `value`
- `source`
  - `explicit`
  - `inferred`
  - `admin`
- `confidence`
- `scope`
- `last_confirmed_at`
- `expires_at`
- `evidence_refs`

### 4.7 Gateway Domain Model

Add explicit gateway entities:

- `ChannelAdapter`
- `ChannelAccount`
- `ConversationBinding`
- `DeliveryAttempt`
- `CapabilityProfile`
- `InboundEnvelope`
- `OutboundEnvelope`
- `ApprovalSession`
- `NotificationRoute`

This matters because a customer-facing agent is not just “a session with messages.” Slack threads, Telegram chats, WhatsApp conversations, email threads, and voice-note channels all have different routing and reply semantics.

### 4.8 Retriever Model

Retrievers are runtime grounding adapters, not tools and not learning jobs.

Use them for data the agent should normally “know” while composing the current response:

- relevant knowledge pages
- FAQ entries
- product documentation snippets
- account or customer context that is safe to read as background
- response-scoped transient guidance

Use tools for data that the agent must explicitly load, change, or act on.

Retriever scope should be explicit:

- agent-level retrievers run on every turn and may execute in parallel with matching
- guideline retrievers run only when the guideline matches
- journey retrievers run only when the journey is active
- journey-state retrievers run only when that specific state is active
- deferred retrievers may start work early but decide whether to inject results after matched guidelines and journeys are known

A retriever result may contain:

- `data`
- `metadata`
- `citations`
- `canned_response_fields`
- optional transient guidelines

Retriever results are **response-scoped**.

They may be persisted as trace evidence for replay/debugging, but they should not become durable memory or active policy by themselves.

If a retriever returns transient guidelines, the runtime should re-run relationship resolution before response planning.

Every served turn that uses retrievers should record:

- retriever IDs
- retriever result hashes
- citation refs
- `knowledge_snapshot_id` when the retriever reads from the agent knowledge workspace

### 4.9 Knowledge Workspace Model

Adopt the LLM-wiki pattern as a controlled knowledge workspace, not as the serving source of policy truth.

Model it as three layers:

1. **Raw sources**
   - uploaded files
   - configured document folders
   - URLs and imported pages
   - session transcripts
   - operator notes and feedback
   - tool results selected as evidence
   - media-derived signals

2. **Agent wiki**
   - LLM-maintained structured pages
   - topic pages
   - product pages
   - FAQ pages
   - troubleshooting pages
   - known-issue pages
   - policy-rationale pages
   - contradiction and stale-claim notes
   - source-backed citations

3. **Compiled knowledge index**
   - page digests
   - entity and concept links
   - citation graph
   - lexical and vector indexes
   - immutable `KnowledgeSnapshot` records used by retrievers

The agent wiki should be updated outside the customer response hot path.

Updates can come from:

- explicit admin document ingestion
- scheduled folder sync
- session-review feedback
- post-turn learning jobs
- lint jobs that detect contradictions, stale pages, orphan pages, or missing citations

The wiki can generate:

- `KnowledgeUpdateProposal`
- `GlossaryTerm` proposals
- `PolicyProposal` candidates when knowledge implies behavior changes
- `CustomerPreference` updates when evidence is customer-scoped and low risk

The wiki must not directly mutate active policy.

Runtime should read from a stable `KnowledgeSnapshot`.

The serving path should be:

```text
raw sources
  -> knowledge compiler
  -> agent wiki
  -> knowledge snapshot
  -> wiki retriever
  -> response-scoped grounding data
```

When a document update changes behavior, the flow should be:

```text
knowledge update
  -> proposal
  -> replay / eval
  -> review
  -> rollout
  -> active policy snapshot
```

---

## 5. Runtime Engine

This is the core online serving path.

### 5.1 Runtime Phases

For each turn:

1. **Gateway ingress**
   - receive message/webhook/poll result
   - authenticate source
   - map to tenant, user, conversation, session, and thread
   - normalize content parts
   - attach capability profile

2. **Ingest**
   - store raw event and content parts
   - upload blobs to object storage

3. **Synchronous enrichment**
   - text normalization
   - audio transcription + language ID
   - image OCR / basic vision extraction
   - video keyframe / transcript extraction when cheap enough
   - persist derived signals with extractor version and confidence
   - safety / moderation
   - channel metadata extraction

4. **Build working context**
   - recent session facts
   - active journey instances
   - durable customer preferences
   - approved policy snapshot candidates
   - active knowledge snapshot ID
   - derived multimodal signals
   - recent tool outputs

5. **Resolve policy view**
   - apply hard constraints
   - apply seeded policy in scope
   - overlay active learned refinements
   - overlay templates and tool policies
   - overlay preferences as soft constraints
   - materialize one `ResolvedPolicyView`

6. **Candidate generation**
   - active journeys and adjacent nodes
   - always-on hard constraints
   - artifacts matching scope and tags
   - lexical / vector search over policy digests
   - recency priors from previous turns

7. **Matcher cascade**
   - deterministic matchers first
   - classifier-based matchers second
   - LLM structured adjudication last

8. **Relationship resolution**
   - exclusions
   - dependencies
   - overrides
   - criticality / risk precedence
   - disambiguation

9. **Retriever grounding**
   - await agent-level retrievers already running in parallel with matching
   - run matched guideline / active journey / active journey-state scoped retrievers
   - inject response-scoped retrieved data, citations, and canned-response fields
   - inject transient guidelines if returned
   - re-run relationship resolution if transient guidelines were added
   - record retriever IDs, result hashes, and knowledge snapshot IDs in the trace

10. **Tool planning**
   - expose only justified tools
   - infer parameters
   - evaluate:
     - `needs_run`
     - `already_have_data`
     - `blocked`
   - run tools
   - append results and blocked-tool insights

11. **Journey update**
    - activate journey
    - advance node
    - backtrack
    - pause / wait-for-input
    - escalate

12. **Response planning**
    - choose response mode: `fluid`, `guided`, or `strict`
    - select candidate templates
    - decide whether to ask for text, image, or audio next
    - decide whether to emit preamble / progress events
    - decide whether to stream partial output

13. **Gateway delivery**
    - map response plan to channel capabilities
    - send text, media request, attachment, or audio reply
    - handle retries and delivery audit

14. **Post-turn**
    - store trace
    - store policy snapshot hash
    - store knowledge snapshot hash
    - emit async learning jobs
    - update session summary

### 5.1.1 Resumable Execution with Async Checkpoints

Parlant-style event-driven execution should be augmented with a lightweight resumable execution model.

Do **not** adopt a full workflow engine in v1.

Instead, use a **step journal with async checkpoints** that borrows the essential ideas from Temporal:

- persisted checkpoints
- explicit step boundaries
- retry metadata
- lease ownership
- idempotency keys
- safe replay and recomputation

Model each customer-triggered turn as a `TurnExecution` with ordered `ExecutionStep` records.

This is intentionally **not** strict Temporal-style durable execution.

The guarantee is:

- resume from the last persisted checkpoint
- recompute forward when necessary
- avoid duplicate side effects through idempotency and side-effect journals

Suggested durable step boundaries:

- `ingest`
- `sync_enrich`
- `resolve_policy`
- `match_and_plan`
- `run_tool:<tool_run_id>`
- `update_journey`
- `compose_response`
- `deliver_response`
- `post_turn`

Each step should carry:

- `status`
  - `pending`
  - `running`
  - `succeeded`
  - `failed`
  - `blocked`
  - `abandoned`
- `attempt`
- `lease_owner`
- `lease_expires_at`
- `idempotency_key`
- `recomputable`
- `last_error`
- `started_at`
- `finished_at`

### 5.1.2 Recovery Semantics

Recovery should support **whole-turn resume**.

If a worker crashes mid-turn:

- load the latest `TurnExecution`
- find the latest persisted checkpoint
- resume from that checkpoint and recompute forward as needed

If a step is marked `recomputable`, the runtime may recompute it from:

- persisted session events
- persisted tool results
- persisted journey state
- the stored policy snapshot

Do not rely on replaying opaque in-memory objects.

It is acceptable for the database to lag behind the actual in-memory step, as long as:

- the system can safely restart from the last persisted checkpoint
- recomputed steps are deterministic enough for runtime correctness
- side-effecting steps are protected against duplication

### 5.1.3 Retry Semantics

Use **bounded retries with safe recomputation**.

Rules:

- retry only idempotent or deduplicated side effects automatically
- use exponential backoff
- require idempotency tokens for delivery and consequential tool calls
- after retry exhaustion, persist recoverable failure state and surface it to operators

This should provide practical agent recovery without forcing the entire system into Temporal-level operational complexity.

### 5.1.4 Sync vs Async Persistence

Checkpoint persistence may be **asynchronous** for recomputable runtime progress.

Implementation note:

- use bounded Go channels as in-process write queues
- drain them with dedicated writer goroutines
- default to a single ordered writer per queue for causally dependent records
- batch or coalesce writes where useful
- expose queue depth and failure metrics

This is acceptable for steps such as:

- `resolve_policy`
- `match_and_plan`
- `compose_response`

because the runtime can resume from an earlier checkpoint and recompute them.

However, side-effecting steps require stricter handling.

Examples:

- consequential tool calls
- outbound delivery
- approval submissions

For these steps, require one or more of:

- synchronous persistence around the side effect
- idempotency keys
- deduplication records such as `tool_runs` or `delivery_attempts`
- external provider idempotency guarantees

So the design target is:

- async checkpoints for resumable computation
- stricter persistence and dedupe for side effects

### 5.1.5 Async Traceability

Use the same append-only async write approach for traceability and audit records.

That means:

- runtime emits structured trace and audit envelopes in-process
- envelopes are pushed onto bounded Go channels
- background writer goroutines persist them to `trace_index` or `audit_log`

This is acceptable because traceability records are primarily:

- diagnostic
- analytical
- operational

and do not define the authoritative side-effect boundary of the turn.

However, important trace records should still contain:

- `session_id`
- `execution_id`
- `trace_id`
- step name
- event kind
- timestamps
- provider or tool identifiers

If the trace store lags behind the live in-memory execution, this is acceptable as long as:

- customer-visible correctness is unaffected
- side-effect journals remain deduplicated
- operators can still reconstruct the turn from checkpoints, session events, and side-effect records

### 5.2 Candidate Generation Strategy

Use a hybrid candidate generator:

- graph constraints from active journeys and dependencies
- hard filters from scope and modality
- BM25 / full-text over artifact digests
- vector similarity over artifact summaries
- carry-forward from previously matched artifacts
- tool-result-triggered expansions

So the engine is neither:

- pure vector search
- nor pure brute-force LLM matching

It is:

```text
graph + metadata filters + search + structured adjudication
```

### 5.3 Matcher Types

Each observation or guideline should declare one matcher type:

- `deterministic`
- `classifier`
- `llm_structured`
- `hybrid`

Examples:

- channel is WhatsApp → deterministic
- uploaded image contains broken seal → classifier / VLM
- customer sounds upset in audio → classifier
- customer wants a policy exception → LLM structured
- customer preference “keep it short” → deterministic from profile

### 5.4 Policy Resolution Precedence

Resolve conflicts in this order:

1. hard constraints
2. active journey-state constraints
3. explicit `overrides` / `excludes`
4. more specific scope
5. higher risk tier
6. approved learned patch over same-lineage seed
7. customer preferences
8. lower-confidence inferences

### 5.4.1 Tool Discovery vs Tool Exposure

Tool discovery and tool exposure are separate concerns.

#### Tool discovery

Tool discovery populates the `ToolCatalog` from:

- native tools
- remote MCP servers
- remote OpenAPI specifications compiled into MCP-style tool definitions

#### Tool exposure

Tool exposure is resolved per turn from policy artifacts such as:

- guideline-attached tools
- observation-attached tools
- journey tool nodes
- explicit `ToolPolicy`

Policy artifacts should also be able to reference MCP at higher levels such as:

- MCP server
- MCP server plus explicit tool list
- MCP server plus future tags or groups

A discovered tool must **not** become automatically visible to the agent just because it exists in the catalog.

The runtime should only expose a tool when:

- the tool is registered and healthy
- the current resolved policy allows it
- the current context justifies it
- required approval constraints are satisfied

At runtime, all externally sourced tools should be presented through the same MCP-shaped invocation path, regardless of whether the source of truth was:

- a native MCP server
- an OpenAPI document

### 5.5 Policy Snapshots

Every turn gets a `policy_snapshot_id`.

That snapshot should contain:

- resolved artifact IDs
- artifact versions
- relationship graph fragment
- template set
- tool policy set
- model policy
- rollout version

This makes possible:

- perfect replay
- safe canary
- instant rollback
- auditability

For high-risk flows, optionally pin the session to a snapshot family instead of re-resolving every turn.

---

## 6. Messaging Gateway

This should be a **first-class subsystem**, not a thin adapter layer.

### 6.1 Gateway Responsibilities

The gateway should own:

- platform adapter lifecycle
- inbound webhook / polling normalization
- user / session / thread routing
- delivery and retry
- attachment / media ingress
- channel capability detection
- approval interactions in messaging
- scheduled outbound notifications
- voice delivery
- access control / allowlists / pairing
- fan-out to runtime and workers

### 6.1.1 API Streaming

The `api` service should support **Server-Sent Events (SSE)** as a first-class streaming transport.

Use SSE for:

- customer session event streams
- admin trace streams
- tool progress streams
- response token / delta streams
- replay / eval progress streams

Suggested event types:

- `session.event.created`
- `runtime.turn.started`
- `runtime.tool.started`
- `runtime.tool.completed`
- `runtime.response.delta`
- `runtime.response.completed`
- `approval.requested`
- `delivery.status`

Design runtime output as structured event envelopes first, then serialize them to SSE.

### 6.2 Capability-Aware Routing

The gateway should compute a `CapabilityProfile` per channel and conversation binding, for example:

- supports text
- supports image upload
- supports audio upload
- supports audio playback
- supports interactive approval
- supports threads
- supports proactive outbound
- max attachment size
- latency expectations

This is essential for native multimodal support.

### 6.3 Gateway vs Runtime Boundary

A clean boundary is:

- **Gateway owns transport semantics**
- **Runtime owns conversational semantics**

#### Gateway responsibilities

- receive message/webhook
- authenticate source
- map to tenant/user/session/thread
- normalize content parts
- attach capability profile
- deliver outbound response
- handle retry/backoff
- maintain pairing/allowlists
- run scheduled delivery triggers

#### Runtime responsibilities

- resolve policy snapshot
- match observations/guidelines/journeys
- plan tools
- generate response plan
- emit structured outbound response

### 6.4 Approval Flow

For consequential customer-facing actions, approval should be a shared concern:

- runtime decides approval is required
- gateway manages the conversational approval UX on that platform
- approval result re-enters runtime as a structured event

### 6.5 Scheduled and Proactive Delivery

Move scheduled outbound communication into the gateway plane:

- follow-up reminders
- human-escalation notifications
- async analysis completion
- delivery of replay or review requests
- proactive customer updates when allowed

---

## 7. Learning Plane

This is the differentiator.

### 7.1 Three Compilers

Build **three separate compilers**, not one generic learning agent.

#### A. Teaching Compiler

**Input:** admin teaching conversation  
**Output:** structured `PolicyProposal`

Responsibilities:

- extract candidate observations, guidelines, journey edits, templates
- detect ambiguity
- ask clarification questions
- deduplicate against current policy
- package a patch set

#### B. Feedback Compiler

**Input:** finished customer session + admin feedback  
**Output:** targeted policy patch proposal

It should classify root cause as:

- policy gap
- policy conflict
- bad template
- journey ordering issue
- tool issue
- preference miss
- one-off exception

Only the first four should normally mutate shared policy.

#### C. Preference Compiler

**Input:** customer session history  
**Output:** durable customer preference updates

It should separate:

- explicit preferences
- repeated inferred preferences
- weak one-turn hints

#### D. Knowledge Workspace Compiler

**Input:** configured document sources, finished sessions, operator notes, feedback, tool results, and derived multimodal signals
**Output:** versioned agent wiki updates, `KnowledgeSnapshot` records, and optional typed proposals

Responsibilities:

- ingest configured folders and uploaded documents
- preserve raw-source checksums and citation refs
- create and update knowledge pages
- maintain an index of pages, entities, concepts, and citations
- detect contradictions and stale claims
- flag missing citations and orphan pages
- create `KnowledgeUpdateProposal` records for review when updates are risky
- create `PolicyProposal` records when a knowledge update implies behavior changes
- create `GlossaryTerm` proposals for business-specific terms
- avoid directly mutating active policy or active retriever snapshots

The knowledge workspace should follow the LLM-wiki pattern:

```text
raw sources
  -> maintained agent wiki
  -> compiled knowledge snapshot
  -> retriever-readable index
```

But it must remain governed by Parmesan's safety rules:

- raw sources are immutable or append-only
- wiki pages are synthesized artifacts with citations
- snapshots are versioned and stable for replay
- customer turns read from a snapshot, not a mutable working tree
- high-risk changes require review before they can influence shared behavior

### 7.2 Proposal Lifecycle

Every proposal moves through:

- `proposed`
- `reviewed`
- `shadow`
- `canary`
- `active`
- `retired`

A proposal should contain:

- diff bundle
- rationale
- evidence refs
- replay score
- safety score
- scope
- rollout plan

### 7.3 Replay and Eval

Before activation, every proposal should pass three gates.

#### Structural Gate

- valid schema
- no broken references
- no invalid journey edges
- no impossible template slots
- no disallowed scope overrides

#### Behavior Gate

- replay against similar historical sessions
- compare old vs new traces
- score desired outcomes

#### Risk Gate

If the proposal touches:

- money
- legal
- compliance
- identity
- privacy
- abuse

then require human approval.

### 7.4 Shadow Mode

In shadow mode, the runtime evaluates both:

- current active policy
- candidate shadow policy

Only the active one affects the response.

Store both traces and compare:

- matched artifacts
- chosen journey path
- tool calls
- blocked-tool insights
- response mode
- human review score

---

## 8. Native Multimodal Architecture

Multimodal should be native in:

- event model
- signal model
- policy model
- response model
- gateway capability model

### 8.1 Input Model

Do **not** model a message as a single string.

Model it as:

```text
SessionEvent
  └── ContentPart[]
        ├── text
        ├── image
        ├── audio
        ├── file
        └── metadata
```

### 8.2 Synchronous vs Asynchronous Enrichment

Use two enrichment classes.

#### Synchronous Enrichers

These are on the hot path:

- ASR transcript
- language ID
- OCR
- basic image classification
- safety checks
- attachment metadata

#### Asynchronous Enrichers

These are off the hot path:

- detailed product or damage analysis
- call quality scoring
- deeper prosody / emotion analysis
- clustering for learning
- QA labeling

Only block the response on enrichments that current policy actually requires.

### 8.3 Multimodal Observations

Observations must declare supported signal sources:

- text
- image
- audio
- metadata
- tool result

Examples:

- screenshot contains payment failure message
- product photo shows broken seal
- voice tone indicates anger or frustration
- OCR extracted order number with low confidence
- audio language is Bahasa Indonesia

### 8.4 Multimodal Journey Nodes

Journey nodes should support multiple node types:

- `MessageNode`
- `RequestInputNode`
- `RequestMediaNode`
- `AnalyzeMediaNode`
- `ToolNode`
- `DecisionNode`
- `TemplateNode`
- `EscalationNode`

This is how image and audio become truly native.

### 8.5 Output Model

Responses should also be modality-aware.

A response plan can choose to:

- send text
- request image
- request audio note
- send template
- send link / file
- send spoken audio
- escalate to human

For voice, start with:

- voice notes
- asynchronous audio replies

Add full duplex streaming later.

---

## 9. Storage and Indexing

Use PostgreSQL as the source of truth.

### 9.1 Core Tables

Minimum first-pass schema:

- `orgs`
- `customers`
- `sessions`
- `session_events`
- `content_parts`
- `media_assets`
- `derived_signals`
- `tool_catalog`
- `tool_provider_bindings`
- `tool_runs`
- `journey_instances`
- `policy_artifacts`
- `policy_edges`
- `policy_proposals`
- `policy_snapshots`
- `policy_rollouts`
- `customer_preferences`
- `customer_preference_events`
- `channel_accounts`
- `conversation_bindings`
- `delivery_attempts`
- `approval_sessions`
- `eval_runs`
- `trace_index`
- `audit_log`

### 9.2 Search

Start simple:

- Postgres full-text search for sessions, admin feedback, policy text
- `pgvector` for artifact summaries and session similarity
- Redis for hot digests and recent session summaries

Only move to OpenSearch / Qdrant when scale forces it.

### 9.2.1 Postgres as Unified Primary Store

Use PostgreSQL with `pgvector` as the primary database for both transactional and semantic retrieval workloads in v1.

Recommended usage:

- relational tables for canonical records
- JSONB for flexible payloads and extractor outputs
- full-text indexes for keyword search
- `pgvector` indexes for artifact and session similarity

Do not introduce a separate vector database in the first implementation.

### 9.2.2 Tool Catalog Storage

Persist discovered tools in normalized catalog tables.

Minimum concepts:

- `ToolProviderBinding`
  - registered MCP server or OpenAPI source
- `ToolCatalogEntry`
  - normalized internal tool definition exposed through MCP-compatible runtime metadata
- `ToolAuthBinding`
  - credentials or auth reference for invocation

Discovery should happen in the control plane or background sync path, not during the hot path of each customer turn.

Cache and periodically refresh:

- remote MCP capabilities
- remote OpenAPI documents
- normalized operation schemas compiled into MCP-style tool entries

OpenAPI should not require a distinct runtime invocation path.

Instead:

- ingest OpenAPI
- normalize operations
- expose them in the tool catalog as MCP-shaped tools
- invoke them through the same runtime tool orchestration path as native MCP tools

### 9.3 Raw vs Derived Media Retention

Keep:

- raw media in object storage
- derived transcript / OCR / features in Postgres
- TTL and redaction controls per tenant

Default runtime should prefer derived signals.

Raw media should only be fetched when needed for:

- human review
- deeper analysis

Multimodal support should not be implemented as a retriever-first feature.

The durable path is:

```text
ACP content part
  -> media asset
  -> enrichment job
  -> derived signals
  -> runtime context
  -> optional retriever query
```

Use retrievers after enrichment, when derived signals can ground a knowledge lookup.

Examples:

- image OCR detects an order number, then a retriever fetches relevant return-policy pages
- image classification detects product damage, then a retriever fetches damage-handling guidance
- audio transcription detects a warranty question, then a retriever fetches warranty FAQ pages
- video keyframes detect a setup issue, then a retriever fetches troubleshooting pages

If derived signals are not ready:

- the runtime may ask the customer for text clarification
- the runtime may emit a status event and pause
- the runtime may continue with lower-confidence text-only matching
- the worker may resume when async enrichment finishes

---

## 10. Admin Control Plane

You need a real admin UI, not just an API.

### Minimum Screens

#### Policy Explorer

- artifact graph
- lineage
- active scopes
- template variants

#### Teach Policy

- chat with teacher agent
- structured diff preview
- clarification loop

#### Session Review

- transcript
- media
- trace
- admin annotations
- “what should have happened?”

#### Proposal Queue

- approve / reject
- replay summary
- affected metrics
- risk labels

#### Preference Inspector

- per-customer profile
- evidence
- confidence
- decay / expiry

#### Rollout Console

- shadow
- canary
- rollback
- policy snapshot history

#### Gateway Operations

- channel account health
- webhook status
- delivery failure logs
- retry queues
- capability profile view
- proactive notification routes

#### Tool Registry

- registered MCP servers
- registered OpenAPI specs
- discovered tool catalog
- OpenAPI-to-MCP imported tool views
- health / refresh status
- auth binding status
- policy exposure references

---

## 11. Go Code Layout

```text
cmd/
  gateway/
  api/
  worker/
  migrate/

internal/
  policyyaml/
    schema/
    compiler/
    loader/

  gateway/
    adapters/
      web/
      whatsapp/
      telegram/
      slack/
      email/
      voice/
    routing/
    delivery/
    approvals/
    capabilities/
    normalization/
    scheduler/

  runtime/
    ingest/
    enrich/
    resolver/
    candidate/
    matcher/
    relation/
    planner/
    composer/
    trace/

  compiler/
    teaching/
    feedback/
    preference/
    knowledge/

  knowledge/
    sources/
    wiki/
    index/
    retriever/
    lint/

  rollout/
    snapshot/
    shadow/
    canary/
    replay/

  domain/
    policy/
    session/
    journey/
    template/
    tool/
    media/
    preference/
    gateway/
    audit/

  adapters/
    db/postgres/
    cache/redis/
    queue/
    blobstore/s3/
    model/
      registry/
      routing/
      providers/
        openai/
        anthropic/
        google/
        openrouter/
      llm/
      embeddings/
      vision/
      speech/
      moderation/

  api/
    http/
    grpc/
    sse/

  tools/
    catalog/
    providers/
      native/
      mcp/
      openapi_import/
    orchestrator/

pkg/
  sdk/
```

### Key Interface Boundaries

```go
type PolicyResolver interface {
    Resolve(ctx context.Context, in ResolveInput) (ResolvedPolicyView, error)
}

type Matcher interface {
    Match(ctx context.Context, in MatchInput) (MatchResult, error)
}

type MediaEnricher interface {
    Enrich(ctx context.Context, part ContentPart) ([]DerivedSignal, error)
}

type Retriever interface {
    Retrieve(ctx context.Context, in RetrieverContext) (RetrieverResult, error)
}

type KnowledgeCompiler interface {
    Compile(ctx context.Context, in KnowledgeCompileInput) (KnowledgeSnapshot, error)
}

type ProposalCompiler interface {
    Compile(ctx context.Context, in CompileInput) (PolicyProposal, error)
}

type Evaluator interface {
    Replay(ctx context.Context, in ReplayInput) (ReplayReport, error)
}

type ChannelAdapter interface {
    Start(ctx context.Context) error
    Send(ctx context.Context, out OutboundEnvelope) (DeliveryResult, error)
    Capabilities(ctx context.Context, binding ConversationBinding) (CapabilityProfile, error)
}
```

---

## 12. Model / Provider Abstraction

Multi-provider support is a **v1 requirement**, not a later extension.

Use separate interfaces for:

- `ReasoningModel`
- `StructuredModel`
- `EmbeddingModel`
- `VisionModel`
- `SpeechToTextModel`
- `TextToSpeechModel`
- `ModerationModel`

Do **not** bind the whole system to one provider.

Provider selection should be possible by:

- capability
- tenant
- channel
- risk tier
- rollout or experiment

Use an existing Go library or adapter framework where it reduces implementation cost, especially for:

- chat and reasoning model adapters
- embeddings
- retries and timeouts
- provider auth wiring

However, keep the domain interfaces provider-neutral. Do not leak provider-specific request or response shapes into runtime, policy, or storage layers.

Recommended approach:

- define internal capability interfaces first
- implement provider adapters behind them
- use a shared client library only inside adapter implementations

The runtime should also support:

- provider fallback chains
- per-capability default providers
- provider-level health and circuit breaking
- provider-aware cost and latency telemetry

### Recommended Model Allocation

- small / cheap structured model
  - extraction
  - policy proposal parsing
  - preference parsing

- stronger model
  - high-risk response composition
  - ambiguous policy adjudication

- specialized ASR / VLM
  - audio
  - image

- separate embedder
  - policy retrieval
  - session retrieval
  - knowledge page retrieval

Later, distill replay traces and approved proposals into smaller in-house matchers.

Do **not** start with weight fine-tuning.

---

## 13. Observability and Audit

Every turn should emit a trace tree like:

- gateway ingress
- normalization
- sync enrichment
- policy resolve
- candidate generation
- matcher evaluations
- relationship resolution
- retriever execution and result hashes
- knowledge snapshot ID and citation refs
- tool planning
- tool execution
- journey transition
- response planning
- gateway delivery
- post-turn learning jobs

Store an audit record for:

- who created or approved a policy artifact
- which snapshot served a turn
- which knowledge snapshot and retrievers grounded the response
- which proposal modified a lineage
- which preferences were inferred and why
- which channel delivered the response
- which approval flow was triggered

This is the backbone of safe learning.

---

## 14. Security and Governance

Hard requirements:

- encrypted blobs at rest
- tenant-scoped keys
- signed media URLs
- PII redaction pipeline
- retention controls by tenant and data class
- RBAC for policy promotion
- human approval for high-risk policy categories
- immutable audit log
- model output moderation
- approval policies for consequential tools
- gateway-level source verification and allowlists

---

## 15. Detailed Implementation Plan

### Phase 0: Foundation

**Deliver:**

- repo skeleton
- configuration system
- migrations
- tracing
- auth / tenancy
- provider abstraction interfaces
- multi-provider routing and adapter skeleton
- local dev stack

**Exit criteria:**

- `api`, `gateway`, and `worker` boot
- migrations run
- trace spans visible
- object store, DB, and cache integrated
- at least two model providers work behind the same internal interfaces

---

### Phase 0.5: Messaging Gateway Foundation

**Deliver:**

- gateway service skeleton
- adapter interface
- inbound / outbound envelope model
- session / thread binding model
- delivery retries
- capability profile
- one real adapter first, ideally web chat or Telegram

**Exit criteria:**

- one channel can send text + image into the runtime
- runtime responses are delivered back correctly
- session / thread mapping is stable
- outbound retries and audit logs work

---

### Phase 1: Core Runtime with Seeded Policy

**Deliver:**

- policy artifact schema
- policy edges
- policy resolver
- session / event model
- text-only runtime
- template modes
- tool orchestration
- journey instances
- policy snapshotting

**Exit criteria:**

- seeded guidelines, journeys, templates, and tools run in production-like text chat
- every turn stores a trace and snapshot hash

---

### Phase 2: Candidate Generation and Matching Hardening

**Deliver:**

- scope filters
- search index over artifact digests
- vector / FTS candidate generation
- deterministic / classifier / LLM matcher cascade
- relation resolver

**Exit criteria:**

- runtime scales beyond tiny rule sets
- artifact conflicts resolve deterministically

---

### Phase 3: Admin Teaching Compiler

**Deliver:**

- admin teaching session type
- proposal extraction schema
- clarifying-question loop
- proposal store
- diff preview UI / API

**Exit criteria:**

- admin can teach new rules in conversation
- result is a typed proposal bundle, not free text

---

### Phase 4: Replay, Shadow, and Canary

**Deliver:**

- replay harness
- proposal structural checks
- shadow evaluation path
- canary rollout controller
- rollback by snapshot

**Exit criteria:**

- learned policy can be evaluated safely before activation

---

### Phase 5: Feedback Compiler

**Deliver:**

- session review UI / API
- feedback annotations
- root-cause classifier
- patch suggestion generator
- response-diff to template extraction

**Exit criteria:**

- admin can review a bad session and get targeted proposals

---

### Phase 6: Customer Preference Engine

**Deliver:**

- preference schema
- explicit / inferred extraction
- merge / confidence / decay logic
- preference-aware response planning
- preference inspector UI / API

**Exit criteria:**

- customer language / style / channel preferences affect responses safely
- preferences never override hard constraints

---

### Phase 7: Knowledge Workspace and Retrievers

**Deliver:**

- `KnowledgeSource`
- `KnowledgePage`
- `KnowledgeSnapshot`
- `KnowledgeUpdateProposal`
- source checksum and citation tracking
- document-folder ingestion job
- maintained agent wiki index
- wiki retriever attached to an agent
- response-scoped retriever stage
- trace recording of retriever IDs, result hashes, and `knowledge_snapshot_id`

**Exit criteria:**

- configured documents are compiled into a versioned agent knowledge snapshot
- runtime can retrieve relevant knowledge from the snapshot without mutating it
- every retrieved answer includes citations
- risky knowledge changes produce proposals instead of directly changing active behavior

---

### Phase 8: Native Image Support

**Deliver:**

- image content parts
- object storage + metadata
- OCR + vision extractors
- image-derived signals
- multimodal observations
- request / analyze image journey nodes

**Exit criteria:**

- image-only and text+image flows work end to end
- damaged-item and screenshot-troubleshooting use cases are production-ready

---

### Phase 9: Native Audio Support

**Deliver:**

- audio content parts
- ASR
- language ID
- prosody / sentiment features
- audio-aware observations
- request-audio journey nodes
- audio reply support

**Exit criteria:**

- voice-note flows work natively
- audio can influence journey and tone policy

---

### Phase 10: Native Video Support

**Deliver:**

- video content parts
- object storage + metadata
- keyframe extraction
- transcript extraction when audio is present
- video OCR / scene labels
- video-derived signals
- video-aware observations

**Exit criteria:**

- short customer videos can produce durable derived signals
- video-derived signals can trigger journeys, tools, and retrievers
- raw video is only fetched for deeper analysis or human review

---

### Phase 11: Hardening

**Deliver:**

- quotas
- rate limits
- retention
- RBAC
- audit dashboards
- backfill jobs
- disaster recovery
- tenant isolation tests

**Exit criteria:**

- ready for multi-tenant production

---

## 16. First Vertical Slice

The best first end-to-end slice is:

## **E-commerce returns + damaged-item claims with text and image**

### Why this slice

- needs guidelines
- needs journeys
- needs templates
- needs tool calls
- benefits from image-native support
- benefits from admin teaching and post-session feedback
- exposes customer preferences
- has clear risk boundaries

Do **not** start with live phone calls as the first slice.

---

## 17. Non-Negotiable Rules for Safe Learning

1. The serving agent never directly edits active policy.
2. All learned changes are stored as proposals and patches.
3. Every served turn stores the exact policy and knowledge snapshots.
4. Preferences are separate from shared policy.
5. Non-backward-compatible journey edits do not mutate active journey instances.
6. High-risk proposals require human approval.
7. Raw media retention is controlled separately from derived signals.
8. Replay must happen before rollout.
9. Gateway capability constraints must be respected during planning and delivery.
10. Retrievers inject response-scoped grounding data only; they do not mutate active policy, durable memory, or the agent wiki.

---

## 18. Immediate Next Design Documents

If implementing this next, the first four ADRs should be:

1. **Policy snapshot semantics**
2. **Session and media canonical schema**
3. **Proposal rollout rules**
4. **Gateway routing and capability model**

These four decisions will determine whether the system stays reliable as it begins to learn.
