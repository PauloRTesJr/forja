# Catalog

The official list of models Forja knows. It may move to its own repository (`forja-catalog`) later; the layout is meant to move with it.

```text
index.json                  one entry per official manifest: id, stage types, license, sha256
schema/manifest.schema.json what a model manifest may contain
schema/family.schema.json   what a family.yaml may contain
```

The manifests themselves live in the engine for now, at `engine/src/forja_engine/manifests/`, so every engine ships with them. Their families live in `engine/families/`. A model from anywhere else arrives as a model package (see `templates/adapter/`).

## Rules a manifest must meet

The engine's tests (`engine/tests/test_catalog.py`) enforce these on every bundled manifest and family:

- License metadata: an SPDX id (or `LicenseRef-<name>`), whether commercial use is allowed, and, for every model that is not a stub, a URL to the terms at a pinned revision. Restrictions (territories, user counts) are listed in plain words.
- Pinned downloads: weights at a 40-character commit `revision`, with a `sha256` for each large file; upstream code at one commit, with the archive's `sha256`.
- Only known fields and values: stage types, views, backends and parameter types from the schema.

Non-commercial models may be listed, and are always flagged by `license.commercial: false`. Weights are never bundled: install downloads them from the publisher.

## The index

`index.json` is generated from the manifests. After adding or changing one, rebuild it:

```sh
cd engine
uv run python -m forja_engine.catalog          # rewrite catalog/index.json
uv run python -m forja_engine.catalog --check  # exit 1 when it is stale
```

`make check` fails while the index is stale.

Signing the index (minisign) and fetching it once a day are not built yet.

## Adding a model to the catalog

1. Build and test it as a model package from `templates/adapter/`.
2. Move its family folder to `engine/families/<family>/` and its manifest to `engine/src/forja_engine/manifests/`.
3. Add the adapter's path to the `mypy` list in the Makefile, rebuild the index, and run `make check`.
4. Open a pull request. Catalog adapters are reviewed before merge: they run on users' machines as ordinary code.

The steps in full are in `docs/contributing/adding-a-model.md`.
