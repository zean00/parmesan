# Getting Started

This guide gets Parmesan running with the stock live-support agent, seeded
knowledge, API, worker, and dashboard.

## Before You Start

For the default path, you only need:

- Docker and Docker Compose
- a valid `OPENROUTER_API_KEY`
- values for `SECRETS_MASTER_KEY` and `OPERATOR_API_KEY`

If you want the lower-level reference while following this guide, keep
[Configuration](./configuration.md) open in another tab.

## What You Get

The default deployment starts:

- PostgreSQL with pgvector
- database migrations
- bootstrap of file-backed agent definitions
- file-backed seeded knowledge registration
- API
- worker
- operator dashboard

The stock sample is:

- agent definition: `agents/live_support.yaml`
- policy bundle: `examples/live_support_policy.yaml`
- seeded knowledge: `knowledge/live_support/`

## Fast Path Summary

1. copy `.env.example` to `.env`
2. set the three required secrets
3. run `docker compose up --build`
4. open the dashboard and sign in with `OPERATOR_API_KEY`

## Fastest Path: Docker Compose

1. Copy the environment template:

```bash
cp .env.example .env
```

2. Edit `.env` and set at least:

- `OPENROUTER_API_KEY`
- `SECRETS_MASTER_KEY`
- `OPERATOR_API_KEY`

3. Start the stack:

```bash
docker compose up --build
```

4. Open:

- dashboard: `http://127.0.0.1:4173`
- API: `http://127.0.0.1:8080`

The default dashboard token is the value of `OPERATOR_API_KEY`.

### Operator Sign-In

When the dashboard opens, it first shows the operator token gate. Use the same
bearer token configured as `OPERATOR_API_KEY`.

![Dashboard token gate](./assets/dashboard-token-gate.png)

## What The Default Compose Flow Does

The compose deployment uses the baked-in files from the image:

- `/config/parmesan.yaml`
- `/agents/*.yaml`
- `/knowledge/**`
- `/examples/**`

Startup sequence:

1. `postgres`
2. `migrate`
3. `bootstrap`
4. `api` and `worker`
5. `dashboard`

## Local Development

The backend commands expect a reachable Postgres database plus the same
environment inputs used by the compose path.

Typical local sequence:

```bash
export PARMESAN_CONFIG=config/parmesan.yaml
export DATABASE_URL=postgres://parmesan:parmesan@localhost:5432/parmesan?sslmode=disable
export SECRETS_MASTER_KEY=change-me-32-byte-development-key
export OPERATOR_API_KEY=dev-operator
export OPENROUTER_API_KEY=...

go run ./cmd/migrate
go run ./cmd/bootstrap
go run ./cmd/api
go run ./cmd/worker
```

### Local OpenAI-Compatible Models

If you want to run against a local OpenAI-compatible backend instead of the
hosted OpenRouter path, the compatible targets are:

- LM Studio
- Ollama
- llama.cpp `llama-server`

Backend examples:

LM Studio:

```bash
lms server start --port 1234
```

Ollama:

```bash
ollama serve
```

llama.cpp:

```bash
llama-server -m /models/model.gguf --port 8080
```

Parmesan-side config shape:

```yaml
providers:
  openai_api_key: "local-dev"
  openai_base_url: "http://127.0.0.1:1234/v1"
  default_reasoning: openai
  default_structured: openai
  default_embedding: openai

tool_providers:
  allow_local_dev: true
```

Then point the model provider base URL at one of:

- `http://127.0.0.1:1234/v1` for LM Studio
- `http://127.0.0.1:11434/v1` for Ollama
- `http://127.0.0.1:8080/v1` for llama.cpp

You can switch the `openai_base_url` value to:

- `http://127.0.0.1:1234/v1` for LM Studio
- `http://127.0.0.1:11434/v1` for Ollama
- `http://127.0.0.1:8080/v1` for llama.cpp

Run the dashboard separately:

```bash
cd dashboard
npm install
npm run dev
```

## Key Runtime Files

- runtime config: `config/parmesan.yaml`
- example config: `config/parmesan.example.yaml`
- agent definitions: `agents/*.yaml`
- seeded knowledge: `knowledge/**`

Relevant local path overrides:

- `PARMESAN_CONFIG`
- `PARMESAN_AGENTS_DIR`
- `KNOWLEDGE_SOURCE_ROOT`
- `TOOL_PROVIDER_ALLOW_LOCAL_DEV`
- `TOOL_PROVIDER_ALLOWED_HOSTS`

## Implementation References

- config loading and env override: `internal/config/config.go`
- bootstrap entrypoint: `cmd/bootstrap/main.go`
- migrate entrypoint: `cmd/migrate/main.go`
- compose deployment: `docker-compose.yml`
- container packaging: `Dockerfile`

## First Validation Checks

After startup, check:

- `GET /healthz`
- dashboard loads successfully
- the `live_support` agent appears in the agent list
- the test console can create an ACP session
- the session workspace shows traces and events for a test conversation

The normal landing page after sign-in is the session inbox:

![Dashboard session inbox](./assets/dashboard-sessions-inbox.png)
