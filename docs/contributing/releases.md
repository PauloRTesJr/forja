# CI, releases and versioning

How a change is checked, how a version is cut, and what a release publishes.

## One version

The root `VERSION` file is the one version of the app, the core, the engine, its model families and the adapter SDK ([decision 28](../decisions.md)). The engine API keeps its own version (`engine/src/forja_engine/api/version.py` and `core/internal/engineapi/version.go`).

```sh
make version              # print it
make version V=0.2.0      # write it everywhere, then refresh engine/uv.lock
make version V=0.2.0-rc.1 # a prerelease: -rc.N, -beta.N or -alpha.N
```

The release workflow publishes `-rc.N` and `-beta.N` prereleases; an `-alpha.N` version has no release tag.

`make version V=` (`scripts/version.py`) writes `VERSION`, `apps/desktop/package.json`, `engine/pyproject.toml` (the version and the exact `forja-engine-sdk` pin), `engine/src/forja_engine/__init__.py`, `engine/sdk/pyproject.toml`, `engine/sdk/src/forja_sdk/__init__.py` and each `engine/families/*/pyproject.toml`. The core is not written: `make core` and `make core-all` stamp the version into the binary when they build it. `uv run --no-project python scripts/version.py --check` fails when a file disagrees with `VERSION`; CI and the release workflow run it.

## Build the installers

```sh
make dist
```

It builds this computer's installers into `apps/desktop/release/`, after building the core. A package carries the app and the core, not the engine, and is unsigned unless signing credentials are in the environment.

