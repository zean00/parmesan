# Parmesan

Parmesan is a customer-facing agent runtime focused on explicit policy control,
durable turn execution, multi-provider model routing, and first-class remote
tool catalogs.

## Project Lineage

Parmesan heavily borrows the core product and runtime ideas from
[Parlant](https://github.com/emcie-co/parlant), but it is not a 1:1 port.
Think of it as a related implementation that keeps the policy-governed
customer-agent shape while extending the system in a different direction.

Key additions compared to the original Parlant concept:

- feedback learning loop, inspired by Hermes-style agent learning workflows
- durable and resumable execution, inspired by Temporal-style agent execution
- ACP and MCP integration as first-class runtime surfaces
- MCP-only tool support
- external agent delegation through ACP

Licensed under the Apache License, Version 2.0. See [LICENSE](./LICENSE).

## Documentation

Use the root README as a repo entry point. Use the docs set for the detailed
reference.

Start with the curated documentation set:

- [Documentation Index](./docs/README.md)
- [Getting Started](./docs/getting-started.md)
- [Configuration](./docs/configuration.md)
- [Concepts](./docs/concepts.md)
- [Architecture](./docs/architecture.md)
- [Engine](./docs/engine.md)
- [Policies](./docs/policies.md)
- [Feedback Loop / Learning](./docs/feedback-learning.md)
- [Operations / Dashboard](./docs/operations-dashboard.md)

## Status

This repository currently contains a working end-to-end customer-facing agent
platform with:

- file-backed agent definitions and seeded knowledge
- ACP-first customer conversation APIs
- durable executions, traces, approvals, tool runs, and delivery tracking
- operator dashboard, notifications, test console, and trace workspace
- customer context enrichment
- feedback, learning, and maintainer flows
- Docker-based release-style deployment

## Quick Orientation

| If you need to... | Start here |
| --- | --- |
| boot the stack | [docs/getting-started.md](/home/sahal/workspace/agents/parmesan/docs/getting-started.md) |
| configure providers, MCP, ACP peers, or enrichment | [docs/configuration.md](/home/sahal/workspace/agents/parmesan/docs/configuration.md) |
| understand execution and policy behavior | [docs/engine.md](/home/sahal/workspace/agents/parmesan/docs/engine.md) and [docs/policies.md](/home/sahal/workspace/agents/parmesan/docs/policies.md) |
| operate sessions and traces | [docs/operations-dashboard.md](/home/sahal/workspace/agents/parmesan/docs/operations-dashboard.md) |
| understand learning and feedback | [docs/feedback-learning.md](/home/sahal/workspace/agents/parmesan/docs/feedback-learning.md) |

## Run

For a less dense step-by-step version of this section, use
[docs/getting-started.md](/home/sahal/workspace/agents/parmesan/docs/getting-started.md).

Release-style Docker deployment:

```bash
cp .env.example .env
# edit .env and set OPENROUTER_API_KEY before live LLM validation
docker compose up --build
```

This starts Postgres, applies migrations, bootstraps the sample live-support
agent from [agents/live_support.yaml](/home/sahal/workspace/agents/parmesan/agents/live_support.yaml),
registers markdown knowledge from [knowledge/live_support](/home/sahal/workspace/agents/parmesan/knowledge/live_support),
runs the API and worker, and serves the dashboard at
`http://127.0.0.1:${PARMESAN_DASHBOARD_PORT:-4173}`. The dashboard token
defaults to `dev-operator`.

The deployment image now ships with the default file-backed config, agent
definitions, and seeded knowledge already attached inside the container:

- `/config/parmesan.yaml`
- `/agents/*.yaml`
- `/knowledge/**`

That means the default compose path no longer depends on host bind mounts for
runtime config or bootstrap data. To change agent definitions, config, or
knowledge seeds, edit the files in this repo and rebuild the images.

Container behavior:

- `postgres`, `api`, `worker`, `dashboard`, and optional `nexus` use
  `restart: unless-stopped`
- `api` exposes a container healthcheck on `/healthz`
- `dashboard` healthchecks through nginx to the API `/healthz`
- `api` waits for `bootstrap` to complete successfully before starting
- `dashboard` waits for a healthy `api` before starting

File-backed runtime configuration lives in
[config/parmesan.yaml](/home/sahal/workspace/agents/parmesan/config/parmesan.yaml).
Environment variables still override file values, and `${VAR}` interpolation is
supported inside the config file.

Nexus can be attached as an optional compose profile when an image is available:

```bash
NEXUS_IMAGE=your-nexus-image docker compose --profile nexus up --build
```

Published ports can be adjusted from `.env`:

- `PARMESAN_API_PORT`
- `PARMESAN_DASHBOARD_PORT`

Manual local development:

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

Dashboard:

```bash
cd dashboard
npm install
npm run dev
```

The Vite dev server runs on `:4173` and proxies `/v1` plus `/healthz` to
`PARMESAN_API_URL` or `http://127.0.0.1:8080` by default.

ACP is the primary public conversation interface. The separate `gateway`
service remains available for legacy `/v1/web/...` clients while ACP-to-channel
adapters migrate externally.

## Core Runtime Model

Parmesan is built around:

- agent profiles with default policy and knowledge scope
- ACP sessions with normalized customer context
- durable turn executions with trace ids
- typed policy bundles authored in YAML
- compiled knowledge snapshots used for retrieval
- operator supervision, feedback, and governed learning

For the detailed explanation, use:

- [Concepts](./docs/concepts.md)
- [Architecture](./docs/architecture.md)
- [Engine](./docs/engine.md)
- [Policies](./docs/policies.md)

ACP session extension metadata should be sent via `_meta`. Parmesan preserves
that object and normalizes customer identity/details from `_meta.parmesan` or
`_meta.customer` into durable session `customer_context`; the older top-level
`metadata` field remains as a Parmesan compatibility alias.
Optional `customer_context.enrichment` config can enrich that context during
session creation from HTTP, PostgreSQL SQL, or static sources and inject only
configured prompt-safe fields at runtime.

External ACP agent peers can be registered in the global config and selected by
policy as a delegated capability:

```yaml
agent_servers:
  OpenCode:
    command: opencode
    args: ["acp", "--pure"]
    startup_timeout_seconds: 10
    request_timeout_seconds: 30
```

Policy bundles may expose a peer with `agents: [OpenCode]` on a guideline or
journey guideline, or require it with `agent: OpenCode` on a journey node.
`capability_isolation.allowed_agent_ids` can restrict which peers are eligible.
At runtime Parmesan selects one capability kind for the turn, so an external
agent peer competes with tools rather than running as an implicit workflow step.

Example live support bundle:

- [examples/live_support_policy.yaml](/home/sahal/workspace/agents/parmesan/examples/live_support_policy.yaml) is the strict customer-support bundle used for the validated Nexus to Parmesan ACP run.

Manual live setup used for Nexus validation:

```bash
PARMESAN_CONFIG=config/parmesan.yaml DATABASE_URL=postgres://midas:midas@localhost:5432/parmesan?sslmode=disable OPENROUTER_API_KEY=... OPERATOR_API_KEY=dev-operator go run ./cmd/migrate
PARMESAN_CONFIG=config/parmesan.yaml DATABASE_URL=postgres://midas:midas@localhost:5432/parmesan?sslmode=disable OPENROUTER_API_KEY=... OPERATOR_API_KEY=dev-operator PARMESAN_AGENTS_DIR=agents KNOWLEDGE_SOURCE_ROOT=knowledge go run ./cmd/bootstrap
PARMESAN_CONFIG=config/parmesan.yaml DATABASE_URL=postgres://midas:midas@localhost:5432/parmesan?sslmode=disable OPENROUTER_API_KEY=... OPERATOR_API_KEY=dev-operator HTTP_ADDR=127.0.0.1:8090 go run ./cmd/api
PARMESAN_CONFIG=config/parmesan.yaml DATABASE_URL=postgres://midas:midas@localhost:5432/parmesan?sslmode=disable OPENROUTER_API_KEY=... OPERATOR_API_KEY=dev-operator HTTP_ADDR=127.0.0.1:8091 go run ./cmd/worker
```

Operator endpoints support single-tenant RBAC. `OPERATOR_API_KEY` remains a
bootstrap admin credential; production operators can use stored operator API
tokens or trusted identity headers via `OPERATOR_TRUSTED_ID_HEADER` and
`OPERATOR_TRUSTED_ROLES_HEADER`.

Local OpenAI-compatible model backends such as LM Studio, Ollama, and
`llama.cpp` can now be targeted by setting `openai_base_url` / `OPENAI_BASE_URL`
to the local server. See
[docs/configuration.md](./docs/configuration.md) and
[docs/getting-started.md](./docs/getting-started.md) for the expected
Parmesan-side config shape and backend-side startup examples.

## API Endpoints
This repository exposes a large ACP, operator, policy, trace, knowledge, and
quality API surface. The canonical contract documentation now lives in:

- [ACP Contract](./docs/acp/README.md)
- [Operations / Dashboard](./docs/operations-dashboard.md)
- [Feedback Loop / Learning](./docs/feedback-learning.md)
- [Architecture](./docs/architecture.md)

Useful top-level endpoints to check first:

- `GET /healthz`
- `GET /v1/info`
- `POST /v1/acp/sessions`
- `POST /v1/acp/agents/{agent_id}/sessions`
- `GET /v1/operator/sessions`
- `GET /v1/operator/notifications`
- `GET /v1/traces/{id}`

## Knowledge, Learning, And Operations

Parmesan separates customer-facing execution from operator-governed learning:

- seeded markdown and registered sources compile into immutable knowledge snapshots
- runtime retrieval reads from active snapshots during execution
- feedback and conversation learning create preferences, knowledge proposals, and policy-oriented review artifacts
- operators inspect sessions, notifications, traces, control state, and learning outputs through the dashboard

Use the canonical docs for the current model and workflows:

- [Feedback Loop / Learning](./docs/feedback-learning.md)
- [Operations / Dashboard](./docs/operations-dashboard.md)
- [Getting Started](./docs/getting-started.md)

## Validation

Fast local validation:

```bash
go test -count=1 ./...
```

Live provider validation:

```bash
OPENROUTER_API_KEY=... ./scripts/live_platform_validation.sh
```

Deeper quality, regression, and release-gate workflows are documented in:

- [Feedback Loop / Learning](./docs/feedback-learning.md)
- [Getting Started](./docs/getting-started.md)

## Repository Guide

Primary directories:

- [`agents/`](./agents) sample agent definitions
- [`config/`](./config) file-backed runtime configuration
- [`knowledge/`](./knowledge) seeded markdown knowledge
- [`examples/`](./examples) reference policy bundles and examples
- [`dashboard/`](./dashboard) operator control panel
- [`docs/`](./docs) current project documentation
- [`cmd/`](./cmd) binaries such as `api`, `worker`, `bootstrap`, and `migrate`
- [`internal/`](./internal) runtime, policy, ACP, learning, and operator implementation
