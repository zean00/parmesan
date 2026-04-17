#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST_PATH="${MANIFEST_PATH:-$ROOT_DIR/integrations/orbyte_nexus_capabilities/knowledge/corpus-manifest.json}"

python3 - "$MANIFEST_PATH" <<'PY'
import json
import pathlib
import re
import sys
import urllib.request
from html.parser import HTMLParser


class TextExtractor(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []

    def handle_data(self, data):
        if data and not data.isspace():
            self.parts.append(data)

    def text(self):
        text = "\n".join(self.parts)
        text = re.sub(r"\n{3,}", "\n\n", text)
        return text.strip()


manifest_path = pathlib.Path(sys.argv[1]).resolve()
manifest = json.loads(manifest_path.read_text())
repo_root = manifest_path.parents[3]
output_dir = (repo_root / manifest["capture_output_dir"]).resolve()
output_dir.mkdir(parents=True, exist_ok=True)

for page in manifest["pages"]:
    req = urllib.request.Request(
        page["url"],
        headers={"User-Agent": "ParmesanCapabilityCapture/1.0"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read().decode("utf-8", errors="replace")
    parser = TextExtractor()
    parser.feed(raw)
    body = parser.text()
    out = output_dir / page["filename"]
    out.write_text(
        f"# {page['title']}\n\n"
        f"Source URI: {page['url']}\n\n"
        f"Source ID: {page['id']}\n\n"
        f"Capture Note: {page.get('notes', '').strip()}\n\n"
        f"{body}\n",
        encoding="utf-8",
    )
    print(f"captured {page['url']} -> {out}")
PY
