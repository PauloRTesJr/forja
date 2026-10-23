# M0 spike

Answers one question before any more product work: **do open models on the 12GB RTX 4070 Ti
produce acceptable Mobile prop assets?** The gate and its outcomes are in
[checklist.md](checklist.md); decision 4 in [docs/decisions.md](../../docs/decisions.md) says
why it is decided this way.

Pipeline (the built-in Mobile prop preset, with its models pinned in [forja.toml](forja.toml)):
BiRefNet → framing → TripoSG → cleanup → 5k-triangle decimation → Hunyuan3D-2 Paint (turbo, 1K
texture) → bake (normal + AO from the high-poly) → LODs → GLB.

The GLB carries base color, a tangent-space normal map, and an ORM map (R = AO; G and B white,
so roughness and metallic stay editable constant factors, decision 6). Vertex normals and
tangents are written into the file, so Unity shades it exactly as it was baked.

Not covered yet, and still part of the M0 milestone in the spec:
- **Hunyuan3D 2.1 shape**, as a second shape model to compare.
- **The cloud arm**: Hunyuan3D 2.1 PBR and TRELLIS.2, which need 24 GB or more.

## 1. Machine setup (Ubuntu, once)

The spec makes Ubuntu on the RTX 4070 Ti the reference machine. Two model families compile
CUDA code at install, so they need more than the NVIDIA driver:

| Need | Why | Check |
|---|---|---|
| NVIDIA driver 550+ | CUDA 12.4 runtime used by torch 2.5.1 | `nvidia-smi` |
| CUDA toolkit 12.4–12.6 (`nvcc`) | compiles `diso` (TripoSG) and `custom_rasterizer` (Hunyuan) | `nvcc --version` |
| `build-essential` | C++ compiler for those extensions | `g++ --version` |
| uv, Go 1.25+, Node + pnpm | Forja itself | `make setup` |

If `nvcc` is not on `PATH`, export `CUDA_HOME=/usr/local/cuda` before installing.

On Windows, `build-essential` becomes three Visual Studio Installer components. CUDA 12 rejects
the compilers that ship with Visual Studio 2026 (MSVC 14.5x), so the installer picks the newest
toolset nvcc accepts, sets up its environment itself, and stops early if a piece is missing:

| Need | Component or package |
|---|---|
| CUDA toolkit 12.6 | `winget install --id Nvidia.CUDA --version 12.6 -e` |
| MSVC v143 build tools | `Microsoft.VisualStudio.Component.VC.14.44.17.14.x86.x64` |
| Windows 11 SDK (C runtime headers) | `Microsoft.VisualStudio.Component.Windows11SDK.26100` |

## 2. Install the models (about 20 GB of weights, 15–30 min)

```bash
make core
./core/bin/forja models install birefnet triposg hunyuan3d2-paint
```

Each model gets its own environment under `<data>/envs/<family>`. The install pins the
upstream code and weights by commit and sha256 and verifies them. It ends with a GPU smoke
test. It is safe to re-run: a failed step resumes where it stopped. `./core/bin/forja models ls`
should show all three as `ready` with device `cuda:0`.

The app does the same from the Models screen, with each check above as a row and a button to
fix it. `./core/bin/forja models checks` prints the checks here; the install asks you to accept
Hunyuan's license once (`--accept-license` skips the question).

Hunyuan3D-2 is under the Tencent Hunyuan 3D 2.0 Community License. That license does not
apply in the EU, the UK or South Korea, and installing accepts it.

## 3. Before running: write the gate down

1. Put 20 CC0 or self-made images in `refs/`, and fill in `refs/SOURCES.csv`.
2. Edit `checklist.md` until you agree with every line, then commit it.

## 4. Run

```bash
cd bench/m0
../../core/bin/forja generate refs/* --priority batch --json > results/run-$(date +%F).json
python3 report.py results/run-*.json --csv results/stages.csv
```

The first image pays for model loading. Run the set twice; the second run's report holds the
warm baselines the spec asks for (time and peak VRAM per stage).

**First image only: check the facing.** Open the asset in the app. If the shape faces sideways
or backwards, or the texture is projected onto the wrong side, set the yaw in `forja.toml`
(`[presets.mobile-prop] overrides = { "shape.yaw_degrees" = 90 }`) and re-run. Use
`"paint.yaw_degrees"` if only the texture is off. Thanks to the stage cache, a paint-only
change re-runs only paint, LODs and export.

## 5. Judge and decide

- Open each asset in the Forja library (or `out/<name>/<name>.glb` in Unity), score it in
  `scores.csv` against the checklist, and count the passes.
- Record the outcome and the measured budgets in the spec's section 10. A budget that proved
  unrealistic is revised once, in writing.

If the normal map shows smeared or flat patches, the cage missed the high-poly there, and the
engine log (`<data>/logs/engine.log`) warns when that happens on more than 5% of texels. Raise
`--set bake.cage=0.04` (a fraction of the model's size). For darker or lighter AO, use `--set bake.ao_radius=0.1` (smaller means more local).

`--set match.enabled=true` adds the `texture.match` stage after paint (the "Match the
reference colors" switch in the app). It fits the camera the
reference was drawn from, corrects the painted colors per material toward the reference (the
back too), and blends the reference onto the surfaces facing that camera, which brings back
detail like eyes and embroidery. `--set match.reproject=0` keeps only the color correction.

Useful variations, each one a single flag:
- `--set paint.resolution=2048`: a sharper texture, at the cost of VRAM.
- `--set shape.steps=30`: a faster shape stage.
- `--set paint.model=stub-paint`: isolate shape quality from texture quality.

When VRAM runs short, the engine falls back to fit-first mode on its own. For Hunyuan that
means CPU offload: the run is slower but stays local.
