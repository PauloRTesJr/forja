# Changelog

Every change to Forja, newest first, generated from the commit history (`make changelog`).
App, core and engine share one version.

## 0.2.0 (2026-10-25)

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
- engine: run Hunyuan3D-2mv on Apple GPUs and report Apple Silicon truthfully
- desktop: name a Mac's Apple GPU and CPU on the Models screen
- engine: measure each stage of a benchmark run (API 1.7.0)
- core: forja bench runs a suite measured and compares runs
- desktop: benchmark on the Models screen
- sdk: publish the stage interfaces as forja-engine-sdk, with a conformance suite
- templates: add an adapter template that runs as an example CPU model
- engine: add models from model packages listed in engine.manifest_dirs
- catalog: add manifest and family schemas, the catalog index and its checks
- config: add a user-editable price table
- core: route by policy and capability, fall back on failure, cap spending
- api,cli: estimate jobs before queueing and edit the price table
- desktop: show estimates, where stages run, fallbacks and the price table
- engine: serverless workers behind RunPod's queue and a Modal gateway
- docker: engine image per CUDA line, baked recipe, RunPod worker and Modal app
- core: RunPod serverless, Modal and ComfyUI providers
- add, try and remove RunPod, Modal and ComfyUI providers from the app and the CLI
- core: optional rig stage in Hero character, bones in asset stats, armatures through Blender
- desktop: skeleton overlay, bend test and Rigged badges for rigged assets
- engine: rig stage type with skinned GLBs, a stub rig, and LODs that keep the skin
- engine: UniRig and SkinTokens rig families
- engine: polish the built-in bake
- engine: quad remesh and Smart UV Project stages through Blender
- engine: Hunyuan3D-2 Paint keeps the UVs a mesh brings
- quad remesh and Smart UV Project switches for environment and hero presets
- install what a blocked optional step needs, beside its switch
- simplify instead when QuadriFlow cannot remesh a shape, and say so

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
- engine: ask Metal for the GPU's working set in a child process
- sdk: reject unknown stage types, devices and input kinds in forja-sdk test
- catalog: accept per-lane builds and smoke imports, and refresh the index
- core: refresh the model list before an install reports it finished
- fal: survive transient failures, bound waits and report billing
- jobs: meter a paid batch at one stage's rate at a time
- engine: answer 401, not 500, to a token with non-ASCII bytes
- core: charge a rented engine's time once
- ci: the bench gate compares result files and brings its reference images
- engine: keep a RunPod worker's data on the network volume
- core: keep offline-only stages off rented pods
- core: price a RunPod pod added without a rate by the price table
- build: release candidates, version kinds and the mac build match how releases work
- desktop: close the price table's style block, and build the app in make check
- security: a project file cannot run programs or loosen spending, nor a folder plant a Blender
- cli: forja engine serve passes blender.path and keeps programs off the current folder
- core: stop paid runs Forja gives up on, count what they cost, and never pay twice
- bench: name a result's report from its id, never from the result file
- core: send a ComfyUI's access token only over https or to this computer
- engine: refuse to start the Modal gateway with an empty or short token
- engine: a model package can neither replace a model nor name a path outside its folder
- check a RunPod volume's datacenter, and pin the engine images' bases by digest
- security: a pod's engine token needs 16 characters, as the Modal gateway's does
- engine: type-check the Blender lookup on every OS, and skip its stages' contract without Blender
- engine: merge sliver edges before QuadriFlow so dense shapes remesh
- engine: treat a failed QuadriFlow solve as a refusal, not a crash

### Performance

- jobs: ask a provider what it runs once per job run

### Refactoring

- core: list the API's routes and export its response types
- **Breaking:** remove the stage cache so every job runs every stage

### Documentation

- change the app and the CLI together, with a feature-parity skill
- add the README, contributing guide, code of conduct and security policy
- add user and contributor documentation, with stubs for work in flight
- add the changelog generated from the commit history
- regenerate the changelog
- decisions: record the decisions of milestones M1, M4, M5 and M7
- decisions: record how rigging works and names a humanoid's bones
- decisions: record how Blender stages run, optional replacements and the bake
- document forja bench, suites, comparisons and the CI gate
- document the engine image, RunPod, Modal and ComfyUI providers
- document routing policies, fallback, estimates, prices and spending limits
- document Apple Silicon models, a Mac as a client and the DMG builds
- document versioning, installers, the release workflow, CI and license scanning
- point the index, CLI, configuration and guides at the merged features
- document the rig stage, rig models, humanoid bone names and rigged exports
- document quad remesh, Smart UV Project, finding Blender and the bake
- point the index, presets, CLI, hardware, models and library at rigging and remesh
- follow the project trust rules in the remesh and routing pages
- the README's status and features follow the milestones
- decisions: a stage its provider billed falls back only to a free provider

### Build and packaging

- desktop: macOS DMGs for Apple Silicon and Intel, notarized when Apple credentials exist
- one version for the app, the core and the engine
- desktop: make dist packages the app for this OS
- optional pre-commit hooks that mirror CI's fast checks
- desktop: make dist packages macOS through pnpm dist:mac
- keep the adapter SDK on the shared version

### Continuous integration

- add the Apple Silicon workflow for MPS smoke tests on the maintainer's Mac
- fail on strong copyleft in shipped dependencies
- check, cross-compile and test the pipeline on GitHub-hosted runners
- GPU nightly, benchmark gate and install workflows
- release workflow for signed builds, images, checksums and notes

### Tests

- engine: wait for the core to list the installed model in the M7 gate
- engine: reject model manifests without license metadata
- generate a GLB end to end with the stub models

### Other

- use the documentation address range for ComfyUI examples

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

