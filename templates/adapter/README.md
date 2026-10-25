# Adapter template

A model package: everything Forja needs to run one more model, outside the engine. It holds a family (one Python environment), a manifest per model, the adapter and its tests. As shipped it runs `example-extrude`, a CPU model that extrudes the subject's silhouette into a slab, so you can try the whole path before writing any model code.

```text
family.yaml                    environment: Python version, lanes, pinned upstream code
requirements/<lane>.txt        dependencies per torch lane (cpu, cuda, mps, rocm)
manifests/example-extrude.yaml the model: adapter, stage types, license, weights, params
src/forja_example/__init__.py  the adapter (forja_sdk.Adapter)
tests/                         fake-upstream tests and the conformance suite
pyproject.toml                 builds the adapter package into the environment
```

## Start a new adapter

1. Copy this folder outside the Forja checkout, for example to `~/forja-models/mymodel`.
2. Rename, everywhere they appear:
   - the family `example` (in `family.yaml` and the manifest's `family`),
   - the package `forja_example` (the `src/` folder, `pyproject.toml`, the manifest's `adapter`),
   - the model id `example-extrude` (the manifest's file name and `id`).
   The family name must not be one of the engine's (`engine/families/`, `stub`, `builtin`).
3. In the manifest, set `tasks`, `license` (SPDX id, commercial use, a URL to the terms), `vram_gb`, `requirements` and `params`. Pin the weights: a Hugging Face repo, a 40-character commit `revision`, and a `sha256` for each large file.
4. In `family.yaml`, pin the upstream code: an archive URL of one commit and its `sha256`. Install downloads and verifies it; it is never committed. List dependencies per lane in `requirements/`.
5. In the adapter, replace `Extruder` with the upstream pipeline. Keep the glue: read inputs with `ctx.input(kind)`, report `ctx.progress(step, total)`, call `ctx.check_cancel()` between steps, seed from `ctx.seed`, write outputs into `ctx.out_dir`. Bump `version` (and the manifest's `adapter_version`) whenever outputs change for the same inputs.

## Test it

From a Forja checkout, with the engine's environment:

```sh
cd engine
uv run pytest ~/forja-models/mymodel/tests
uv run forja-sdk test ~/forja-models/mymodel/manifests/mymodel.yaml
```

The tests swap the upstream pipeline for a fake, so they run without a GPU or weights. On a machine with the model installed, run the suite on the real thing: `forja-sdk test <manifest> --device cuda --weights <data>/models/<id>`.

## Use it

Add the package to the engine's manifest folders in `config.toml` (`forja config edit` opens it), with its full path:

```toml
[engine]
manifest_dirs = ["/home/you/forja-models/mymodel"]
```

The engine reads it when it starts, so restart the core (`forja daemon stop`, or quit the app). Then install the model and pick it for its stage:

```sh
forja models install mymodel
forja models pin shape mymodel
forja generate photo.png
```

Install builds `<data>/envs/<family>` with uv from this folder, fetches the pinned weights, and imports the adapter as a smoke test. Neither the engine nor the core needs a change. The adapter is arbitrary code that runs in its own process on your machine, not in a sandbox: install only packages you trust.

The full guide is `docs/contributing/adding-a-model.md` in the Forja repository.
