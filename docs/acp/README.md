# ACP v1

ACP is exposed as a path-versioned public contract under `/v1/acp/...`.

Supported routes:
- `POST /v1/acp/sessions`
- `POST /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events/stream`

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