| Built on | Packages | Signed with |
|---|---|---|
| Windows | NSIS installer (`Forja-<version>-win-<arch>-setup.exe`) and a zip | Authenticode, when `WIN_CSC_LINK` and `WIN_CSC_KEY_PASSWORD` are set; otherwise SmartScreen warns on first run |
| macOS | DMG and zip for Apple Silicon and Intel, with both core binaries | A Developer ID, notarized: see [macOS](../macos.md#signing-and-notarization) |
| Linux | AppImage and .deb | Not signed |

Every file is named `Forja-<version>-<os>-<arch>`, so all of them sit in one GitHub Release next to `SHA256SUMS`. `make core-all` cross-compiles the core alone for six targets (macOS, Linux and Windows, each on amd64 and arm64) into `core/bin/forja-<os>-<arch>`.

## Changelog and release notes

The changelog is generated from the Conventional Commits history with git-cliff, which `make` runs through `uv tool run`, pinned in the Makefile; `cliff.toml` configures it.

```sh
make changelog   # regenerate CHANGELOG.md
make notes       # print the newest tag's release notes
```

Commits are grouped as Features (`feat`), Fixes (`fix`), Performance (`perf`), Refactoring, Documentation, Build and packaging (`build`), Continuous integration (`ci`), Tests and Other (`chore`, `style`, `revert`). Commits that do not follow the convention are left out, and so is `chore(release)`. A breaking change is marked **Breaking:**. For a stable version, `make notes` skips the prerelease tags, so the notes cover everything since the previous stable release, its release candidates' changes included.

## Cut a release

1. `make version V=X.Y.Z`, commit, and tag that commit `vX.Y.Z`.
2. For a stable release, the GPU nightly and the benchmark gate must have passed on that commit first: start both by hand on it (Actions, Run workflow), or push a release candidate tag on it, which runs both.
3. Push the tag. The release workflow does the rest.

| Tag | Release | Channel |
|---|---|---|
| `vX.Y.Z` | Stable, marked latest | `stable` |
| `vX.Y.Z-rc.N`, `vX.Y.Z-beta.N` | Prerelease | `beta` |

### What the release workflow does

`.github/workflows/release.yml`, on a matching tag:

1. **Checks the version**: the tag must equal `VERSION`, and every file must carry it.
2. **GPU gates**, stable releases only: `gpu-nightly.yml` and `bench-gate.yml` must each have a successful run on the tagged commit. A repository without the GPU runner (`FORJA_GPU_RUNNER` not `true`) releases without them, with a warning.
3. **Core**: `make core-all` with the release version.
4. **Desktop**: `make dist` on Windows, macOS and Linux runners, signing with the secrets below when they exist.
5. **Engine images**: `docker/engine/Dockerfile` for `cu124` and `cpu`, pushed to the GitHub Container Registry as `ghcr.io/<owner>/forja-engine:<version>-<cuda line>`, and as `latest-<cuda line>` for a stable release or `beta-<cuda line>` for a prerelease. Each image gets a build provenance attestation, pushed to the registry with it.
6. **Publish**: for a stable release, the benchmark gate's results (`bench-results.tar.gz`), which become the next baseline ([benchmarks](../bench.md#the-ci-gate)); a `SHA256SUMS` file over every file; a build provenance attestation for every file it lists; release notes from `make notes`; then the GitHub Release, created from the existing tag.

To check a download:

```sh
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify Forja-0.2.0-mac-arm64.dmg --repo <owner>/forja
```

### Signing secrets

Repository secrets, read by name. Each is optional: without a platform's secrets its packages are unsigned (and Macs signed ad hoc), and the workflow says so.

| Secret | For |
|---|---|
| `WIN_CSC_LINK`, `WIN_CSC_KEY_PASSWORD` | Windows: the Authenticode certificate and its password |
| `CSC_LINK`, `CSC_KEY_PASSWORD` | macOS: the Developer ID Application certificate and its password |
| `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, `APPLE_TEAM_ID` | macOS notarization |

## The CI workflows

| Workflow | Runs on | When | What |
|---|---|---|---|
| `ci.yml` | GitHub-hosted | Pushes to `main`, pull requests, by hand | `make check` and `pnpm build` on Ubuntu, Windows and macOS; `make core-all`; versions, the scripts' lint, `make licenses` and actionlint; except on pushes, also `make integration` (a GLB end to end with the stub models) on all three OSes and the benchmark smoke test with the stub suite |
| `gpu-nightly.yml` | The GPU runner | Daily at 03:17 UTC, `v*` tags, by hand | Installs the real models and generates the engine's chest fixture and any reference images, with a fresh library and store every run |
| `bench-gate.yml` | The GPU runner | `v*-rc*` tags, by hand | The [benchmark gate](../bench.md#the-ci-gate) |
| `install.yml` | GitHub-hosted, and the GPU runner | Mondays at 04:41 UTC, by hand | A first install as a user's runs: BiRefNet lite on the CPU lane on Ubuntu, Windows and macOS, then a job with it; on the GPU runner, every CUDA family built from nothing, without weights |
| `apple-silicon.yml` | The maintainer's Mac | By hand | MPS tests and Hunyuan3D-2mv on the Apple GPU ([macOS](../macos.md#the-apple-silicon-workflow)) |
| `release.yml` | GitHub-hosted | Version tags | [Above](#what-the-release-workflow-does) |

Every workflow pins its actions by commit. `.github/actions/setup` installs the toolchains at the versions the repository pins (Go from `core/go.mod`, uv at the version the action names, Node and pnpm from `apps/desktop`) and the dependencies from the lockfiles, with caches; a job turns parts off with its `go`, `python` and `node` inputs.

### Self-hosted runners

GPU work runs on the maintainer's own machines, registered as self-hosted runners of the repository (Settings, Actions, Runners):

- **The GPU runner**: Linux with an NVIDIA GPU, labelled `self-hosted`, `linux` and `gpu`. Families that compile CUDA code need the driver, the CUDA toolkit (`nvcc`) and a C++ compiler on it ([the M0 setup](../../bench/m0/README.md)).
- **The Mac**: Apple Silicon, labelled `self-hosted`, `macOS` and `ARM64`, for `apple-silicon.yml`.

A self-hosted runner must never run code from a pull request. None of these workflows has a `pull_request` trigger, their jobs refuse one anyway, and only people with write access can start a workflow by hand. The GPU workflows run only where the repository variable `FORJA_GPU_RUNNER` is `true`, so forks and a repository without the runner skip them instead of queueing forever.

Repository variables (Settings, Secrets and variables, Actions, Variables); `FORJA_GPU_RUNNER` turns the GPU workflows on, and the others are optional:

| Variable | Default | What it does |
|---|---|---|
| `FORJA_GPU_RUNNER` | unset | `true` enables the GPU workflows and the release's GPU gates |
| `FORJA_GPU_DATA_DIR` | `~/.cache/forja-ci` | The runner's data folder, kept between runs, so model environments and weights install once |
| `FORJA_GPU_REFS_DIR` | unset | A folder of reference images on the runner, which the GPU nightly generates besides the chest fixture (the benchmark set is not committed) |
| `FORJA_GPU_MODELS` | `birefnet triposg hunyuan3d2-paint` | The models the GPU workflows install |

On the Mac, `FORJA_CI_DATA_DIR` in the runner's `.env` file (default `~/forja-ci`) is the data folder that holds Hunyuan3D-2mv, installed there once by hand, its license accepted.

## License scanning

```sh
make licenses
```

It fails when something Forja ships carries a license that would bind Forja's own code ([decision 29](../decisions.md)):

- **The core's Go modules**: go-licenses, for Linux, macOS and Windows builds, fails on any forbidden, restricted or unknown license, because the binary links them statically.
- **The engine's Python dependencies**: `scripts/licenses.py python` reads the runtime dependencies of the engine's environment, dev tools and model families left out.
- **The app's npm packages**: `scripts/licenses.py npm` reads `pnpm licenses list --prod`.

For Python and npm, strong copyleft fails (GPL, AGPL, SSPL, EUPL, OSL and the like); weak copyleft such as LGPL and MPL passes, since those libraries stay separate, replaceable files. A package whose license metadata misleads the scan goes in `EXCEPTIONS` in `scripts/licenses.py`, with the reason. Model families' upstream code is not a dependency: it is fetched at install time under its own license, which each manifest declares and `engine/tests/test_manifest_licenses.py` checks.

## Pre-commit hooks

```sh
make hooks
```

installs optional hooks that run the fast checks before each commit (`.pre-commit-config.yaml`, through `uv tool run pre-commit`). Each runs only when the commit touches the files it covers, and all of them run locally: ruff on the engine and on `scripts/`, the manifests' license test, gofmt and `go vet` on the core, eslint and tsc on the app, the version check, and actionlint on the workflows. `make check` stays the full gate.
