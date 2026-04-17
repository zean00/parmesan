# Orbyte + Nexus Integrated Validation

This integration pack validates one full local stack:

- `Nexus` as the webchat surface
- `Parmesan` as the ACP runtime and background worker
- `Orbyte full MCP` for direct product retrieval and lead creation
- `Orbyte minimal MCP` for delegated complaint intake and ticket-status polling
- `OpenCode` as the delegated ACP peer attached to Orbyte minimal MCP

## Included Assets

- `agents/orbyte_nexus_validation.yaml`
  - bootstrap agent profile for the integrated flow
- `policy/orbyte_nexus_validation_policy.yaml`
  - policy bundle for complaint delegation, proactive ticket updates, and product lead capture
- `config/parmesan.orbyte_nexus.yaml`
  - Parmesan runtime config template for both Orbyte MCP providers and the delegated OpenCode peer
- `conversations/integrated_validation.json.tmpl`
  - parameterized conversation script used by the live validator

## Expected Flow

1. User complains about a damaged product in Nexus.
2. Parmesan delegates to `OpenCodeOrbyteMinimal`.
3. OpenCode uses exact skill `crm_customer_complaint_ticket_intake`.
4. OpenCode returns a strict JSON envelope with ticket metadata.
5. Parmesan stores the ticket metadata, replies to the user, and creates a `crm_ticket_status` watch.
6. The validator resolves the ticket through Orbyte minimal MCP.
7. Parmesan lifecycle polling emits a proactive update back into the same Nexus session.
8. The user asks for product information.
9. Parmesan uses Orbyte full MCP directly for product retrieval and CRM lead creation.
10. The validator confirms learned preferences and created CRM artifacts.

## Delegation Contract Usage

This integration pack uses a `delegation_contract` for the complaint flow.

That means:

- `OpenCodeOrbyteMinimal` returns structured complaint result data
- Parmesan verifies that result against Orbyte MCP output
- Parmesan creates the ticket-status watch only after verification succeeds

So the complaint ticket contract in this pack is an integration-specific
instance of the generic delegation-contract feature, not special logic embedded
in the engine.

For the generic engine model, read [Delegation Contracts](../../docs/delegation-contracts.md).

## Durability Validation Note

This integration pack has also been used to validate durable retry/resume
behavior in the live stack.

The clearest proof path is the direct product inquiry flow:

1. send the product inquiry through Nexus
2. stop `orbyte_full` before the direct MCP tools execute
3. observe the Parmesan execution enter a retryable `waiting` state
4. restart `orbyte_full`
5. observe the same execution id resume and complete successfully

That proves durable execution across a real MCP dependency outage, not just a
clean happy-path flow.

One caveat from current behavior:

- the product inquiry flow is the best durability proof path
- the delegated complaint flow currently tends to fail soft when
  `orbyte_minimal` is unavailable, so it is not the best example of retryable
  blocked execution

## Runtime Variables

The Parmesan config template expects these environment variables:

- `PARMESAN_HTTP_ADDR`
- `PARMESAN_METRICS_ADDR`
- `DATABASE_URL`
- `SECRETS_MASTER_KEY`
- `OPERATOR_API_KEY`
- `PARMESAN_AGENTS_DIR`
- `ORBYTE_FULL_MCP_URL`
- `ORBYTE_MINIMAL_MCP_URL`
- `OPENCODE_COMMAND`
- `OPENCODE_MODEL`
- `DEFAULT_REASONING_PROVIDER`
- `DEFAULT_STRUCTURED_PROVIDER`
- `DEFAULT_EMBEDDING_PROVIDER`
- `OPENROUTER_API_KEY` or `OPENAI_API_KEY`

## Companion Commands

- live stack orchestration: `scripts/live_orbyte_nexus_validation.sh`
- live validator: `go run ./cmd/integration-orbyte-nexus-validate`

## Repo Boundary

This integration pack is intentionally outside the core engine:

- generic runtime behavior lives under `internal/engine/`
- Orbyte-specific adapters live under `internal/integrations/orbyte/`
- this folder contains the checked-in integration bundle and validation assets
- the validator command lives at `cmd/integration-orbyte-nexus-validate`

That split keeps the Orbyte + Nexus scenario from leaking back into core runtime code.
