# Adding a model

A model needs a manifest and an adapter, and no change to the core or the engine. The same pieces serve two routes:

- **A model package**, a folder anywhere, listed in `engine.manifest_dirs`. Use it to try a model, keep a private one, or publish one yourself.
- **A catalog model**, moved into this repository so every Forja user gets it. It goes through review.

Start with a model package either way: [`templates/adapter/`](../../templates/adapter/) is one that already installs, runs and passes the conformance suite.

## The pieces

```text
mymodel/
  family.yaml                Python version, lanes, pinned upstream code, compiled extras
  requirements/<lane>.txt    dependencies per torch lane: cuda, rocm, mps, cpu
  manifests/<model-id>.yaml  one per model the family runs
  src/forja_mymodel/         the adapter package
  tests/                     fake-upstream tests and the conformance suite
  pyproject.toml             builds the adapter package into the environment
```

A **family** is one Python environment shared by related models (Hunyuan3D-2mv and Hunyuan3D-2 Paint share `hunyuan3d2`). Each family gets its own worker process and its own uv environment, so models never fight over dependencies.

## 1. Copy the template

Copy `templates/adapter/` out of the checkout and rename the family (`example`), the package (`forja_example`) and the model id (`example-extrude`). A family name must be new: not one in `engine/families/`, nor `stub` or `builtin`. Its [README](../../templates/adapter/README.md) lists every place each name appears.

## 2. Write the adapter

An adapter subclasses `forja_sdk.Adapter` (the [SDK reference](adapter-sdk.md) has every type):

```python
class MyShape(Adapter):
    version = "1"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,), vram_bytes=8 << 30)

    def load(self, device: Device, dtype: str = "fp16") -> None: ...
    def unload(self) -> None: ...
    def run(self, ctx: StageContext) -> list[StageOutput]: ...
```

- **`load`** runs once per worker; the model stays warm between jobs. Weights are in `self.weights_dir`, where install put them. When `self.offload` is set, the model does not fit the GPU in `fit-first` mode: take the low-memory path (CPU offload, tiling) before any weights reach the device. Import heavy modules (torch, the upstream package) here, not at the top of the module, so the module imports anywhere.
- **`run`** gets a `StageContext`: `ctx.input(ArtifactKind.IMAGE_PNG)` for an input, `ctx.views()` for side views, `ctx.params`, `ctx.seed`, `ctx.out_dir`. Report `ctx.progress(step, total)` and call `ctx.check_cancel()` between steps (diffusion steps, for instance). Write outputs only into `ctx.out_dir` and return them; the first is the one the next stage reads.
- **`unload`** frees device memory (`torch.cuda.empty_cache()` after dropping the pipeline).
- **`estimate_vram(params)`** returns peak bytes for those params; the default is `capabilities().vram_bytes`.
- **`version`** changes whenever outputs change for the same inputs; it is part of the [cache key](stage-cache.md).

An adapter never touches the network, the job graph or the cache, and gives byte-identical outputs for the same inputs, params and seed: seed every generator from `ctx.seed`.

What each model stage type reads and must return:

| Stage type | Inputs | First output |
|---|---|---|
| `background-removal` | the reference image (PNG) | a PNG with an alpha channel |
| `shape` | the framed cut-out (PNG, RGBA), plus side views for models that list `views` | a GLB mesh; mark it `preview=True` so the app shows it early |
| `texture` | the low-poly mesh (GLB) and the framed cut-out | a GLB with UVs and a base color texture (painters unwrap UVs themselves) |

## 3. Describe the family

```yaml
name: mymodel
python: "3.11"
lanes: [cuda]
torch_backend: { cuda: cu124 }        # torch from PyTorch's index for that CUDA build
upstream:
  url: https://codeload.github.com/<org>/<repo>/tar.gz/<40-character commit sha>
  sha256: <sha256 of that archive>
  license: MIT
  pythonpath: ["."]                   # folders of the extracted source to import from
build: ["<package>==<version>"]       # compiled against the installed torch, in order; {upstream} is the source
smoke: "import <upstream module>"     # proves the upstream code and its extensions load
```

