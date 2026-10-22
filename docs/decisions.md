# Decisions

Decisions taken after the spec was written. Where they disagree with the spec, these win.

| # | Decision | Why |
|---|---|---|
| 1 | v1 means "works for the author": the Windows/Ubuntu RTX 4070 Ti desktop plus the Mac as a LAN client, ending at M3–M4. Public-only work (installers for every OS, signing, Flatpak, ROCm, Intel Mac/ARM, third-party catalogs, PyPI SDK, catalog signing, translations) moves to a later "1.0 public" milestone. Interfaces stay pluggable. | Eight milestones in three languages is too much for one part-time developer; build only for users that exist. |
| 2 | The everyday engine runs on **Windows**, on the same GPU as Unity. The Mac is mostly for web/mobile work, not games. | Sharing the GPU with Unity is the normal case: idle unload and the pause toggle are core features, not extras. |
| 3 | The core owns jobs, plan, durable state and the stage cache index. The engine only executes stage batches and stores artifacts by hash. | One source of truth for "was this computed?" across local, LAN and cloud providers. |
| 4 | M0 gate: write the acceptance checklist before running anything; run a cloud arm (Hunyuan3D 2.1 PBR, TRELLIS.2 on a rented 24GB+ GPU) on the same 20 images. Local passes → continue as specced. Only cloud passes → default texturing routes to cloud and the claim becomes "local where it fits". Neither passes → stop and revisit in six months. | Makes the gate decidable and separates "12GB is too small" from "the models are not there yet". |
| 5 | When "fully offline" conflicts with speed budgets, offline wins: the default `fit_mode` is `fit-first` (offload, tiling); the 90 s target is re-measured on that path. | Offline is a v1 goal; speed numbers are targets. |
| 6 | Local texture output is base color + baked normal + baked AO, with roughness and metallic as editable per-asset constants. "PBR" is used only for outputs from models that produce it (Hunyuan3D 2.1 Paint, TRELLIS.2). | Hunyuan3D-2 Paint produces delit RGB only; nothing local produces roughness or metallic. |
| 7 | v1 is image-in only; the input image carries the art style. LoRA loading and text-to-image leave v1. First post-v1 feature: text-to-image with a style LoRA. | Style is set in 2D; there is no LoRA ecosystem for the 3D models. |
| 8 | GLB only until M6; FBX arrives with rigging, through Blender. | glTFast handles static props; FBX matters only for retargeting rigged characters. |
| 9 | Apache-2.0 for Forja's own code. Adapters for restrictively licensed families fetch pinned upstream source at install time and never commit it here. | Hunyuan3D repositories ship code under the same territory-restricted license as the weights (confirm in M0). |
| 10 | The 90 s success target refers to the Mobile prop preset (there is no "Standard" preset). | Matches the budget table. |
