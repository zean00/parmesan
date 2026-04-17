This directory is intentionally checked in as an empty target for captured web
pages used by the capability-validation knowledge corpus.

Populate it with:

```bash
rtk ./scripts/capture_orbyte_nexus_capability_corpus.sh
```

The capture script reads `corpus-manifest.json`, fetches the listed URLs, strips
them into markdown-like plain text, and writes deterministic files into this
folder.
