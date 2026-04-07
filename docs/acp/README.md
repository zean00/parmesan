# ACP v1

ACP is exposed as a path-versioned public contract under `/v1/acp/...`.

Supported routes:
- `POST /v1/acp/sessions`
- `GET /v1/acp/sessions/{id}`
- `POST /v1/acp/sessions/{id}/messages`
- `POST /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events/stream`
- `GET /v1/acp/sessions/{id}/approvals`
- `POST /v1/acp/sessions/{id}/approvals/{approval_id}`

Conversation-edge rules:
- `POST /v1/acp/sessions/{id}/messages` is the primary turn-ingress endpoint and creates a durable execution plus the trigger event.
- If the session mode is `manual`, ACP message ingress persists and streams the customer message but does not create an automated execution.
- approval reads and responses should use the ACP session-scoped approval endpoints instead of the legacy `/v1/web/...` gateway surface.
- Operator supervision uses `/v1/operator/...`; operator notes are hidden from ACP list/stream responses.
- Operator session listing supports `customer_id`, `agent_id`, `mode`, `label`, `operator_id`, `active=true`, and `limit` filters.
- Operator event listing supports `min_offset`, `limit`, `source`, `trace_id`, and `kind` filters.
- Operator feedback uses `POST /v1/operator/sessions/{id}/feedback` and can compile into customer preferences, knowledge proposals, or draft policy/SOUL proposals.
- Operator customer preferences are available under `/v1/operator/customers/{customer_id}/preferences?agent_id=...`.
- Trace listing supports `trace_id`, `session_id`, `execution_id`, `kind`, and `limit` filters; `GET /v1/traces/{id}` returns the detailed timeline.
- If `OPERATOR_API_KEY` is configured, `/v1/operator/...` requires `Authorization: Bearer <token>` or `X-Operator-Token: <token>`.

Knowledge workspace routes:
- `POST /v1/operator/agents`
- `GET /v1/operator/agents`
- `GET /v1/operator/agents/{id}`
- `PUT /v1/operator/agents/{id}`
- `POST /v1/operator/knowledge/sources`
- `POST /v1/operator/knowledge/sources/{id}/compile`
- `GET /v1/operator/knowledge/snapshots/{id}`
- `GET /v1/operator/knowledge/pages`
- `GET /v1/operator/knowledge/proposals`
- `GET /v1/operator/knowledge/proposals/{id}`
- `GET /v1/operator/knowledge/proposals/{id}/preview`
- `POST /v1/operator/knowledge/proposals/{id}/state`
- `POST /v1/operator/knowledge/proposals/{id}/apply`
- `GET /v1/operator/media/assets`
- `GET /v1/operator/media/assets/{id}`
- `POST /v1/operator/media/assets/{id}/reprocess`
- `POST /v1/operator/media/assets/reprocess`
- Media asset responses expose retry state directly: `retry_count`, `next_retry_at`, `last_retry_at`, `enrichment_status`, and `error`.
- `GET /v1/operator/media/signals`

Knowledge rules:
- Agent profiles bind an ACP `agent_id` to a default policy bundle and default knowledge scope.
- Operator agent profile reads include `soul_hash` and `active_session_count` when derivable.
- Policy bundles can carry a `soul` block for identity, brand, language, tone, formatting, escalation, and avoid rules.
- SOUL is injected as strong response style guidance, but hard policy, strict templates, approval/tool constraints, and explicit customer constraints take precedence.
- Folder sources require `KNOWLEDGE_SOURCE_ROOT` and cannot point outside that root.
- Compiled wiki pages and chunks are stored as typed records; Markdown files are source input, not runtime truth.
- Runtime retrievers inject response-scoped grounding from immutable knowledge snapshots and must not mutate policy or wiki state during ACP turn processing.
- Non-text ACP content parts are treated as media assets; image/audio parts now produce derived signals like OCR text, summaries, labels, transcripts, and language hints.
- Retrieval prefers customer-scoped `customer_agent` knowledge when available, then falls back to shared agent or bundle knowledge.
- Shared conversation learning creates draft knowledge proposals; low-risk customer facts update first-class customer preferences directly.
- Policy and SOUL changes inferred from operator feedback always become draft rollout proposals and never auto-apply.
- Proposal review supports explicit `draft`, `approved`, `rejected`, and `applied` states, plus a preview surface before apply.
- OpenRouter multimodal enrichers are used when configured:
  - images use `image_url`
  - audio uses `input_audio`
  - PDFs use `file`
  - video uses `video_url`
- `OPENROUTER_MULTIMODAL_MODEL` can override the default multimodal model selection.
- Media assets can be inspected by `status` and `type`, and individual asset drilldowns include the derived signals plus stored enrichment provenance.
- Failed or outdated media assets can be reprocessed directly through the operator API without re-running the full conversation turn.
- Filtered media batches can also be reprocessed in one request for operator recovery workflows.
- Automatic retries and operator reprocess operations are also written into the trace/audit stream for operator debugging.

Core event families:
- `message`
- `status`
- `approval.requested`
- `approval.resolved`
- `tool.started`
- `tool.completed`
- `tool.failed`
- `tool.blocked`

Contract rules:
- `offset` and `trace_id` are server-generated when omitted.
- ACP list and stream resume by `min_offset`.
- ACP list and stream exclude deleted events by default.
- Event ordering is ascending by `(offset, created_at)` within a session.
- `data` and `metadata` remain extensibility fields, but each core event family has required typed fields.

Schemas:
- [`schemas/session.json`](schemas/session.json)
- [`schemas/event-base.json`](schemas/event-base.json)
- [`schemas/event-message.json`](schemas/event-message.json)
- [`schemas/event-status.json`](schemas/event-status.json)
- [`schemas/event-approval-requested.json`](schemas/event-approval-requested.json)
- [`schemas/event-approval-resolved.json`](schemas/event-approval-resolved.json)
- [`schemas/event-tool-started.json`](schemas/event-tool-started.json)
- [`schemas/event-tool-completed.json`](schemas/event-tool-completed.json)
- [`schemas/event-tool-failed.json`](schemas/event-tool-failed.json)
- [`schemas/event-tool-blocked.json`](schemas/event-tool-blocked.json)