`requirements/<lane>.txt` lists the family's dependencies for each lane in `lanes`. Install adds `forja-engine` (and with it `forja-engine-sdk`) and the adapter package. `catalog/schema/family.schema.json` is the full schema.

## 4. Write the manifest

```yaml
id: mymodel
name: My Model
family: mymodel
adapter: forja_mymodel:MyShape
adapter_version: "1"                  # equal to MyShape.version
tasks: [shape]
description: One line for the Models screen.
disk_gb: 9.5                          # environment plus weights, measured
license:
  spdx: MIT                           # or LicenseRef-<name> for a custom license
  commercial: true
  restrictions: []                    # plain words: territories, user limits
  url: https://github.com/<org>/<repo>/blob/<commit>/LICENSE
vram_gb: 8                            # peak at default params; the engine measures the real one
requirements:
  cuda: { status: supported, min_vram_gb: 8, dtypes: [fp16, bf16] }
  mps: { status: unsupported }
  cpu: { status: unsupported }
weights:
  hub: huggingface
  repo: <org>/<repo>
  revision: <40-character commit sha>
  files:
    - { path: model.safetensors, sha256: <sha256>, bytes: <size> }
params:
  steps: { type: integer, default: 30, minimum: 8, maximum: 100 }
rank: 10                              # higher wins when a preset leaves the model open
views: [front, left, back, right]     # only for models that read side views
```

`catalog/schema/manifest.schema.json` is the full schema. A license with restrictions, a custom one, or one without commercial use makes users accept it before installing.

## The pinning rules

Everything an install downloads is pinned, so the same manifest always installs the same bytes:

- Upstream code: an archive of one commit, with its sha256. Install refuses a mismatch.
- Weights: a 40-character commit `revision`, never a branch or tag, and a `sha256` for each large file.
- Upstream code under a restrictive license is fetched at install time and never committed to Forja (decision 9).
- No `trust_remote_code`. Pickle checkpoints load only through `torch.load(weights_only=True)`; prefer safetensors.
- A new upstream commit, a new weights revision or a changed adapter each change the cache key; bump `version` and `adapter_version` for the last.

## 5. Test it

Real models need a GPU and gigabytes of weights; tests need neither. Swap the upstream pipeline for a fake and test the glue you own: preprocessing, params, progress, cancellation, output files, orientation. The template's `tests/` and `engine/tests/test_families.py` show the pattern: fake modules in `sys.modules` for torch and the upstream package, a fake pipeline on the adapter.

Then run the conformance suite, which also checks the manifest against the adapter:

```sh
cd engine
uv run pytest ~/forja-models/mymodel/tests
uv run forja-sdk test ~/forja-models/mymodel/manifests/mymodel.yaml
```

On a GPU machine with the model installed, run it for real; it also compares `vram_gb` with the peak PyTorch measures:

```sh
<data>/envs/mymodel/Scripts/python -m forja_sdk test manifests/mymodel.yaml --device cuda --weights <data>/models/mymodel
```

(`bin/python` instead of `Scripts/python` on macOS and Linux.) See [the SDK reference](adapter-sdk.md#conformance) for every check.

## 6. Run it

```toml
# config.toml
[engine]
manifest_dirs = ["/home/you/forja-models/mymodel"]
```

Restart the core (`forja daemon stop`, or quit the app), then:

```sh
forja models checks mymodel
forja models install mymodel
forja models pin shape mymodel
forja generate photo.png
```

`engine/tests/test_gate.py` does exactly this with the template, through the real core, as the proof that adding a model needs no core change.

## 7. Bring it into the catalog

1. Move the family folder to `engine/families/<family>/` and the manifest to `engine/src/forja_engine/manifests/`.
2. Add `families/<family>/src` to the `mypy` list in the Makefile's `check` target.
3. Move the adapter's tests into `engine/tests/test_families.py`, next to the other families'.
4. Rebuild the catalog index: `cd engine && uv run python -m forja_engine.catalog`.
5. `make check`, then open a pull request with the license fields filled in. Reviewers read the adapter: it runs on users' machines as ordinary code.

The catalog rules are in [catalog/README.md](../../catalog/README.md).
