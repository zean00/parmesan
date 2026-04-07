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
- Trace listing supports `trace_id`, `session_id`, `execution_id`, `kind`, and `limit` filters; `GET /v1/traces/{id}` returns the detailed timeline.
- If `OPERATOR_API_KEY` is configured, `/v1/operator/...` requires `Authorization: Bearer <token>` or `X-Operator-Token: <token>`.

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
