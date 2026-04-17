#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ORBYTE_ROOT="${ORBYTE_ROOT:-$HOME/workspace/orbyte}"
NEXUS_ROOT="${NEXUS_ROOT:-$HOME/workspace/agents/nexus}"
RUN_DIR="${RUN_DIR:-/tmp/parmesan-orbyte-nexus-validation}"
mkdir -p "$RUN_DIR"

ORBYTE_ADMIN_PASSWORD="${ORBYTE_ADMIN_PASSWORD:-admin123!}"
SECRETS_MASTER_KEY="${SECRETS_MASTER_KEY:-change-me-32-byte-development-key}"
OPERATOR_API_KEY="${OPERATOR_API_KEY:-dev-operator}"
DEFAULT_REASONING_PROVIDER="${DEFAULT_REASONING_PROVIDER:-openrouter}"
DEFAULT_STRUCTURED_PROVIDER="${DEFAULT_STRUCTURED_PROVIDER:-$DEFAULT_REASONING_PROVIDER}"
DEFAULT_EMBEDDING_PROVIDER="${DEFAULT_EMBEDDING_PROVIDER:-$DEFAULT_REASONING_PROVIDER}"
OPENCODE_COMMAND="${OPENCODE_COMMAND:-opencode}"
OPENCODE_MODEL="${OPENCODE_MODEL:-openrouter/openai/gpt-4.1-mini}"
OPENCODE_HOME_ROOT="${OPENCODE_HOME_ROOT:-$RUN_DIR/opencode}"
OPENCODE_HOME_ORBYTE_FULL="${OPENCODE_HOME_ORBYTE_FULL:-$OPENCODE_HOME_ROOT/orbyte-full}"
OPENCODE_HOME_ORBYTE_MINIMAL="${OPENCODE_HOME_ORBYTE_MINIMAL:-$OPENCODE_HOME_ROOT/orbyte-minimal}"
OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL="${OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL:-$OPENCODE_HOME_ORBYTE_FULL/.config}"
OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL="${OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL:-$OPENCODE_HOME_ORBYTE_MINIMAL/.config}"

ORBYTE_FULL_ADDR="${ORBYTE_FULL_ADDR:-127.0.0.1:18110}"
ORBYTE_MINIMAL_ADDR="${ORBYTE_MINIMAL_ADDR:-127.0.0.1:18111}"
PARMESAN_HTTP_ADDR="${PARMESAN_HTTP_ADDR:-127.0.0.1:18090}"
PARMESAN_METRICS_ADDR="${PARMESAN_METRICS_ADDR:-127.0.0.1:19090}"
PARMESAN_WORKER_HTTP_ADDR="${PARMESAN_WORKER_HTTP_ADDR:-127.0.0.1:18091}"
PARMESAN_WORKER_METRICS_ADDR="${PARMESAN_WORKER_METRICS_ADDR:-127.0.0.1:19091}"
NEXUS_ADDR="${NEXUS_ADDR:-127.0.0.1:18082}"
NEXUS_ADMIN_ADDR="${NEXUS_ADMIN_ADDR:-127.0.0.1:18083}"

ORBYTE_FULL_BASE_URL="http://${ORBYTE_FULL_ADDR}"
ORBYTE_MINIMAL_BASE_URL="http://${ORBYTE_MINIMAL_ADDR}"
PARMESAN_BASE_URL="http://${PARMESAN_HTTP_ADDR}"
NEXUS_BASE_URL="http://${NEXUS_ADDR}"

ORBYTE_FULL_DATABASE_URL="${ORBYTE_FULL_DATABASE_URL:-postgres://orbyte:orbyte@127.0.0.1:55432/orbyte_full_validation?sslmode=disable}"
ORBYTE_MINIMAL_DATABASE_URL="${ORBYTE_MINIMAL_DATABASE_URL:-postgres://orbyte:orbyte@127.0.0.1:55432/orbyte_minimal_validation?sslmode=disable}"
PARMESAN_DATABASE_URL="${PARMESAN_DATABASE_URL:-postgres://parmesan:parmesan@127.0.0.1:5432/parmesan_validation?sslmode=disable}"

