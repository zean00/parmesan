# Parmesan Documentation

This directory is the source for both:

- repository-readable Markdown docs
- MkDocs site generation

For the full landing page and navigation, use [index.md](./index.md).

## Build With MkDocs

From the repository root:

```bash
python3 -m pip install -r docs/requirements-mkdocs.txt
python3 -m mkdocs build --clean
```

The MkDocs config lives in `mkdocs.yml` and is set to `use_directory_urls:
false` so generated pages build as explicit `.html` files instead of directory
indexes.

Mermaid is vendored locally under `docs/javascripts/mermaid.min.js`, so the
built site renders diagrams without depending on a CDN.

Generated site output goes to `site/` and should not be committed.
