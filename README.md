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
go run ./cmd/gateway
go run ./cmd/worker
```

Default ports:

- `api`: `:8080`
- `gateway`: `:8081`
- `worker`: `:8082`

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
- `POST /v1/tools/providers/register`
- `POST /v1/tools/providers/{id}/auth`
- `GET /v1/tools/providers/{id}/auth`
- `POST /v1/tools/providers/{id}/sync`
- `GET /v1/tools/providers`
- `GET /v1/tools/catalog`
- `GET /v1/executions`
- `GET /v1/executions/{id}`
- `GET /v1/executions/{id}/resolved-policy`
- `GET /v1/executions/{id}/tool-runs`
- `GET /v1/executions/{id}/delivery-attempts`
- `POST /v1/replays`
- `GET /v1/replays`
- `GET /v1/replays/{id}`
- `GET /v1/replays/{id}/diff`
- `GET /v1/traces`

## Example Policy

See [`examples/policy.yaml`](examples/policy.yaml).