ORBYTE_FULL_MCP_URL="${ORBYTE_FULL_MCP_URL:-${ORBYTE_FULL_BASE_URL}/mcp}"
ORBYTE_MINIMAL_MCP_URL="${ORBYTE_MINIMAL_MCP_URL:-${ORBYTE_MINIMAL_BASE_URL}/mcp}"
CRM_MANIFEST_FULL="${CRM_MANIFEST_FULL:-$RUN_DIR/orbyte-full-crm-seed.json}"
CRM_MANIFEST_MINIMAL="${CRM_MANIFEST_MINIMAL:-$RUN_DIR/orbyte-minimal-crm-seed.json}"
SHOWCASE_MANIFEST_FULL="${SHOWCASE_MANIFEST_FULL:-$RUN_DIR/orbyte-full-showcase-seed.json}"
REPORT_OUT="${REPORT_OUT:-$RUN_DIR/integrated-validation-report.json}"

PARMESAN_CONFIG="${PARMESAN_CONFIG:-$ROOT/integrations/orbyte_nexus/config/parmesan.orbyte_nexus.yaml}"
PARMESAN_AGENTS_DIR="${PARMESAN_AGENTS_DIR:-$ROOT/integrations/orbyte_nexus/agents}"
VALIDATION_AGENT_ID="${VALIDATION_AGENT_ID:-agent_orbyte_nexus_validation}"
VALIDATION_SCRIPT="${VALIDATION_SCRIPT:-$ROOT/integrations/orbyte_nexus/conversations/integrated_validation.json.tmpl}"
COMPLAINT_MCP_URL="${COMPLAINT_MCP_URL:-$ORBYTE_MINIMAL_MCP_URL}"

