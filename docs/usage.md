# Usage, Quotas, And Rate Limits

Parmesan records usage as an append-only ledger and enforces quota policies on
the same path. The first version is fixed-window enforcement for the scopes
that matter operationally:

- `customer`
- `agent`
- `organization`

The runtime records these metrics:

- `customer_turns`
- `model_requests`
- `input_tokens`
- `output_tokens`
- `total_tokens`
- `estimated_cost_micros`
- `tool_calls`

Quota windows are fixed `minute`, `hour`, `day`, or `month` windows. A
minute-level quota is the rate-limit form; longer windows are ordinary spend or
volume quotas.

## Policy Model

A quota policy has:

```json
{
  "id": "quota_customer_turns_per_minute",
  "scope_kind": "customer",
  "scope_id": "cust_123",
  "metric": "customer_turns",
  "window": "minute",
  "limit": 30,
  "enforcement": "block",
  "status": "active"
}
```

`scope_id` is optional. When it is omitted, the policy applies to every entity
of that scope kind. For example, a policy with `scope_kind: "agent"` and no
`scope_id` applies separately to each agent.

Supported enforcement modes:

- `block`: reserve usage atomically and reject the request when the limit would
  be exceeded
- `warn`: record a warned decision but still allow the request
- `allow_overage`: record over-limit usage without warning/blocking the caller

Disabled policies remain stored for auditability but are ignored by runtime
enforcement.

## Runtime Enforcement

Customer message ingress meters `customer_turns` before execution creation. A
blocked quota returns HTTP `429` from the ACP message endpoint and does not
create an automated turn.

The engine meters `model_requests` before each model call. If a request is
allowed, returned provider usage is recorded into input, output, total token,
and estimated-cost metrics when those values are available.

Tool calls are metered before invocation. If the quota blocks the call, the tool
run is persisted as failed and the external tool is not invoked. Completion
events with zero quantity are also written so the ledger can show successful or
failed outcomes without double-counting the reserved call.

## Organization Scope

Organization usage is resolved from the session metadata first, then the agent
profile metadata keys `org_id` or `organization_id`, then the runtime default.
The runtime default is taken from `observability.org_id`, or from the
environment fallback chain `PARMESAN_ORG_ID`,
`PARMESAN_OBSERVABILITY_ORG_ID`, `DEFAULT_ORG_ID`.

## Operator API

Quota policies:

- `POST /v1/operator/usage/quota-policies`
- `GET /v1/operator/usage/quota-policies`
- `GET /v1/operator/usage/quota-policies/{id}`
- `PUT /v1/operator/usage/quota-policies/{id}`
- `DELETE /v1/operator/usage/quota-policies/{id}`

Usage reads:

- `GET /v1/operator/usage/events`
- `GET /v1/operator/usage/summary`

List endpoints accept `limit`. Event and summary reads also accept `since` and
`until` as RFC3339 timestamps. Common filters include `scope_kind`, `scope_id`,
`metric`, `session_id`, `execution_id`, `provider`, `status`, and `window`
where applicable.

Operator `GET` usage routes require `operator.view`; mutating policy routes
require `operator.manage`.

## Database Upgrade Note

Usage quota storage uses `window_key` internally because `window` is a SQL
keyword. Fresh databases get that shape from migration 019. Databases that
already applied the earlier migration 019 shape are upgraded by migration 021,
which renames existing `window` columns on usage quota policies, buckets, and
events, then rebuilds the bucket summary index.
