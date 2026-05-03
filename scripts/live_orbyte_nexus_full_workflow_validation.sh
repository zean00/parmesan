#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${RUN_DIR:-/tmp/parmesan-orbyte-nexus-validation}"

export PARMESAN_CONFIG="${PARMESAN_CONFIG:-$ROOT/integrations/orbyte_nexus_full_workflow/config/parmesan.orbyte_nexus_full_workflow.yaml}"
export PARMESAN_AGENTS_DIR="${PARMESAN_AGENTS_DIR:-$ROOT/integrations/orbyte_nexus_full_workflow/agents}"
export VALIDATION_AGENT_ID="${VALIDATION_AGENT_ID:-agent_orbyte_nexus_full_workflow_validation}"
export VALIDATION_SCRIPT="${VALIDATION_SCRIPT:-$ROOT/integrations/orbyte_nexus_full_workflow/conversations/integrated_validation.json.tmpl}"
export COMPLAINT_MCP_URL="${COMPLAINT_MCP_URL:-${ORBYTE_FULL_MCP_URL:-http://${ORBYTE_FULL_ADDR:-127.0.0.1:18110}/mcp}}"
export RUN_DIR
export CRM_MANIFEST_FULL="${CRM_MANIFEST_FULL:-$RUN_DIR/orbyte-full-crm-seed.json}"
export COMPLAINT_CRM_MANIFEST="${COMPLAINT_CRM_MANIFEST:-$CRM_MANIFEST_FULL}"

exec "$ROOT/scripts/live_orbyte_nexus_validation.sh"
