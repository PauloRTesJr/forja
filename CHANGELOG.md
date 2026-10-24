# Changelog

Every change to Forja, newest first, generated from the commit history (`make changelog`).
App, core and engine share one version.

## Unreleased

### Features

- built-in presets run the real models and say when one is missing
- guided model setup with checks, fixes, licenses and live progress
- settings as real controls instead of JSON
- add other computers by pairing code, keep their keys in the keychain
- core: FORJA_SOCKET picks the core's socket or pipe
- engine: bake the colors of a textured high-poly and keep textures through cleanup
- core: skip stages a picked model already does, and enforce spending limits
- core: fal.ai as a model hub provider
- desktop: quality and a model per step on Generate; hosted models from fal.ai
- build one asset from several views of its subject
- desktop: prepare and confirm dropped images on Generate
- core: read asset stats from the GLB
- cli: reach every feature of the desktop app from the command line
- desktop: paste images on Generate with Ctrl/Cmd+V
- presets: explain optional stages beside their switch
- library: delete assets, export them to other formats and open them in Blender
- core: keep the stage cache under cache_max_gb
- desktop: drag assets out of the window into Blender, Unity or a folder
- desktop: compare a stage's models in one aligned list
- desktop: one card for the image and its recipe on Generate
- desktop: use the forge emblem as the app icon and in-app mark

### Fixes

- engine: keep model interpreters in Forja's data folder
- engine: keep Hunyuan's license restrictions whole
- desktop: keep the setup panel until its test run
- engine: repair the winding of textured meshes across UV seams
- core: hosted models the app can show, pictures it can load, steps it can hide
- desktop: catalog pictures through the core, model ids, no switch for a skipped step
- core: read fal.ai's catalog without the key, and price one generation
- desktop: an unusable hosted model no longer blanks the window
- engine: send Hunyuan3D-2 Paint the views it reads
- core: look for a sheet of views before a job takes a slot
- desktop: stop the stage track from flickering a scrollbar while a stage runs

### Refactoring

- core: list the API's routes and export its response types

### Documentation

- change the app and the CLI together, with a feature-parity skill

### Build and packaging

- one version for the app, the core and the engine
- desktop: make dist packages the app for this OS
- optional pre-commit hooks that mirror CI's fast checks

### Continuous integration

- fail on strong copyleft in shipped dependencies
- check, cross-compile and test the pipeline on GitHub-hosted runners
- GPU nightly, benchmark gate and install workflows
- release workflow for signed builds, images, checksums and notes

### Tests

- engine: reject model manifests without license metadata
- generate a GLB end to end with the stub models

## 0.1.0 (2026-10-23)

### Features

- engine: supervisor, model workers, scheduler, VRAM manager and stub pipeline
- core: Go daemon and CLI with layered config, planner, stage cache, providers and export
- desktop: Electron app with live queue, library, render-on-demand viewer and thumbnails
- engine: real model families for BiRefNet, TripoSG and Hunyuan3D-2 Paint, plus the M0 spike kit
- engine: high-to-low normal and AO bake stage with written tangents
- engine: build Hunyuan paint in RAM and offload when it does not fit
- engine: texture.match stage pulls painted textures back toward the reference
- core: key the stage cache by what the routed provider runs
- desktop: Arcane Forge theme and phase-grouped queue rows

### Fixes

- engine: warn in the engine log when the bake cage misses the high-poly
- engine: stop model workers deadlocking on Windows when loading DLLs
- core: stop the whole engine process tree on Windows
- engine: find and check the CUDA and MSVC toolchain before building extensions
- engine: force CUDA when building family extensions
- engine: import torch before the Hunyuan rasterizer in the install smoke test
- engine: fetch the fp32 Hunyuan paint UNet that upstream actually loads
- engine: pin cuDNN below 9.21 for BiRefNet on CUDA
- engine: plan VRAM from measured device memory and offload before load
- engine: declare the VRAM BiRefNet and Hunyuan paint really use
- engine: keep NaN tangents out of the glTF JSON that broke library models

### Documentation

- remind agents the repo is open source and must not leak credentials
- bench: Windows toolchain for the M0 CUDA extensions

### Build and packaging

- desktop: pin pnpm 10 through packageManager
- check out every text file with LF line endings
- run Go tests with -race only when cgo is available

### Tests

- engine: compare the TripoSG weights path with the OS path separator
- core: check export file modes only where Unix permission bits exist

### Other

- core: go mod tidy

