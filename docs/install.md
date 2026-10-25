# Install and first asset

## What you need

- **A computer to run models on.** Windows 10 or 11 with an NVIDIA GPU and a current driver is the tested setup. With 12 GB of VRAM every preset runs locally; with 8 GB, shapes do but textures do not. The [hardware guide](hardware.md) has the details. Without such a GPU, use another computer's ([sharing](sharing.md)) or a hosted model hub ([hubs](hubs.md)).
- **Disk space** for the models you install: about 4 GB for BiRefNet, 12.5 GB for TripoSG and 23 GB for Hunyuan3D-2 Paint.
- **Build tools, once.** Models whose GPU code is compiled at install time need the CUDA toolkit 12.x and a C++ compiler (Visual Studio's C++ tools on Windows). Forja checks for them before installing and offers a fix for each one it can install itself.

## Get Forja

There are no installers yet (installers are documented after integration); run Forja from source:

```sh
git clone https://github.com/PauloRTesJr/forja.git
cd forja
make setup
```

You need Git, [Go](https://go.dev/dl/), [uv](https://docs.astral.sh/uv/), Node.js 22 or newer with pnpm 10 (`corepack enable`), and `make`. On Windows, run `make` from Git Bash.

Start the app with `make desktop`. For the command line alone, `make core` builds `core/bin/forja`; put that folder on your `PATH` or call it by its path.

## Install the models

**In the app:** the Models screen shows a setup card until the presets' models are installed. It works through four steps:

1. **What this computer needs**: GPU driver, CUDA toolkit, C++ tools, uv and free disk. A failed check has a fix button or the steps to follow. The toolkit and compiler are only used during installs.
2. **Licenses**: models with custom, restricted or non-commercial licenses ask you to accept them once. See [models and licenses](models.md).
3. **Install**: builds each model family's Python environment, downloads the pinned code and weights, verifies them, and tests the model on your GPU.
4. **Test run**: generates an asset from a sample image.

Models the computer cannot run are not offered; the card says which stages need another computer or a hub.

**From a terminal:**

```sh
forja setup          # shows the plan, asks, installs, then runs a test
forja setup --yes --accept-license   # the same without questions
```

Installs resume where they stopped if interrupted: run the same command again.

## Make your first asset

Pick an image of one subject: a prop, a character or a building, on a plain background or already cut out. The image carries the art style; Forja keeps it.

**In the app:** drop or paste the image on Generate, pick a preset, and press Generate. The Queue shows each stage as it runs, and the untextured shape appears as soon as it exists. The finished asset lands in the Library.

**From a terminal:**

```sh
forja generate chest.png                      # the project's default preset, Mobile prop
forja generate chest.png --preset draft       # untextured, to judge the shape first
forja generate chest.png --view back=back.png # another view of the same subject
forja assets get <asset-id> -o chest.glb
```

`forja generate` follows the job until it ends and prints the asset id. Run it inside a project (a folder with a `forja.toml`) and the asset is also copied into the project's export folder; see the [library](library.md).

An image showing several views side by side (a turnaround sheet) is used whole unless you ask for it to be split: `--sheet split`, or `--sheet ask` to confirm the views first. The app asks.

## Try it without a GPU

Stub models are deterministic CPU stand-ins used by the tests. They run the whole pipeline in seconds, so you can try the app and the CLI anywhere, but their output is a placeholder, not a real asset.

```sh
forja config set models.stubs=true
```

## Where Forja keeps things

| What | Windows | macOS | Linux |
|---|---|---|---|
| Settings (`config.toml`, presets) | `%APPDATA%\forja` | `~/Library/Application Support/forja` | `~/.config/forja` |
| Library, cache, model environments and weights | `%LOCALAPPDATA%\forja` | `~/Library/Application Support/forja` | `~/.local/share/forja` |

`general.data_dir` in `config.toml` moves the second folder, weights included (for example, to a larger drive); `forja config path` prints the files in use. The `FORJA_DATA_DIR` and `FORJA_CONFIG_DIR` environment variables override both folders, which is how a development build stays apart from an installed one.
