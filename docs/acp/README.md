# ACP v1

ACP is exposed as a path-versioned public contract under `/v1/acp/...`.

Supported routes:
- `POST /v1/acp/agents/{agent_id}/sessions`
- `GET /v1/acp/agents/{agent_id}/sessions/{id}`
- `POST /v1/acp/agents/{agent_id}/sessions/{id}/messages`
- `POST /v1/acp/agents/{agent_id}/sessions/{id}/events`
- `GET /v1/acp/agents/{agent_id}/sessions/{id}/events`
- `GET /v1/acp/agents/{agent_id}/sessions/{id}/events/stream`
- `GET /v1/acp/agents/{agent_id}/sessions/{id}/approvals`
- `POST /v1/acp/agents/{agent_id}/sessions/{id}/approvals/{approval_id}`
- `POST /v1/acp/sessions`
- `GET /v1/acp/sessions/{id}`
- `POST /v1/acp/sessions/{id}/messages`
- `POST /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events`
- `GET /v1/acp/sessions/{id}/events/stream`
- `GET /v1/acp/sessions/{id}/approvals`
- `POST /v1/acp/sessions/{id}/approvals/{approval_id}`

Conversation-edge rules:
- Agent-scoped routes are the preferred multi-profile ACP surface. The path `{agent_id}` is authoritative; a mismatched body `agent_id` is rejected, and existing session reads/writes return not found if the persisted session belongs to another agent.
- Session creation accepts ACP extension metadata through `_meta`. Parmesan preserves `_meta` in session metadata and normalizes `_meta.parmesan.customer`, `_meta.customer`, and related `customer_id` fields into `metadata.customer_context`.
- Agent-scoped session creation assigns a stable anonymous `customer_id` when `_meta` and Parmesan compatibility fields do not provide customer identity, preserving customer-scoped context without requiring a Parmesan-specific ACP field.
- `POST /v1/acp/sessions/{id}/messages` is the primary turn-ingress endpoint and creates or coalesces a durable execution plus the trigger event.
- Quick successive customer messages are coalesced for `ACP_RESPONSE_COALESCE_MS` milliseconds (default `1500`, set `0` to disable) while the execution is still safe to merge before response composition.
- One execution can emit multiple ordered `ai_agent` message events when a strict template defines `messages: [...]` or generation returns a bounded JSON `messages` array; each event carries response-batch metadata while compatibility status events keep the first `event_id`.
- If the session mode is `manual`, ACP message ingress persists and streams the customer message but does not create an automated execution.
- approval reads and responses should use the ACP session-scoped approval endpoints instead of the legacy `/v1/web/...` gateway surface.
- Operator supervision uses `/v1/operator/...`; operator notes are hidden from ACP list/stream responses.
- Operator session listing supports customer, agent, mode, label, assignment, pending approval, failed media, unresolved lint, activity-window, cursor, and limit filters.
- Operator event listing supports `min_offset`, `limit`, `source`, `trace_id`, and `kind` filters.
- Operator feedback uses `POST /v1/operator/sessions/{id}/feedback` and can compile into customer preferences, knowledge proposals, or draft policy/SOUL proposals.
- Operator customer preferences are available under `/v1/operator/customers/{customer_id}/preferences?agent_id=...`; lifecycle actions use `/confirm`, `/reject`, and `/expire` on a preference key.
- Trace listing supports `trace_id`, `session_id`, `execution_id`, `kind`, and `limit` filters; `GET /v1/traces/{id}` returns the detailed timeline.
- `/v1/operator/...` supports single-tenant RBAC with stored operator API tokens, trusted identity headers, and `OPERATOR_API_KEY` as bootstrap admin fallback.
- Executions can wait durably with persisted retry metadata, block on approval resume signals, and be recovered by operators with `POST /v1/operator/executions/{id}/retry`, `/unblock`, or `/abandon`.
- Planned independent tools run in parallel; approval-required tools still block before invocation and successful tool outputs are reused by idempotency key on resume.

