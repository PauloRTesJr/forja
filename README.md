# Forja

Forja turns a reference image into a game-ready 3D asset: a GLB with a low-poly mesh, a base color texture, baked normal and ambient occlusion maps, and LODs. It runs open models on your own GPU, on another computer on your network, or on a hosted model hub you connect. The desktop app and the `forja` command line do the same things through one local service.

## Status

Forja is pre-1.0 and works for its author: Windows with an NVIDIA GPU (built and tested on a 12 GB RTX 4070 Ti), with a Mac joining as a client of that GPU over the network. Unsigned Windows installers are on the [Releases](https://github.com/PauloRTesJr/forja/releases) page; the release workflow also builds macOS and Linux packages, which are not yet tested on those systems. Expect breaking changes until 1.0.

What works today:

- Four presets, from an untextured draft to a 30,000-triangle hero character ([presets](docs/presets.md)).
- Local models: BiRefNet (background removal), TripoSG and Hunyuan3D-2mv (shape), Hunyuan3D-2 Paint (texture). Install them from the app or the CLI; each gets its own Python environment ([models](docs/models.md), [hardware](docs/hardware.md)).
- Side and back views, and turnaround sheets split into views.
- A library that exports to GLB, FBX, OBJ, glTF, USDZ, STL and .blend, opens assets in Blender, and drags them into other apps ([library](docs/library.md)).
- Sharing a GPU with another computer by a six-digit pairing code ([sharing](docs/sharing.md)), and fal.ai's hosted models with a spending budget ([model hubs](docs/hubs.md)).
- Rigging a character with UniRig or SkinTokens, with standard humanoid bone names that game engines retarget to, kept through the LODs and the FBX, glTF and .blend exports ([rigging](docs/rigging.md)).
- Quad remesh and Smart UV Project through Blender, and a bake in MikkTSpace tangents ([remesh and bake](docs/remesh.md)).
- Rented GPUs: the engine's Docker image, RunPod serverless and pods, Modal, and a ComfyUI bridge ([cloud](docs/cloud.md)); routing by price or speed, estimates before a job runs, an editable price table and hard spending limits ([routing](docs/routing.md)).
- A Mac running Hunyuan3D-2mv's shape on its GPU and sending the rest to another computer ([macOS](docs/macos.md)).
- `forja bench`: a measured benchmark suite with a regression check ([benchmarks](docs/bench.md)).
- Every feature from the command line too ([CLI](docs/cli.md)).

## Quick start

You need Git, [Go](https://go.dev/dl/), [uv](https://docs.astral.sh/uv/), Node.js 22 or newer with pnpm 10 (`corepack enable`), and `make` (on Windows, run it from Git Bash).

```sh
git clone https://github.com/PauloRTesJr/forja.git
cd forja
make setup      # Python, Go and Node dependencies
make desktop    # builds the core and starts the app
```

Until the presets' models are installed, the Models screen shows a setup card: it checks what your computer needs, asks you to accept the models' licenses, installs them and runs a test. Then drop an image on Generate.

From a terminal instead:

```sh
make core
core/bin/forja setup             # install the models the presets need, then test them
core/bin/forja generate photo.png
```

No GPU? `forja config set models.stubs=true` turns on stub models: deterministic CPU stand-ins that exercise the whole pipeline. Their output is a placeholder, not a real asset.

The full walk-through is [Install and first asset](docs/install.md).

## Documentation

[docs/README.md](docs/README.md) lists every guide: using Forja, the hardware guide, and how to add a model, a stage type or a provider.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md). Commits are signed off under the [DCO](https://developercertificate.org/); there is no CLA. Report security problems privately as described in [SECURITY.md](SECURITY.md).

## License

Forja's own code is [Apache-2.0](LICENSE). Models are not part of Forja: install downloads each from its publisher, under its own license, which the Models screen shows before you accept it. Some are restricted; for example, the Hunyuan3D-2 license does not cover the EU, the UK or South Korea. Upstream model code with such a license is fetched at install time and never stored in this repository.
