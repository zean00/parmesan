# Parmesan

Parmesan is a customer-facing agent runtime focused on explicit policy control,
durable turn execution, multi-provider model routing, and first-class remote
tool catalogs.

## Status

This repository currently contains the bootstrap implementation:

- runnable `api`, `gateway`, `worker`, and `migrate` binaries
- YAML policy parsing and validation
- REST + SSE API surfaces
- in-memory stores for sessions, policy bundles, tool providers, and execution journals
- domain types for policy, sessions, tools, and durable execution
- provider-neutral model abstractions with OpenAI and OpenRouter adapters
- proposal, replay, and canary rollout control primitives
- migration SQL and protobuf contract stubs for later expansion

## Run

```bash
go run ./cmd/api
go run ./cmd/worker
```

Legacy compatibility surface:

```bash
go run ./cmd/gateway
```

Default ports:

- `api`: `:8080`
- `gateway`: `:8081` legacy compatibility only
- `worker`: `:8082`

ACP is the primary public conversation interface. The separate `gateway`
service remains available for legacy `/v1/web/...` clients while ACP-to-channel
adapters migrate externally.

Operator endpoints support single-tenant RBAC. `OPERATOR_API_KEY` remains a
bootstrap admin credential; production operators can use stored operator API
tokens or trusted identity headers via `OPERATOR_TRUSTED_ID_HEADER` and
`OPERATOR_TRUSTED_ROLES_HEADER`.

## API Endpoints

- `GET /healthz`
- `GET /v1/info`
- `GET /v1/models/providers`
- `POST /v1/policy/validate`
- `POST /v1/policy/import`
- `GET /v1/policy/bundles`
- `POST /v1/proposals`
- `GET /v1/proposals`
- `GET /v1/proposals/{id}`
- `GET /v1/proposals/{id}/preview`
- `GET /v1/proposals/{id}/summary`
- `POST /v1/proposals/{id}/state`
- `POST /v1/rollouts`
- `GET /v1/rollouts`
- `GET /v1/rollouts/{id}`
- `POST /v1/rollouts/{id}/disable`
- `POST /v1/rollouts/{id}/rollback`
- `GET /v1/admin/events/stream`
- `POST /v1/sessions`
- `POST /v1/sessions/{id}/events`
- `GET /v1/sessions/{id}/events`
- `GET /v1/sessions/{id}/events/stream`
- `POST /v1/acp/sessions`
- `GET /v1/acp/sessions/{id}`
- `POST /v1/acp/sessions/{id}/messages`
- `POST /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events/stream`
- `GET /v1/acp/sessions/{id}/approvals`
- `POST /v1/acp/sessions/{id}/approvals/{approval_id}`
- `GET /v1/operator/sessions` with optional `customer_id`, `agent_id`, `mode`, `label`, `operator_id`, `assigned_operator_id`, `unassigned=true`, `active=true`, `pending_approval=true`, `failed_media=true`, `unresolved_lint=true`, `last_activity_after`, `last_activity_before`, `view`, `cursor`, and `limit` filters
- `GET /v1/operator/queue/summary`
- `GET /v1/operator/sessions/{id}`
- `GET /v1/operator/sessions/{id}/events` with optional `min_offset`, `limit`, `source`, `trace_id`, and `kind` filters
- `GET /v1/operator/sessions/{id}/stream`
- `POST /v1/operator/sessions/{id}/takeover`
- `POST /v1/operator/sessions/{id}/resume`
- `POST /v1/operator/sessions/{id}/messages`
- `POST /v1/operator/sessions/{id}/messages/on-behalf-of-agent`
- `POST /v1/operator/sessions/{id}/notes`
- `POST /v1/operator/sessions/{id}/process`
- `POST /v1/operator/sessions/{id}/feedback`
- `GET /v1/operator/feedback` with optional `session_id`, `operator_id`, `category`, and `limit` filters
- `GET /v1/operator/feedback/{id}`
- `POST /v1/operator/operators`
- `GET /v1/operator/operators`
- `GET /v1/operator/operators/{id}`
- `PUT /v1/operator/operators/{id}`
- `POST /v1/operator/operators/{id}/tokens`
- `POST /v1/operator/operators/{id}/tokens/{token_id}/revoke`
- `GET /v1/operator/customers/{customer_id}/preferences` with required `agent_id` and optional `status`, `key`, `source`, `include_expired`, and `limit` filters
- `GET /v1/operator/customers/{customer_id}/preferences/pending` with required `agent_id`
- `PUT /v1/operator/customers/{customer_id}/preferences/{key}`
- `POST /v1/operator/customers/{customer_id}/preferences/{key}/confirm`
- `POST /v1/operator/customers/{customer_id}/preferences/{key}/reject`
- `POST /v1/operator/customers/{customer_id}/preferences/{key}/expire`
- `GET /v1/operator/customers/{customer_id}/preference-events` with required `agent_id` and optional `key`, `source`, and `limit` filters
- `POST /v1/operator/agents`
- `GET /v1/operator/agents`
- `GET /v1/operator/agents/{id}`
- `PUT /v1/operator/agents/{id}`
- `POST /v1/operator/knowledge/sources`
- `GET /v1/operator/knowledge/sources`
- `GET /v1/operator/knowledge/sources/{id}`
- `POST /v1/operator/knowledge/sources/{id}/compile`
- `POST /v1/operator/knowledge/sources/{id}/resync`
- `GET /v1/operator/knowledge/sources/{id}/jobs`
- `GET /v1/operator/knowledge/jobs/{id}`
- `GET /v1/operator/knowledge/snapshots/{id}`
- `GET /v1/operator/knowledge/pages` with optional `scope_kind`, `scope_id`, `snapshot_id`, and `limit` filters
- `GET /v1/operator/knowledge/proposals` with optional `scope_kind` and `scope_id`
- `GET /v1/operator/knowledge/proposals/{id}`
- `GET /v1/operator/knowledge/proposals/{id}/preview`
- `POST /v1/operator/knowledge/proposals/{id}/state`
- `POST /v1/operator/knowledge/proposals/{id}/apply`
- `POST /v1/operator/knowledge/lint/run`
- `GET /v1/operator/knowledge/lint`
- `POST /v1/operator/knowledge/lint/{id}/resolve`
- `GET /v1/operator/media/assets` with optional `session_id`
- `GET /v1/operator/media/assets/{id}` with optional `session_id`
- `POST /v1/operator/media/assets/{id}/reprocess` with optional `session_id`
- `POST /v1/operator/media/assets/reprocess` with optional `session_id`, `status`, `type`, and `limit`
- media asset responses now surface `retry_count`, `next_retry_at`, `last_retry_at`, `enrichment_status`, and `error` as top-level fields
- `GET /v1/operator/media/signals` with optional `session_id`
- `POST /v1/tools/providers/register`
- `POST /v1/tools/providers/{id}/auth`
- `GET /v1/tools/providers/{id}/auth`
- `POST /v1/tools/providers/{id}/sync`
- `GET /v1/tools/providers`
- `GET /v1/tools/catalog`
- `GET /v1/executions`
- `GET /v1/executions/{id}`
- `GET /v1/executions/{id}/resolved-policy`

