# Forja documentation

## Using Forja

- [Install and first asset](install.md): what you need, installing the models, making an asset, where files live.
- [Presets](presets.md): the four built-in pipelines, optional stages, overrides, your own presets, how a stage gets its model.
- [Models and licenses](models.md): the models, installing and removing them, license acceptance, models from a folder.
- [Hardware guide](hardware.md): what runs on 8, 12, 16 and 24 GB of VRAM, and on Macs.
- [Sharing a GPU over the network](sharing.md): pairing another computer with a six-digit code.
- [Model hubs (fal.ai)](hubs.md): hosted models, prices and spending limits.
- [Library, export and Blender](library.md): exporting to other formats, Open in Blender, dragging assets out, exporting into a project, the cache.
- [The forja command line](cli.md): every command, scripting, exit codes.
- [Configuration](configuration.md): the layers, the settings, the files.

Documented after integration:

- [Benchmarks](bench.md)
- [Cloud GPUs](cloud.md): RunPod, Modal, ComfyUI, the engine's Docker image
- [Routing, estimates and spending](routing.md)
- [macOS and Apple Silicon](macos.md)
- [Rigging](rigging.md)
- [Quad remesh, Smart UV and baking](remesh.md)

## Contributing

- [CONTRIBUTING.md](../CONTRIBUTING.md): setup, the test suites, commits and sign-off, pull requests.
- [Architecture](contributing/architecture.md): the three processes and what each owns.
- [The stage cache](contributing/stage-cache.md): the cache key, and when to bump a version.
- [Adding a model](contributing/adding-a-model.md): model packages, families, manifests, the pinning rules.
- [The adapter SDK](contributing/adapter-sdk.md): `forja-engine-sdk`, its types and the conformance suite.
- [Adding a stage type](contributing/adding-a-stage-type.md)
- [Adding a provider](contributing/adding-a-provider.md)
- [CI, releases and versioning](contributing/releases.md) (documented after integration)
- [The catalog](../catalog/README.md): the manifest schema and index.
- [The adapter template](../templates/adapter/README.md)
- [Decisions](decisions.md): decisions that override or sharpen the spec.
