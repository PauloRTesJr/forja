# The adapter SDK

`forja-engine-sdk` (`engine/sdk/`, imported as `forja_sdk`) is the contract between Forja and a model adapter. It needs only the Python standard library, so an adapter can depend on it alone. The engine re-exports the same objects as `forja_engine.sdk`: an adapter may import either, and `isinstance` checks and `except Cancelled` hold across both.

Inside a model family's environment it is already installed, with the engine. Elsewhere, install it from a checkout: `pip install ./engine/sdk` (PyPI releases are documented after integration).

## Types

All in `forja_sdk` (source: `engine/sdk/src/forja_sdk/types.py`).

| Name | What it is |
|---|---|
| `Adapter` | The base class. Implement `capabilities()` and `run(ctx)`; override `load(device, dtype)`, `unload()` and `estimate_vram(params)` as needed. Class attribute `version`; instance attributes `weights_dir` and `offload`, set by the worker before `load`. |
| `Capabilities` | `tasks` (stage types served), `devices` (where it runs, in order of preference), `vram_bytes` (peak at default params). |
| `StageType` | The stage types: `background-removal`, `framing`, `shape`, `mesh.cleanup`, `mesh.decimate`, `mesh.uv`, `texture`, `texture.match`, `bake`, `mesh.lods`, `export`, `views.detect`, `views.split`. |
| `ArtifactKind` | `image/png`, `mesh/glb`, `application/json`. |
| `Device` | `cuda`, `mps`, `rocm`, `cpu`. |
| `StageInput` | `path`, `kind`, `hash` (a content ref), `view` (one of `VIEWS`, or None for the front). |
| `StageOutput` | `path`, `kind`, `preview` (show it before the job ends). |
| `StageContext` | `stage_id`, `task`, `inputs`, `params`, `seed`, `out_dir`; `input(kind, index)`, `views()`, `progress(step, total)`, `check_cancel()`. |
| `Cancelled` | Raised by `check_cancel()` once the stage is cancelled. Let it propagate. |
| `VIEWS` | `("front", "left", "back", "right")`; "left" shows the subject's left side. |

## Artifacts

`forja_sdk.artifacts` reads and writes the artifact kinds with the standard library only:

- `validate(path, kind)` checks a file is a valid artifact of its kind and returns what it learned; it raises `InvalidArtifact` otherwise. PNGs are checked chunk by chunk, CRCs and pixel data length included. GLBs are checked for their header, chunks, buffers, accessors and index ranges, and NaN positions; meshopt-compressed GLBs are accepted.
- `read_png_info(bytes)` gives width, height, channels and alpha; `read_glb_info(bytes)` gives triangles, vertices, UVs, normals, materials and whether a base color texture is used.
- `write_png(path, width, height, pixels, alpha=True)` and `write_glb(path, positions, faces, uvs=None, texture_png=None)` write small files, for tests and simple adapters.
- `kind_for(path)` maps `.png`, `.glb` and `.json` to their kinds.

`forja_sdk.samples` makes deterministic inputs shaped like the pipeline's: a subject photo, its cut-out, a box mesh with UVs, a textured box (`inputs_for(stage_type, folder)`).

## Conformance

The conformance suite runs an adapter the way the engine does and holds it to the contract.

```sh
forja-sdk test my_package:MyAdapter                  # sample inputs, on the CPU when it runs there
forja-sdk test manifests/my-model.yaml               # also checks the manifest against the adapter
forja-sdk test manifests/my-model.yaml --device cuda --weights <data>/models/my-model
forja-sdk test my_package:MyAdapter --task texture --input mesh.glb --input cutout.png
```

| Check | Passes when |
|---|---|
| `capabilities` | Tasks and devices are declared with SDK types, `vram_bytes` is a byte count, `version` is set |
| `manifest` | Given a manifest: its tasks equal the adapter's, `adapter_version` equals `version`, every supported backend is in the adapter's devices, the license has an SPDX id, the commercial flag and (unless a stub) a URL, and parameter defaults have the right type and range |
| `load` | `load(device, dtype)` returns, on a device the adapter lists |
| `run[<task>]` | Outputs exist, lie inside `ctx.out_dir`, are valid files of their kind, and the first is what the next stage reads: a cut-out with alpha, a mesh with triangles, a textured mesh with UVs |
| `inputs[<task>]` | Reported only when the adapter changed an input file |
| `progress[<task>]` | Every report has `0 <= step <= total` and `total > 0`, and steps never go back. Skipped when nothing is reported (fine for a single step) |
| `determinism[<task>]` | A second run with the same inputs, params and seed writes byte-identical outputs |
| `cancellation[<task>]` | With cancel requested right after the first progress report that leaves work to do, `run` raises `Cancelled`. Skipped for single-step stages |
| `vram` | `estimate_vram` returns bytes. On CUDA or ROCm with PyTorch, the peak measured over load and the first run stays within the manifest's `vram_gb` (or the estimate) plus 25% (`--vram-tolerance`). Skipped where there is nothing to measure |
| `reload` | After `unload()` and `load()`, a run writes the same bytes as the first |
| `unload` | `unload()` returns |

Each stage type the adapter serves gets its own `run`, `progress`, `determinism` and `cancellation` checks, on sample inputs unless `--input` gives files (with `--task`). Stage types without a sample, such as `views.split`, need `--input`. Other flags: `--param key=value` (JSON values), `--seed`, `--dtype`, `--offload`, `--path <folder>` to import from, `--skip <check>`, `--keep <folder>` to keep the outputs, and `--json`. The command exits 1 when any check fails.

From pytest:

```python
from forja_sdk.conformance import assert_conforms, load_manifest

def test_conforms(tmp_path):
    assert_conforms(MyAdapter, manifest=load_manifest(MANIFEST), work_dir=tmp_path)
```

`run_suite(...)` takes the same arguments and returns the `Report` without raising. The engine's own tests run the suite on the stub models and every built-in stage (`engine/tests/test_sdk.py`).

## Changing the SDK

The SDK is a public contract. Adding a stage type or an artifact kind is additive; changing or removing one breaks adapters, and needs a new major version of `forja-engine-sdk`. See [adding a stage type](adding-a-stage-type.md).
