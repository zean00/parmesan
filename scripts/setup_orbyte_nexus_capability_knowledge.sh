#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST_PATH="${MANIFEST_PATH:-$ROOT_DIR/integrations/orbyte_nexus_capabilities/knowledge/corpus-manifest.json}"
PARMESAN_BASE_URL="${PARMESAN_BASE_URL:-http://127.0.0.1:18090}"
OPERATOR_API_KEY="${OPERATOR_API_KEY:?OPERATOR_API_KEY is required}"

python3 - "$MANIFEST_PATH" "$PARMESAN_BASE_URL" "$OPERATOR_API_KEY" <<'PY'
import json
import pathlib
import sys
import urllib.error
import urllib.request

manifest_path = pathlib.Path(sys.argv[1]).resolve()
base_url = sys.argv[2].rstrip("/")
operator_key = sys.argv[3]

manifest = json.loads(manifest_path.read_text())
source_id = manifest["source_id"]
scope_kind = manifest["scope_kind"]
scope_id = manifest["scope_id"]
repo_root = manifest_path.parents[3]
capture_output_dir = (repo_root / manifest["capture_output_dir"]).resolve()

headers = {
    "Authorization": f"Bearer {operator_key}",
    "Content-Type": "application/json",
}

payload = {
    "id": source_id,
    "scope_kind": scope_kind,
    "scope_id": scope_id,
    "kind": "folder",
    "uri": str(capture_output_dir),
}

req = urllib.request.Request(
    f"{base_url}/v1/operator/knowledge/sources",
    data=json.dumps(payload).encode(),
    headers=headers,
    method="POST",
)
try:
    with urllib.request.urlopen(req, timeout=30) as resp:
        print(f"created knowledge source: {resp.status}")
except urllib.error.HTTPError as err:
    body = err.read().decode("utf-8", errors="replace")
    if err.code not in (200, 201, 409):
        raise SystemExit(f"create knowledge source failed: {err.code} {body}")
    print(f"knowledge source already present or accepted: {err.code}")

compile_req = urllib.request.Request(
    f"{base_url}/v1/operator/knowledge/sources/{source_id}/compile",
    data=b"",
    headers={"Authorization": f"Bearer {operator_key}"},
    method="POST",
)
with urllib.request.urlopen(compile_req, timeout=30) as resp:
    print(f"compile requested: {resp.status}")
PY
