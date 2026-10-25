# macOS and Apple Silicon

A Mac with Apple Silicon runs some models itself, on its GPU or its CPU, and sends the rest to another computer: a PC on your network ([sharing](sharing.md)), a rented GPU ([cloud](cloud.md)) or a model hub ([hubs](hubs.md)). Its library stays on the Mac.

## What runs on a Mac

A Mac runs locally only what each model's pinned upstream code runs on Apple hardware:

| Model | Stage | On Apple Silicon |
|---|---|---|
| BiRefNet, BiRefNet lite | Background removal | On the CPU, about 6 seconds for a 1024×1024 cut-out. ONNX Runtime has no Metal provider, and its CoreML provider hung compiling this model |
| Hunyuan3D-2mv | Shape, from the front plus side and back views | On the Apple GPU, in fp16. **Experimental**: upstream supports macOS and its shape pipeline is plain PyTorch, but it stays experimental until the [Apple Silicon workflow](#the-apple-silicon-workflow) has run it on a Mac |
| TripoSG | Shape | No: its mesh extraction needs a CUDA kernel (diso) |
| Hunyuan3D-2 Paint | Texture | No: it renders through a CUDA rasterizer |

The rig models (UniRig, SkinTokens) need CUDA too. Models a Mac cannot run show "Cannot run here" on the Models screen, and their stages go to another provider.

With BiRefNet and Hunyuan3D-2mv installed, a Mac runs the Draft preset by itself. The textured presets need another computer, a rented GPU or a hosted model for the texture stage.

An Intel Mac has no Apple GPU for the engine to use: of these models, it runs only BiRefNet, on its CPU.

### The Apple GPU's memory

Unified memory has no VRAM total, so the Apple GPU's budget is Metal's recommended working set, the memory Metal lets the GPU keep resident (about two thirds of the RAM on Macs up to 32 GB), less the usual reserve (`engine.vram_reserve_gb`, 1 GB). Hunyuan3D-2mv needs 6 GB of that: a Mac with 16 GB of memory runs it, and an 8 GB Mac shows it as too big. On the Models screen, a model on the Apple GPU says "Apple GPU", and BiRefNet says "Runs on the CPU".

### Install the models

The Models screen's setup card works as on other computers ([install](install.md#install-the-models)). From a terminal:

```sh
forja models install birefnet
forja models install hunyuan3d2-mv    # asks you to accept its license first
```

On a Mac, the Hunyuan3D-2 family installs only the 2mv shape model: PyPI's macOS arm64 PyTorch wheels carry the Apple GPU support, and Paint's packages and CUDA build tools are left out. Hunyuan3D-2mv's license is Tencent's community license, which does not cover the EU, the UK or South Korea ([models and licenses](models.md)).

An Intel build running under Rosetta sees no Apple GPU. Forja's check for the model says so, and asks for the Apple Silicon build (the arm64 DMG), and for uv for Apple Silicon if yours came from an Intel Homebrew in `/usr/local`.

## A Mac as a client of a GPU computer

The everyday setup ([decision 1](decisions.md)): the Mac makes the assets, a Windows or Linux PC with an NVIDIA GPU runs the models.

1. On the PC, turn on **Share this computer's GPU** in Settings (or `forja share on`), and note its address and pairing code.
2. On the Mac, Settings, **Add another computer**, with that address and code (or `forja providers add 192.0.2.10 --code 482913`).
3. Install the models on the PC from the Mac: the Models screen lists the PC's models too, with its name under "Runs on", or `forja models install triposg --provider <pc>`.

With the default policy, **This computer first**, the Mac runs each stage it has a ready model for and sends the rest to the PC: with BiRefNet installed on the Mac, the cut-out runs on its CPU, and shape and texture on the PC. To send a stage to the PC anyway, pick its model under the PC's name on Generate, or pin it there:

```sh
forja models pin background-removal birefnet --provider <pc>
```

Or set the policy to **Fastest** (`routing.policy = "fastest"`): after a few runs, each stage goes where it ran fastest ([routing](routing.md)).

## Build the DMGs

On a Mac, from a checkout set up with `make setup`:

```sh
make dist
```

On macOS this cross-compiles both darwin builds of the core (`make core-all`), then packages the app with `pnpm dist:mac`. `apps/desktop/release/` then holds a DMG and a zip for Apple Silicon and for Intel: `Forja-<version>-mac-arm64.dmg`, `Forja-<version>-mac-x64.dmg` and their `.zip` twins. Each package carries both core binaries, and the app starts the one for its architecture. They need macOS 13 or newer.

A package carries the app and the core, not the engine. The core runs `forja-engine` from your `PATH`, or the command in `config.toml`, for example an engine in a checkout:

```toml
[engine]
command = ["uv", "run", "--quiet", "--project", "/Users/<you>/forja/engine", "forja-engine"]
```

### Signing and notarization

The build signs and notarizes when it finds Apple's credentials in the environment:

| Variable | What it is |
|---|---|
| `CSC_LINK` | A Developer ID Application certificate, as a `.p12` file |
| `CSC_KEY_PASSWORD` | Its password |
| `CSC_NAME` | Instead of `CSC_LINK`: the name of a certificate already in your keychain |
| `APPLE_ID` | The Apple ID that notarizes |
| `APPLE_APP_SPECIFIC_PASSWORD` | An app-specific password of that Apple ID |
| `APPLE_TEAM_ID` | The developer team |

- **With a certificate**, the app is signed under the hardened runtime, whose only entitlement is the JIT that V8 needs, and notarized when the three `APPLE_` variables are set.
- **Without one**, the build says so and signs the app ad hoc: Apple Silicon Macs refuse to open an app with no signature at all. An ad-hoc signature has no team, so that build leaves the hardened runtime off, and there is nothing to notarize. macOS blocks the first open of an app that is not notarized; after that first try, System Settings, Privacy & Security, offers to open it anyway.

The release workflow builds the DMGs on a GitHub-hosted Mac with the same variables, from repository secrets ([releases](contributing/releases.md)).

## The Apple Silicon workflow

`.github/workflows/apple-silicon.yml` runs by hand, before a release, on the maintainer's Mac registered as a self-hosted runner with the labels `self-hosted`, `macOS` and `ARM64`. Only people with write access can start it, and a fork's copy skips its job. It:

1. runs the engine tests on the Apple GPU (`pytest -m mps`) and the whole engine suite, on a native arm64 Python that detects the `mps` lane;
2. builds the core and runs a Draft job end to end with the stub models;
3. with the `real_model` input (on by default), generates a shape with Hunyuan3D-2mv on the Apple GPU, if it is installed in the runner's own data folder (`FORJA_CI_DATA_DIR` in the runner's `.env`, else `~/forja-ci`), and checks that the stage ran on `mps`.

Hunyuan3D-2mv stays experimental on Macs until this passes ([decision 26](decisions.md)).
