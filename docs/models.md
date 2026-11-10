# Models and licenses

Forja ships no model weights. Each model is described by a manifest (where its weights live, what it needs, what its license allows), and installing one downloads it from its publisher.

## The models

| Model | id | Stage | License | VRAM | Disk | Runs on |
|---|---|---|---|---|---|---|
| BiRefNet (general) | `birefnet` | Background removal | MIT | 8 GB¹ | 3.8 GB | NVIDIA, CPU (a Mac's included) |
| BiRefNet lite | `birefnet-lite` | Background removal | MIT | 0.8 GB | 3.1 GB | NVIDIA, CPU (a Mac's included) |
| Pixal3D | `pixal3d` | Shape | MIT⁴ | 18 GB (needs 10) | 25.8 GB | NVIDIA, Linux⁵ |
| TRELLIS.2 | `trellis2` | Shape | MIT⁴ | 24 GB (needs 24) | 16.1 GB | NVIDIA⁵ |
| TripoSG | `triposg` | Shape | MIT | 8 GB (needs 8) | 12.5 GB | NVIDIA |
| Pixal3D multi-view | `pixal3d-mv` | Shape, from the front plus side and back views | MIT⁴ | 18 GB (needs 10) | 18.8 GB | NVIDIA, Linux⁵ |
| Hunyuan3D-2mv (turbo) | `hunyuan3d2-mv` | Shape, from the front plus side and back views | Tencent Hunyuan 3D 2.0 Community² | 6 GB (needs 6) | 5.0 GB | NVIDIA, Apple Silicon (experimental: [macOS](macos.md)) |
| TRELLIS.2 Paint | `trellis2-texture` | Texture (base color, roughness, metallic) | MIT⁴ | 24 GB (needs 24) | 8.0 GB | NVIDIA⁵ |
| Hunyuan3D-2 Paint (turbo) | `hunyuan3d2-paint` | Texture (base color) | Tencent Hunyuan 3D 2.0 Community² | 12.5 GB (needs 10) | 23.1 GB | NVIDIA |
| SkinTokens (TokenRig) | `skintokens` | Rig ([rigging](rigging.md)) | MIT | 14 GB (needs 14) | 8.1 GB | NVIDIA |
| UniRig | `unirig` | Rig ([rigging](rigging.md)) | MIT³ | 8.7 GB (needs 8) | 10.7 GB | NVIDIA |

Within a stage, the list runs from the highest-ranked model down: a job gets the best one ready, so installing Pixal3D or TRELLIS.2 upgrades every preset where they fit. VRAM is the peak at default settings; "needs" is the least the model runs in at all. The engine replaces these figures with the peaks it measures on your GPU. The [hardware guide](hardware.md) says what runs on 8, 12, 16 and 24 GB.

1. BiRefNet's figure is what ONNX Runtime's CUDA arena grows to on a 12 GB card, because cuDNN sizes its workspace to the free memory. It runs in much less.
2. Commercial use is allowed, with limits: the license does not cover the EU, the UK or South Korea, and above 1 million monthly active users a separate license is needed. Forja shows these terms; it does not block by location.
3. UniRig's upstream tree, fetched at install time, also carries the Michelangelo shape encoder under GPL-3.0.
4. Their image encoder is Meta's DINOv3, under the DINOv3 License: commercial use is allowed, but not military, weapons or other uses that trade controls forbid. Its weights are gated on Hugging Face: accept the license on [its page](https://huggingface.co/facebook/dinov3-vitl16-pretrain-lvd1689m) and set `HF_TOKEN` before installing.
5. Upstream tests these on Linux. Their CUDA extensions are built at install; Pixal3D's NATTEN does not build on Windows, so Forja does not offer to install Pixal3D there: a Windows computer sends it to a Linux one ([sharing](sharing.md), [cloud](cloud.md)). Disk figures are estimates until measured.

The stub models (`stub-background`, `stub-shape`, `stub-paint`, `stub-rig`) are deterministic CPU stand-ins for tests and machines without a GPU. They are used only when `models.stubs = true`.

## Install, repair and remove

In the app, the Models screen lists every model with its status, where it runs, its cost and its license, and installs, repairs or removes it. From a terminal:

```sh
forja models ls                      # this computer's models; --all adds other providers'
forja models checks triposg          # what this computer still needs for it
forja models checks --stage mesh.remesh  # what it needs to run a stage type itself (Blender)
forja models fix <check>             # run a check's fix, such as installing the CUDA toolkit
forja models install triposg         # build its environment and fetch its weights
forja installs ls                    # installs, removals and fixes, running and recent
forja installs watch <task>          # follow one; forja installs cancel <task> stops it
forja models rm triposg              # remove its weights, and its environment once unused
```

Installing again repairs a model. `--provider <id>` installs on another computer you added ([sharing](sharing.md)).

An install runs these steps, each reported live:

1. **Check this computer**: the driver, and for models that compile GPU code, the CUDA toolkit and C++ compiler. Fixes install software, so the engine runs them only for a caller on its own computer.
2. **Python environment**: one per model family, built with uv, so models never share or break each other's dependencies.
3. **Model code**: upstream code, downloaded from a pinned commit and checked against its sha256. Code with a restrictive license is fetched here, never shipped with Forja.
4. **GPU extensions**: compiled against the environment's PyTorch.
5. **Weights**: downloaded at a pinned revision and checked by sha256. A gated Hugging Face repository needs your token in the `HF_TOKEN` environment variable.
6. **Test on this device**: imports the model and touches the GPU.

Every step can be resumed: an interrupted install continues when you run it again.

## Licenses

The Models screen shows each model's license, whether it allows commercial use, and its restrictions. A model whose license is custom (`LicenseRef-...`), non-commercial or restricted asks you to accept it once before installing; Forja records the acceptance and its time in your library. From the CLI, `forja models install` asks, and `--accept-license` accepts for scripts.

When a job runs inside a project (a folder with `forja.toml`), the asset copied into the project comes with a `<name>.forja.json` file that lists each stage's model and license, so provenance travels with the file.

## Pick the model for a stage

```sh
forja models pin shape triposg       # always use TripoSG for shape when a preset leaves it open
forja models unpin shape             # back to the highest-ranked ready model
```

The full order of choice is in [presets](presets.md#which-model-a-stage-uses).

## Models from a folder

A model package adds a model that is not in Forja's catalog: a folder with the family's `family.yaml`, a `manifests/` folder and the adapter. Add its full path to `config.toml`, restart the core (`forja daemon stop`, or quit the app), then install it like any other:

```toml
[engine]
manifest_dirs = ["/home/you/forja-models/mymodel"]
```

An adapter is code that runs on your computer with your permissions, in its own process but not in a sandbox. Add only packages whose source and author you trust. Writing one is covered in [adding a model](contributing/adding-a-model.md).

Hosted models (fal.ai) are covered in [model hubs](hubs.md).