cleanup() {
  set +e
  for pidfile in "$RUN_DIR"/*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      sleep 1
      kill -9 "$pid" >/dev/null 2>&1 || true
    fi
    rm -f "$pidfile"
  done
}
trap cleanup EXIT

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

wait_http() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      echo "$name is ready at $url"
      return 0
    fi
    sleep 1
  done
  echo "$name did not become ready at $url" >&2
  return 1
}

write_opencode_config() {
  local home_dir="$1"
  local xdg_config_home="$2"
  local server_name="$3"
  local server_url="$4"
  local config_dir="${xdg_config_home}/opencode"
  mkdir -p "$config_dir"
  python3 - <<'PY' "$config_dir/opencode.json" "$server_name" "$server_url"
import json
import os
import sys

config_path, server_name, server_url = sys.argv[1:4]
snippet = {
    "$schema": "https://opencode.ai/config.json",
    "permission": {"*": "allow"},
    "mcp": {
        server_name: {
            "type": "remote",
            "url": server_url,
            "enabled": True,
            "timeout": 120000,
        }
    },
}
with open(config_path, "w", encoding="utf-8") as fh:
    json.dump(snippet, fh, indent=2)
    fh.write("\n")
PY
}

start_bg() {
  local name="$1"
  local workdir="$2"
  shift 2
  local logfile="$RUN_DIR/${name}.log"
  (
    cd "$workdir"
    "$@" >"$logfile" 2>&1
  ) &
  local pid=$!
  echo "$pid" >"$RUN_DIR/${name}.pid"
}

orbyte_login() {
  local base_url="$1"
  local cookie_jar="$2"
  local csrf_file="$3"
  curl -fsS -c "$cookie_jar" -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"${ORBYTE_ADMIN_PASSWORD}\"}" "${base_url}/auth/login" >/dev/null
  python3 - <<'PY' "$cookie_jar" "$csrf_file"
import sys
token = ""
with open(sys.argv[1], "r", encoding="utf-8") as fh:
    for line in fh:
        if line.startswith("#HttpOnly_"):
            line = line[len("#HttpOnly_"):]
        elif line.startswith("#"):
            continue
        parts = line.rstrip("\n").split("\t")
        if len(parts) >= 7 and parts[5] == "orbyte_csrf":
            token = parts[6]
            break
if not token:
    raise SystemExit("missing orbyte_csrf cookie after login")
open(sys.argv[2], "w", encoding="utf-8").write(token)
PY
}

orbyte_set_mcp_mode() {
  local base_url="$1"
  local mode="$2"
  local cookie_jar="$3"
  local csrf_token
  csrf_token="$(cat "$4")"
  local payload
  payload="$(
    python3 - "$mode" <<'PY'
import json
import sys

mode = sys.argv[1]
value = {
    "enabled": True,
    "exposure_mode": mode,
    "discovery_mode": "keyword",
    "tool_discovery_mode": "",
    "playbook_discovery_mode": "",
    "discovery_indexing_enabled": True,
    "governance_enabled": True,
    "default_action_mode": "draft_only",
    "tool_states_json": "{}",
    "blocked_action_classes_json": "[]",
    "blocked_tool_keys_json": "[]",
    "blocked_document_types_json": "[]",
    "allowed_submit_document_types_json": "[]",
    "domain_policy_overrides_json": "{}",
    "default_capabilities_json": "[]",
    "playbooks_json": "[]",
}
if mode == "minimal":
    value["playbooks_json"] = json.dumps([
        {
            "id": "crm_customer_complaint_ticket_intake",
            "name": "CRM Customer Complaint Ticket Intake",
            "description": "Capture a customer complaint as a CRM service ticket after resolving the customer, checking for duplicates, and routing it to the right queue and severity.",
            "domains": ["crm", "service"],
            "labels": ["crm", "ticket-intake", "complaint", "service-desk"],
            "keywords": ["customer complaint", "create ticket", "support case", "issue intake", "service ticket", "complain ticket"],
            "use_when": "The user or connected customer service agent wants to turn a customer complaint, service issue, refund problem, damaged order, or support escalation into a CRM ticket.",
            "workflow_steps": [
                {"step": "resolve_customer_context", "title": "Resolve Customer Context", "tool_id": "crm.customer.summary", "required": True, "description": "Resolve the named customer or party first so the complaint is attached to the correct CRM account context. Execute this step first and wait for the resolved customer context before any duplicate checks.", "output": "Customer account, profile, and party id."},
                {"step": "check_existing_tickets", "title": "Check Existing Tickets", "tool_id": "crm.ticket.search", "required": True, "description": "After the customer context is resolved, search for existing open tickets for the same customer and complaint pattern before creating a new ticket. Execute this after resolve_customer_context, not in parallel with it.", "output": "Possible duplicate tickets, current queue, and ticket status."},
                {"step": "decide_queue_priority_and_severity", "title": "Decide Queue, Priority, And Severity", "required": True, "description": "Only after the customer and duplicate-ticket checks are completed, choose the right queue, priority, severity, and issue category based on the complaint type and urgency."},
                {"step": "create_ticket", "title": "Create Ticket", "tool_id": "crm.ticket.create", "required": False, "when": "Use only when the customer is resolved, no matching open ticket already covers the same complaint, and the user or automation policy explicitly wants ticket creation.", "description": "Create the CRM complaint ticket with title, description, customer, queue, priority, severity, and source channel.", "output": "Created ticket id and queue assignment context."},
                {"step": "confirm_ticket_reference", "title": "Confirm Ticket Reference", "required": False, "when": "Use immediately after create_ticket when a new complaint ticket was created.", "description": "Confirm the created complaint ticket before you finish. First inspect the create_ticket output carefully. If it returned a human ticket number such as CRM-..., treat that as a ticket number, not as the internal record id. First call crm.ticket.get only when you have a real internal ticket id. If crm.ticket.get fails, or create_ticket only returned a human ticket number, immediately run crm.ticket.search using the returned ticket number as the primary query plus the resolved customer or party context and selected queue. Do not restrict the recovery search to status=open because newly created tickets can still be new or another initial status. If that still fails, broaden crm.ticket.search with the recent complaint wording and the same customer and queue context to recover the created ticket. Do not continue until you have both a real ticket id and ticket number from MCP output.", "output": "Verified ticket id, ticket number, status, and queue context from MCP output."},
                {"step": "assign_ticket", "title": "Assign Ticket", "tool_id": "crm.ticket.assign", "required": False, "when": "Use when the workflow wants to route the complaint immediately to a named agent or owner after creation.", "description": "Assign or reassign the complaint ticket to the correct owner.", "output": "Updated ticket assignee and routing note."},
                {"step": "capture_original_complaint_note", "title": "Capture Original Complaint Note", "tool_id": "crm.ticket.comment.create", "required": False, "when": "Use when the original complaint detail, transcript, or escalation note should be preserved as a ticket comment.", "description": "Attach the original complaint narrative or escalation note to the ticket.", "output": "Ticket comment id and preserved complaint context."},
            ],
            "tool_inventory": ["crm.customer.summary", "crm.ticket.search", "crm.ticket.create", "crm.ticket.get", "crm.ticket.assign", "crm.ticket.comment.create"],
            "required_final_facts": ["Resolved customer or party context.", "Complaint summary or issue type.", "Whether an existing open ticket already covers the complaint.", "Chosen queue, priority, and severity when creating a new ticket.", "Whether a new ticket should be created or an existing ticket should be reused/updated.", "Verified ticket id and ticket number from MCP output when a new ticket is created."],
            "required_draft_outputs": ["ticket id when a new complaint ticket is created", "ticket number when a new complaint ticket is created", "assigned owner when the ticket is routed immediately", "comment id when the original complaint note is attached"],
            "guardrails": ["Do not create a duplicate complaint ticket when an active open ticket already covers the same customer issue.", "Do not create a complaint ticket until the customer or party is resolved to the correct CRM account context.", "Do not guess queue, priority, or severity from vague wording; when urgency is unclear, say that the classification needs confirmation or use the safest supported default.", "Do not parallelize the initial customer resolution and duplicate-ticket check; execute them in order so the duplicate check uses the resolved customer context.", "Do not treat a CRM-... ticket number returned by create_ticket as the same thing as the internal crm_ticket:... record id.", "Do not restrict the first recovery search after create_ticket to status=open when the newly created ticket may still be in status new or another initial state.", "Do not finish a newly created complaint ticket workflow until crm.ticket.get or crm.ticket.search has confirmed both a stable ticket id and ticket number from MCP output.", "Do not present a narrative-only success answer for a new ticket when the workflow has not confirmed the ticket reference from tool output."],
            "success_checks": ["Final answer clearly states whether a new complaint ticket was created or an existing ticket should be reused.", "When a new ticket is created, the answer includes the confirmed ticket id, ticket number, selected queue, and severity context.", "The workflow is not complete for a newly created ticket unless the ticket reference was confirmed from MCP output."],
            "pitfalls": ["Do not treat every complaint as a high-severity escalation without evidence from the complaint context.", "Do not skip duplicate-ticket checks just because the customer sounds frustrated.", "Do not start customer summary and ticket search as one parallel batch; that can stall the session and weaken duplicate detection."],
            "examples": ["The customer called to complain about a damaged shipment. Create a CRM complaint ticket if nothing open already covers it.", "A support agent needs to log a refund complaint from a customer. Capture it as the right CRM ticket and assign it if needed."],
        }
    ])
print(json.dumps({"scope": "deployment", "value": value}))
PY
  )"
  curl -fsS -b "$cookie_jar" -H "X-CSRF-Token: ${csrf_token}" -H 'Content-Type: application/json' \
    -X PUT \
    -d "$payload" \
    "${base_url}/admin/api/config/entries/platform.mcp/value" >/dev/null
}

need_cmd curl
need_cmd go
need_cmd python3

echo "[1/8] Migrate and start Orbyte full"
(
  cd "$ORBYTE_ROOT"
  APP_JWT_SECRET=dev-secret DATABASE_URL="$ORBYTE_FULL_DATABASE_URL" rtk go run ./cmd/migrate up
)
start_bg "orbyte-full" "$ORBYTE_ROOT" env APP_ADDRESS="$ORBYTE_FULL_ADDR" APP_ENV=development APP_AUTH_DEV_MODE=true APP_JWT_SECRET=dev-secret APP_BOOTSTRAP_ADMIN_PASSWORD="$ORBYTE_ADMIN_PASSWORD" DATABASE_URL="$ORBYTE_FULL_DATABASE_URL" rtk go run ./cmd/server
wait_http "${ORBYTE_FULL_BASE_URL}/healthz" "Orbyte full"

echo "[2/8] Migrate and start Orbyte minimal"
(
  cd "$ORBYTE_ROOT"
  APP_JWT_SECRET=dev-secret DATABASE_URL="$ORBYTE_MINIMAL_DATABASE_URL" rtk go run ./cmd/migrate up
)
start_bg "orbyte-minimal" "$ORBYTE_ROOT" env APP_ADDRESS="$ORBYTE_MINIMAL_ADDR" APP_ENV=development APP_AUTH_DEV_MODE=true APP_JWT_SECRET=dev-secret APP_BOOTSTRAP_ADMIN_PASSWORD="$ORBYTE_ADMIN_PASSWORD" DATABASE_URL="$ORBYTE_MINIMAL_DATABASE_URL" rtk go run ./cmd/server
wait_http "${ORBYTE_MINIMAL_BASE_URL}/healthz" "Orbyte minimal"

echo "[3/8] Configure Orbyte MCP exposure and seed synthetic data"
FULL_COOKIE="$RUN_DIR/orbyte-full.cookie"
FULL_CSRF="$RUN_DIR/orbyte-full.csrf"
MIN_COOKIE="$RUN_DIR/orbyte-minimal.cookie"
MIN_CSRF="$RUN_DIR/orbyte-minimal.csrf"
orbyte_login "$ORBYTE_FULL_BASE_URL" "$FULL_COOKIE" "$FULL_CSRF"
orbyte_login "$ORBYTE_MINIMAL_BASE_URL" "$MIN_COOKIE" "$MIN_CSRF"
orbyte_set_mcp_mode "$ORBYTE_FULL_BASE_URL" "full" "$FULL_COOKIE" "$FULL_CSRF"
orbyte_set_mcp_mode "$ORBYTE_MINIMAL_BASE_URL" "minimal" "$MIN_COOKIE" "$MIN_CSRF"
write_opencode_config "$OPENCODE_HOME_ORBYTE_FULL" "$OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL" "orbyte-full" "$ORBYTE_FULL_MCP_URL"
write_opencode_config "$OPENCODE_HOME_ORBYTE_MINIMAL" "$OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL" "orbyte-minimal" "$ORBYTE_MINIMAL_MCP_URL"
(
  cd "$ORBYTE_ROOT"
  DATABASE_URL="$ORBYTE_FULL_DATABASE_URL" CRM_SEED=1 rtk go test -count=1 -run TestSeedCRMSyntheticScenario -v ./internal/platform/app
  cp /tmp/orbyte-crm-seed.json "$CRM_MANIFEST_FULL"
  DATABASE_URL="$ORBYTE_MINIMAL_DATABASE_URL" CRM_SEED=1 rtk go test -count=1 -run TestSeedCRMSyntheticScenario -v ./internal/platform/app
  cp /tmp/orbyte-crm-seed.json "$CRM_MANIFEST_MINIMAL"
  rtk go run ./cmd/agentproof seed --base-url "$ORBYTE_FULL_BASE_URL" --username admin --password "$ORBYTE_ADMIN_PASSWORD" --scenario retail_recovery_showcase --output "$SHOWCASE_MANIFEST_FULL"
)

echo "[4/8] Migrate and bootstrap Parmesan"
(
  cd "$ROOT"
  export DATABASE_URL="$PARMESAN_DATABASE_URL"
  export PARMESAN_CONFIG="$PARMESAN_CONFIG"
  export PARMESAN_AGENTS_DIR="$PARMESAN_AGENTS_DIR"
  export PARMESAN_HTTP_ADDR="$PARMESAN_HTTP_ADDR"
  export PARMESAN_METRICS_ADDR="$PARMESAN_METRICS_ADDR"
  export SECRETS_MASTER_KEY="$SECRETS_MASTER_KEY"
  export OPERATOR_API_KEY="$OPERATOR_API_KEY"
  export ORBYTE_FULL_MCP_URL="$ORBYTE_FULL_MCP_URL"
  export ORBYTE_MINIMAL_MCP_URL="$ORBYTE_MINIMAL_MCP_URL"
  export OPENCODE_COMMAND="$OPENCODE_COMMAND"
  export OPENCODE_MODEL="$OPENCODE_MODEL"
  export DEFAULT_REASONING_PROVIDER="$DEFAULT_REASONING_PROVIDER"
  export DEFAULT_STRUCTURED_PROVIDER="$DEFAULT_STRUCTURED_PROVIDER"
  export DEFAULT_EMBEDDING_PROVIDER="$DEFAULT_EMBEDDING_PROVIDER"
  export OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}"
  export OPENROUTER_BASE_URL="${OPENROUTER_BASE_URL:-https://openrouter.ai/api/v1}"
  export OPENAI_API_KEY="${OPENAI_API_KEY:-}"
  export OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
  export OPENCODE_HOME_ORBYTE_FULL="$OPENCODE_HOME_ORBYTE_FULL"
  export OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL"
  export OPENCODE_HOME_ORBYTE_MINIMAL="$OPENCODE_HOME_ORBYTE_MINIMAL"
  export OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL"
  rtk go run ./cmd/migrate
  rtk go run ./cmd/bootstrap
)

echo "[5/8] Start Parmesan API and worker"
start_bg "parmesan-api" "$ROOT" env \
  DATABASE_URL="$PARMESAN_DATABASE_URL" \
  PARMESAN_CONFIG="$PARMESAN_CONFIG" \
  PARMESAN_AGENTS_DIR="$PARMESAN_AGENTS_DIR" \
  PARMESAN_HTTP_ADDR="$PARMESAN_HTTP_ADDR" \
  PARMESAN_METRICS_ADDR="$PARMESAN_METRICS_ADDR" \
  SECRETS_MASTER_KEY="$SECRETS_MASTER_KEY" \
  OPERATOR_API_KEY="$OPERATOR_API_KEY" \
  ORBYTE_FULL_MCP_URL="$ORBYTE_FULL_MCP_URL" \
  ORBYTE_MINIMAL_MCP_URL="$ORBYTE_MINIMAL_MCP_URL" \
  OPENCODE_COMMAND="$OPENCODE_COMMAND" \
  OPENCODE_MODEL="$OPENCODE_MODEL" \
  DEFAULT_REASONING_PROVIDER="$DEFAULT_REASONING_PROVIDER" \
  DEFAULT_STRUCTURED_PROVIDER="$DEFAULT_STRUCTURED_PROVIDER" \
  DEFAULT_EMBEDDING_PROVIDER="$DEFAULT_EMBEDDING_PROVIDER" \
  OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}" \
  OPENROUTER_BASE_URL="${OPENROUTER_BASE_URL:-https://openrouter.ai/api/v1}" \
  OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
  OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}" \
  OPENCODE_HOME_ORBYTE_FULL="$OPENCODE_HOME_ORBYTE_FULL" \
  OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL" \
  OPENCODE_HOME_ORBYTE_MINIMAL="$OPENCODE_HOME_ORBYTE_MINIMAL" \
  OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL" \
  rtk go run ./cmd/api
start_bg "parmesan-worker" "$ROOT" env \
  DATABASE_URL="$PARMESAN_DATABASE_URL" \
  PARMESAN_CONFIG="$PARMESAN_CONFIG" \
  PARMESAN_AGENTS_DIR="$PARMESAN_AGENTS_DIR" \
  HTTP_ADDR="$PARMESAN_WORKER_HTTP_ADDR" \
  METRICS_ADDR="$PARMESAN_WORKER_METRICS_ADDR" \
  SECRETS_MASTER_KEY="$SECRETS_MASTER_KEY" \
  OPERATOR_API_KEY="$OPERATOR_API_KEY" \
  ORBYTE_FULL_MCP_URL="$ORBYTE_FULL_MCP_URL" \
  ORBYTE_MINIMAL_MCP_URL="$ORBYTE_MINIMAL_MCP_URL" \
  OPENCODE_COMMAND="$OPENCODE_COMMAND" \
  OPENCODE_MODEL="$OPENCODE_MODEL" \
  DEFAULT_REASONING_PROVIDER="$DEFAULT_REASONING_PROVIDER" \
  DEFAULT_STRUCTURED_PROVIDER="$DEFAULT_STRUCTURED_PROVIDER" \
  DEFAULT_EMBEDDING_PROVIDER="$DEFAULT_EMBEDDING_PROVIDER" \
  OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}" \
  OPENROUTER_BASE_URL="${OPENROUTER_BASE_URL:-https://openrouter.ai/api/v1}" \
  OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
  OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}" \
  OPENCODE_HOME_ORBYTE_FULL="$OPENCODE_HOME_ORBYTE_FULL" \
  OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_FULL" \
  OPENCODE_HOME_ORBYTE_MINIMAL="$OPENCODE_HOME_ORBYTE_MINIMAL" \
  OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL="$OPENCODE_XDG_CONFIG_HOME_ORBYTE_MINIMAL" \
  rtk go run ./cmd/worker
wait_http "${PARMESAN_BASE_URL}/healthz" "Parmesan"

echo "[6/8] Start Nexus gateway"
(
  cd "$NEXUS_ROOT"
  DATABASE_URL="${NEXUS_DATABASE_URL:-postgres://postgres:postgres@127.0.0.1:5432/nexus?sslmode=disable}" \
    rtk go run ./cmd/migrator
)
start_bg "nexus" "$NEXUS_ROOT" env \
  DATABASE_URL="${NEXUS_DATABASE_URL:-postgres://postgres:postgres@127.0.0.1:5432/nexus?sslmode=disable}" \
  NEXUS_ENV=development \
  WEBCHAT_DEV_AUTH=true \
  ACP_IMPLEMENTATION=parmesan \
  ACP_BASE_URL="$PARMESAN_BASE_URL" \
  ACP_TOKEN="$OPERATOR_API_KEY" \
  ACP_RPC_TIMEOUT_SECONDS=300 \
  DEFAULT_AGENT_PROFILE_ID="$VALIDATION_AGENT_ID" \
  DEFAULT_ACP_AGENT_NAME="$VALIDATION_AGENT_ID" \
  HTTP_ADDR="$NEXUS_ADDR" \
  ADMIN_ADDR="$NEXUS_ADMIN_ADDR" \
  rtk go run ./cmd/gateway
wait_http "${NEXUS_BASE_URL}/healthz" "Nexus"

echo "[7/8] Run integrated validation"
(
  cd "$ROOT"
  rtk go run ./cmd/integration-orbyte-nexus-validate \
    --nexus-base-url "$NEXUS_BASE_URL" \
    --parmesan-base-url "$PARMESAN_BASE_URL" \
    --parmesan-operator-key "$OPERATOR_API_KEY" \
    --orbyte-full-mcp-url "$ORBYTE_FULL_MCP_URL" \
    --orbyte-minimal-mcp-url "$ORBYTE_MINIMAL_MCP_URL" \
    --complaint-mcp-url "$COMPLAINT_MCP_URL" \
    --agent-id "$VALIDATION_AGENT_ID" \
    --crm-manifest "$CRM_MANIFEST_FULL" \
    --crm-manifest-minimal "$CRM_MANIFEST_MINIMAL" \
    --showcase-manifest "$SHOWCASE_MANIFEST_FULL" \
    --script "$VALIDATION_SCRIPT" \
    --report-out "$REPORT_OUT"
)

echo "[8/8] Validation complete"
echo "Report: $REPORT_OUT"
