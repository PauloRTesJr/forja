# Hardware guide

What runs where, from the VRAM figures in the model manifests. Your own numbers will differ a little: the engine measures each model's real peak on your GPU and plans with that.

## How Forja decides

- **Budget.** Models get the GPU's memory minus a reserve for the desktop and other apps: 1 GB by default (Settings, "Keep VRAM free for other apps"; `engine.vram_reserve_gb`). Memory other programs hold at the moment, a game or Unity for instance, also comes off.
- **Fit.** For each model the Models screen shows *fits*, *offload* (runs with part of the model in system RAM, slower), *too big* (below the least the model runs in) or *unsupported* (no GPU it can use). Setup offers only models that fit or offload.
- **When a model does not fit** (Settings, "When a model does not fit in VRAM"; `engine.fit_mode`): `fit-first`, the default, runs it with offload; `quality-first` refuses, so you can route the stage to another computer instead.
- **One at a time.** A GPU runs one model stage at a time. Loading a model that does not fit beside the loaded ones unloads the least recently used. Queued jobs that need the loaded model go first, up to eight in a row, so batches swap models less.
- **Idle.** After 15 minutes without work the engine unloads every model (Settings, "Free GPU memory when idle"). `forja engine unload` frees the GPU at once, before a game.

## By VRAM

Figures are for NVIDIA GPUs; the shape and texture models run only on CUDA.

| | 8 GB | 12 GB | 16 GB | 24 GB |
|---|---|---|---|---|
| Background removal: BiRefNet (8 GB¹) / lite (0.8 GB) | lite fits; general offloads | fits | fits | fits |
| Shape: TripoSG (8 GB, needs 8) | too big | fits | fits | fits |
| Shape with side views: Hunyuan3D-2mv (6 GB, needs 6) | fits | fits | fits | fits |
| Texture: Hunyuan3D-2 Paint (12.5 GB, needs 10) | too big | offload | fits | fits |
| Presets that run locally | Draft | all | all | all |

¹ BiRefNet's figure is its ONNX Runtime arena on a 12 GB card, which grows to the free memory; it runs in much less.

- **8 GB** (RTX 3060 Ti, 3070, 4060): cut-outs and shapes, with Hunyuan3D-2mv. Texture on another computer ([sharing](sharing.md)) or a hosted model ([hubs](hubs.md)), or use the Draft preset.
- **12 GB** (RTX 3060 12 GB, 4070, 4070 Ti): every preset. Hunyuan3D-2 Paint runs with offload, so the texture stage is the slow one. This is the setup Forja is built and tested on.
- **16 GB** (RTX 4060 Ti 16 GB, 4080, 5080): every preset without offload. Shape and texture models take turns on the GPU.
- **24 GB** (RTX 3090, 4090): TripoSG and Hunyuan3D-2 Paint fit side by side (20.5 GB), so jobs in a row reload models less often. BiRefNet lite leaves them more room than BiRefNet general.

Larger texture models that paint full PBR materials need more than 24 GB and are not in the catalog yet.

## Other computers

- **Apple Silicon Macs** run BiRefNet (on the CPU, about 6 seconds a cut-out). Shape and texture models need CUDA, so a Mac uses a GPU computer on the network or a hosted model. macOS builds and Apple GPU support are documented after integration ([macOS](macos.md)).
- **AMD GPUs and computers without a GPU** have no local shape or texture model yet. The stub models (`models.stubs = true`) run the pipeline anywhere, with placeholder output.

## Disk and memory

Plan for the models' disk (environment and weights): about 4 GB for BiRefNet, 12.5 GB for TripoSG, 5 GB for Hunyuan3D-2mv (which shares Hunyuan3D-2 Paint's environment) and 23 GB for Hunyuan3D-2 Paint. Offload keeps part of a model in system RAM, so leave room for it there. The stage cache is capped at 50 GB by default ([library](library.md#the-cache)).