## Knowledge and Retrievers

Parmesan now has a first-pass agent knowledge workspace that follows the
LLM-wiki pattern without making Markdown the serving source of truth. Operators
register folder-backed knowledge sources, compile them into typed
`KnowledgePage`/`KnowledgeChunk` records, and publish immutable
`KnowledgeSnapshot` records. Runtime retrievers read from the active snapshot
and inject response-scoped grounding into the policy result; they do not mutate
policy, session memory, or the wiki during a customer turn.

Folder-backed sources require `KNOWLEDGE_SOURCE_ROOT`; paths outside that root
are rejected by the operator API. Retrieval now supports hybrid lexical plus
embedding search, with Postgres able to rank chunks via `pgvector` and memory
or fallback environments still using in-process ranking. Non-text ACP content
parts are persisted as media assets during ingest and now produce concrete
image/audio derived signals. Post-turn learning writes explicit low-risk
customer facts into first-class `CustomerPreference` records and records shared
knowledge as draft `KnowledgeUpdateProposal` records until an operator reviews
them. Operator feedback uses the same compiler path and can create customer
preferences, shared knowledge proposals, or draft policy/SOUL rollout proposals.
Preference learning now keeps explicit preferences active while routing inferred
signals through pending events for operator confirmation; explicit customer
statements can supersede older active values with preserved evidence. Policy and
SOUL rollout proposals now expose deterministic preview diffs and review gates
before promotion, while shared knowledge apply remains gated by lint findings
for high-risk citation, staleness, and contradiction issues. Proposal payloads
can target whole pages or section-level updates, and payload citations are
preserved into applied pages and chunks. Knowledge source resync now runs as an
asynchronous background job and exposes per-source job history/status.

Multimodal provider config:
- `OPENROUTER_API_KEY`
- `OPENROUTER_BASE_URL` (defaults to `https://openrouter.ai/api/v1`)
- `OPENROUTER_MULTIMODAL_MODEL`

OpenRouter-backed enrichers now support:
- images via `image_url`
- audio via `input_audio`
- PDFs via `file`
- video via `video_url`

If OpenRouter is unavailable or a modality call fails, Parmesan falls back to
local heuristic extraction for the supported media types.

## Agent Profiles and SOUL

Agent profiles bind an ACP `agent_id` to default policy and knowledge scopes.
Policy bundles may include a `soul` block for brand/persona settings such as
identity, role, tone, formality, verbosity, supported languages, formatting
rules, escalation style, and avoid rules. SOUL is injected as strong response
style guidance, but it does not override hard policy, strict templates,
approval requirements, tool constraints, or explicit customer constraints.
Operator agent profile reads include lightweight binding context such as
`soul_hash` and `active_session_count`.