Knowledge workspace routes:
- `POST /v1/operator/operators`
- `GET /v1/operator/operators`
- `GET /v1/operator/operators/{id}`
- `PUT /v1/operator/operators/{id}`
- `POST /v1/operator/operators/{id}/tokens`
- `POST /v1/operator/operators/{id}/tokens/{token_id}/revoke`
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
- `GET /v1/operator/knowledge/pages`
- `GET /v1/operator/knowledge/proposals`
- `GET /v1/operator/knowledge/proposals/{id}`
- `GET /v1/operator/knowledge/proposals/{id}/preview`
- `POST /v1/operator/knowledge/proposals/{id}/state`
- `POST /v1/operator/knowledge/proposals/{id}/apply`
- `POST /v1/operator/knowledge/lint/run`
- `GET /v1/operator/knowledge/lint`
- `POST /v1/operator/knowledge/lint/{id}/resolve`
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
- Inferred preference signals are reviewable and are not injected into runtime responses until confirmed active; explicit customer statements can supersede older active values.
- Policy and SOUL changes inferred from operator feedback always become draft rollout proposals and never auto-apply.
- Policy proposal preview returns deterministic diffs for guideline, journey, template, tool-policy, and SOUL changes before review or rollout.
- Knowledge source resync is asynchronous and returns a background sync job instead of compiling inline.
- Knowledge lint findings are surfaced during preview/apply; unresolved high-risk citation, staleness, or contradiction findings block apply.
- Knowledge proposals can apply whole-page or section-level changes; payload citations are preserved into applied pages and chunks.
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
- Live platform validation can be run with `OPENROUTER_API_KEY=... ./scripts/live_platform_validation.sh`; the script prints the quality catalog summary from `go run ./cmd/quality-catalog -summary`, verifies domain-specific launch packs with `go run ./cmd/quality-domain-pack -fail`, checks reports with `go run ./cmd/quality-report-check` using a default `0.7` minimum overall floor plus stricter per-scenario catalog thresholds where defined, and writes JSON reports to `PLATFORM_VALIDATION_REPORT_DIR` or `/tmp/parmesan-platform-validation-live` with response-quality scorecards, extracted claims, evidence matches, and release-snapshot artifacts. High-risk built-in scenarios currently require at least `0.85` overall.
- Labeled operator feedback now creates reviewable regression fixture candidates that can be listed at `GET /v1/operator/quality/regressions` and marked `candidate`, `accepted`, or `rejected` with `POST /v1/operator/quality/regressions/{feedback_id}/state`.
- Accepted regression fixtures can be exported at `GET /v1/operator/quality/regressions/export` to seed deterministic scenario packs and future catalog curation.
- `OPERATOR_API_KEY=... go run ./cmd/regression-export -base-url http://127.0.0.1:8080 -out artifacts/regression-fixtures.json` pulls those accepted fixtures into a reviewable JSON file.
- `go run ./cmd/regression-seed -in artifacts/regression-fixtures.json -out artifacts/regression-scenario-seeds.json` converts that reviewable export into catalog-seed scenario entries.
- `go run ./cmd/regression-seed -in artifacts/regression-fixtures.json -out artifacts/regression-scenario-seeds.json -promote-live scenario_id` promotes reviewed seed IDs into the catalog-driven live gate.
- `go run ./cmd/quality-seed-check -in artifacts/regression-scenario-seeds.json` validates reviewed seed entries before they are merged into the catalog.
- Setting `QUALITY_SCENARIO_SEEDS=artifacts/regression-scenario-seeds.json` causes the quality catalog and report checker to merge reviewed seed scenarios automatically.
- `./scripts/live_platform_validation.sh` auto-detects `artifacts/regression-scenario-seeds.json`, validates it, exports `QUALITY_SCENARIO_SEEDS`, and derives the expected live-gate scenario IDs from `go run ./cmd/quality-catalog -live-only -ids`.
- `go run ./cmd/quality-live-diff` shows the difference between built-in live-gate scenarios and the merged live-gate set after reviewed seeds are applied.
- `go run ./cmd/quality-release-snapshot -dir /tmp/parmesan-platform-validation-live -out artifacts/quality-release-snapshot.json` freezes the latest live-gate evidence into one artifact with the merged live-gate set, live diff, report summary, and aggregated provider stats.
- `./scripts/live_platform_validation.sh` writes that snapshot automatically to `QUALITY_RELEASE_SNAPSHOT_OUT`, defaulting to `artifacts/quality-release-snapshot.json`, after a passing report check.
- `go run ./cmd/quality-release-history -dir artifacts/quality-release-history -require-consecutive 3` checks archived release snapshots and enforces a configurable consecutive-clean-run requirement.
- `go run ./cmd/quality-release-trend -dir artifacts/quality-release-history` compares the latest archived snapshot to the previous one and reports score, pass/fail, and provider-health deltas.
- `./scripts/live_platform_validation.sh` now archives each passing snapshot into `QUALITY_RELEASE_HISTORY_DIR`, defaulting to `artifacts/quality-release-history`, and runs the history check with `QUALITY_RELEASE_REQUIRE_CONSECUTIVE_CLEAN`.
- `./scripts/live_platform_validation.sh` also runs the release-trend report after archiving the latest snapshot.
- The quality catalog now carries 200 deterministic scenarios and 100 catalog-driven live-gate scenarios for the release path.

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
