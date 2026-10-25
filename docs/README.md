# Forja documentation

## Using Forja

- [Install and first asset](install.md): what you need, installing the models, making an asset, where files live.
- [Presets](presets.md): the four built-in pipelines, optional stages, overrides, your own presets, how a stage gets its model.
- [Rigging](rigging.md): the rig stage on Hero character, the rig models, humanoid bone names, rigs in LODs, exports and the viewer.
- [Quad remesh, Smart UV and baking](remesh.md): the Blender stages, how Forja finds Blender, replacing a stage, how the bake makes its maps.
- [Models and licenses](models.md): the models, installing and removing them, license acceptance, models from a folder.
- [Hardware guide](hardware.md): what runs on 8, 12, 16 and 24 GB of VRAM, on Macs and on rented GPUs.
- [macOS and Apple Silicon](macos.md): what a Mac runs itself, a Mac as a client of a GPU computer, building and signing the DMGs.
- [Sharing a GPU over the network](sharing.md): pairing another computer with a six-digit code.
- [Cloud GPUs](cloud.md): the engine's Docker image, RunPod serverless and pods, Modal, ComfyUI workflows.
- [Model hubs (fal.ai)](hubs.md): hosted models and their prices.
- [Routing, estimates and spending](routing.md): which provider runs a stage, fallback, estimates before queueing, the price table, spending limits.
- [Library, export and Blender](library.md): exporting to other formats, Open in Blender, dragging assets out, exporting into a project, what stays on disk.
- [Benchmarks](bench.md): `forja bench`, suites, what is measured, comparing runs, the CI gate.
- [The forja command line](cli.md): every command, scripting, exit codes.
- [Configuration](configuration.md): the layers, the settings, the files.

## Contributing

- [CONTRIBUTING.md](../CONTRIBUTING.md): setup, the test suites, commits and sign-off, pull requests.
- [Architecture](contributing/architecture.md): the three processes and what each owns.
- [Adding a model](contributing/adding-a-model.md): model packages, families, manifests, the pinning rules, when to bump a version.
- [The adapter SDK](contributing/adapter-sdk.md): `forja-engine-sdk`, its types and the conformance suite.
- [Adding a stage type](contributing/adding-a-stage-type.md)
- [Adding a provider](contributing/adding-a-provider.md)
- [CI, releases and versioning](contributing/releases.md): the one version, installers, the release workflow, CI and its runners, license scanning, pre-commit hooks.
- [The catalog](../catalog/README.md): the manifest schema and index.
- [The adapter template](../templates/adapter/README.md)
- [Decisions](decisions.md): decisions that override or sharpen the spec.