Operator media inspection now exposes:
- per-asset signal drilldown
- asset filtering by `status` and `type`
- stored provenance such as provider, model, request IDs, and enrichment latency when available
- asset-level reprocessing for failed or outdated enrichment without replaying the whole turn
- batch reprocessing for filtered asset sets
- media retry/reprocess lifecycle emits traceable audit records like `media.retry.started`, `media.enrichment.succeeded`, and `media.reprocess.succeeded`
- `GET /v1/executions/{id}/tool-runs`
- `GET /v1/executions/{id}/delivery-attempts`
- `POST /v1/replays`
- `GET /v1/replays`
- `GET /v1/replays/{id}`
- `GET /v1/replays/{id}/diff`
- `GET /v1/traces` with optional `trace_id`, `session_id`, `execution_id`, `kind`, and `limit` filters
- `GET /v1/traces/{id}`

## Example Policy

See [`examples/policy.yaml`](examples/policy.yaml).

## Validation

Fast local validation:

```bash
go test -count=1 ./...
```

Performance regression check:

```bash
./scripts/bench_regression.sh
```

This runs the full local suite plus the main policy and end-to-end benchmarks:

- `BenchmarkResolveGoldenScenarios`
- `BenchmarkRunParmesanGoldenScenarios`
- `BenchmarkRunParmesanFullGoldenCorpus`

Use this as the normal guardrail for refactors and latency work. External
Parlant parity can stay as a periodic validation step, not the default inner
loop.

Live provider platform validation:

```bash
OPENROUTER_API_KEY=... ./scripts/live_platform_validation.sh
```

This runs the canonical end-to-end platform scenarios against a live provider:
e-commerce learning, pending preference review, Indonesian language preference,
and pet-store topic-scope quality. Reports are written to
`PLATFORM_VALIDATION_REPORT_DIR`, defaulting to
`/tmp/parmesan-platform-validation-live`, and include transcripts, provider
stats, learned preferences, proposal IDs, response-quality scorecards,
extracted claims, and evidence matches. The quality package also carries a
100-scenario production-readiness catalog used to track platform-wide quality
coverage across grounding, topic scope, preferences, multilingual behavior,
refusal/escalation, retrieval, tool/approval, SOUL, and failure-mode cases.
Inspect the catalog directly with:

```bash
go run ./cmd/quality-catalog -summary
go run ./cmd/quality-catalog -live-only
OPERATOR_API_KEY=... go run ./cmd/regression-export -base-url http://127.0.0.1:8080 -out artifacts/regression-fixtures.json
go run ./cmd/regression-seed -in artifacts/regression-fixtures.json -out artifacts/regression-scenario-seeds.json
QUALITY_SCENARIO_SEEDS=artifacts/regression-scenario-seeds.json go run ./cmd/quality-catalog -summary
go run ./cmd/quality-report-check -dir /tmp/parmesan-platform-validation-live -expect-scenarios ecommerce_knowledge_grounding_damaged_toaster_replacem,ecommerce_knowledge_grounding_refund_timing_question,pet_store_topic_scope_human_cooking_question,pet_store_topic_scope_pet_food_question,support_multilingual_english_fallback,support_multilingual_respond_in_indonesian,support_preference_call_me_rina,support_preference_prefer_email,support_refusal_escalation_operator_handoff,support_refusal_escalation_unsafe_request -min-overall 0.7
```

`quality-report-check` now applies stricter per-scenario minimums from the
catalog when they exceed the global `-min-overall` floor.
If `QUALITY_SCENARIO_SEEDS` points at a reviewed seed file, the catalog and
report checker merge those scenarios automatically, with matching IDs overriding
built-in expectations.

The script defaults reasoning, structured, and embedding providers to
OpenRouter; override `DEFAULT_REASONING_PROVIDER`,
`DEFAULT_STRUCTURED_PROVIDER`, and `DEFAULT_EMBEDDING_PROVIDER` if needed.

Operator quality review:

- `GET /v1/operator/quality/regressions` lists regression fixture candidates derived from labeled feedback.
- `POST /v1/operator/quality/regressions/{feedback_id}/state` marks a candidate as `candidate`, `accepted`, or `rejected`.
- `GET /v1/operator/quality/regressions/export` exports accepted fixtures as scenario-shaped review artifacts with derived `expected_quality` and `risk`.
- `go run ./cmd/regression-seed` converts that accepted export into `ScenarioExpectation`-shaped seed JSON for catalog review.
- `go run ./cmd/regression-export` fetches those exported regression fixtures from the operator API and writes a reviewable JSON artifact for catalog curation.

## ACP v1 Contract

ACP is exposed as a path-versioned public facade under `/v1/acp/...`.

Session shape:
- `id`
- `channel`
- `customer_id`
- `agent_id`
- `mode`
- `title`
- `metadata`
- `labels`
- `created_at`

Event shape:
- `id`
- `session_id`
- `source`
- `kind`
- `offset`
- `trace_id`
- `created_at`
- `content`
- `data`
- `metadata`
- `deleted`
- `execution_id`

Contract rules:
- `offset` and `trace_id` are generated by the server when omitted.
- ACP event listing and streaming are ordered by `offset` and resume via `min_offset`.
- Deleted events are excluded from ACP list and stream responses by default.
- ACP is a conversation/session facade; durable workflow truth remains in executions, journey instances, tool runs, and delivery attempts.
