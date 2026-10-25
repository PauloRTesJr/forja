# Benchmarks

`forja bench` measures the real pipeline. It runs a suite of reference images through presets as ordinary jobs and records what every stage of every image cost: time, model load, memory, GPU use and the mesh it made. Use it to check a change against the speed budgets, and to compare one run with another.

## What a run does

- Each image goes through each preset the suite names. Every stage runs, as in any job: a job reuses only outputs it made itself in that run. A benchmark job adds no asset to the library.
- Jobs are queued at batch priority, so jobs you start yourself, from the app or the CLI, go first. One benchmark runs at a time; a second one is refused until the first ends.
- Every image uses the suite's seed, or 42.
- The run measures the device of the provider that runs the first model stage: this computer, or the one routing picks ([routing](routing.md)). The result names it by its GPU, such as `nvidia-geforce-rtx-4070-ti`, or by its CPU when it has no GPU.

| Option | Flag | What it changes |
|---|---|---|
| Warm (the default) | | Before measuring, one image per preset runs unmeasured to load the models |
| Cold | `--cold` | This computer's engine unloads its models before every image, so each sample pays its model loads, as after a pause |
| Batch | `--batch` | A preset's images are queued together, as a folder dropped on Generate, to measure throughput and GPU utilization. Without it, one image runs at a time, so each time is that image's own |
| Repeat | `--repeat <n>` | Every image runs `n` times |

With `--batch` and `--cold` together, the models are unloaded once, before each preset's batch.

## Run a benchmark

**From a terminal:**

```sh
forja bench                                   # bench/suites/default.yaml of the Forja checkout around this folder
forja bench --suite bench/suites/ci.yaml      # the stub suite, which runs on any computer
forja bench --preset mobile-prop --repeat 3 --label "decimate rewrite"
forja bench --cold --out results/             # also write results/bench.json and results/bench.html
```

| Flag | What it does |
|---|---|
| `--suite <file>` | The suite to run (default: `bench/suites/default.yaml`, found by walking up from the current folder) |
| `-p`, `--preset <id>` | Run this preset instead of the suite's; repeat the flag for several |
| `--cold`, `--batch`, `--repeat <n>` | See the table above |
| `--label <text>` | A name for the run, such as the change it measures |
| `-o`, `--out <folder>` | Also write `bench.json` and `bench.html` into this folder |

It prints each sample as it ends, with its image-to-GLB time, stage time, model loads and the core's own time, then the summary. Ctrl+C cancels the run; the command waits until the run has stopped, so its result is still saved.

| Exit code | Meaning |
|---|---|
| 0 | Every sample succeeded |
| 1 | The run could not start: another benchmark is running, a preset is unknown or has no ready model, or the suite has no image here |
| 3 | A sample failed |
| 4 | Cancelled |

A preset over its budget does not change the exit code; [compare](#compare-two-runs) the run against a baseline for that.

**In the app:** the Models screen has a **Benchmark** section. **Choose suite…** picks a suite file (the app offers the last suite it ran), **Cold models** makes it a cold run, and **Run benchmark** starts it. While it runs, the section shows its progress, the current sample's stages as they run, and each sample as it ends; **Cancel** stops it. The app runs every preset of the suite, one image at a time; `--batch`, `--repeat`, `--preset` and `--label` are CLI flags.

## Suites

A suite is a YAML file. The repository has two, in `bench/suites/`:

- `default.yaml`: the M0 reference images (`bench/m0/refs`) through Draft and Mobile prop, with the real models pinned in `bench/m0/forja.toml`, and the budgets from the spec.
- `ci.yaml`: one image from the engine's tests through Draft and Mobile prop with the stub models, pinned in `bench/suites/ci/forja.toml`. It runs anywhere and proves the harness end to end, but stub times say nothing about real models, so it sets no per-preset budgets.

```yaml
schema: 1
name: default
version: 1
description: The reference images of the M0 spike through Draft and Mobile prop, real models.
presets: [draft, mobile-prop]
project: ../m0
budgets:
  draft: 10
  mobile-prop: 90
core_overhead_ms: 200
images:
  - dir: ../m0/refs
```

| Field | Required | What it is |
|---|---|---|
| `schema` | yes | The suite format: `1` |
| `name` | yes | The suite's name, in results and comparisons |
| `version` | | Bump it whenever the images or presets change: a comparison warns when two runs used different versions |
| `description` | | Free text |
| `presets` | yes | The preset ids each image goes through |
| `project` | | A folder, relative to the suite file, whose `forja.toml` pins the models the run uses ([decision 12](decisions.md)), so results stay comparable when other models get installed. Its `forja/presets/` folder can hold presets too |
| `seed` | | The seed of every job (default 42) |
| `budgets` | | Seconds from image to GLB, per preset |
| `core_overhead_ms` | | The budget for the core's own time per job, in milliseconds |
| `images` | yes | The images, below |

A field the format does not know is an error, so a typo never passes silently.

### Where the images come from

Each entry of `images` names one image with `path:` or a folder with `dir:`, relative to the suite file. A folder contributes every PNG, JPEG and WebP file directly inside it. `name:` replaces the name an image gets from its file name, and `category:` (such as `prop` or `character`) labels it in the results; two images with the same name become `chest` and `chest-2`.

Forja never downloads suite images. An image or folder that is not there is skipped, with the reason, at the start of the run and in its report; a suite with no image to run at all is refused.

The default suite reads `bench/m0/refs`, which git ignores. Put the reference images there yourself, each CC0 or made by you, and record each file's source and license in `bench/m0/refs/SOURCES.csv`. The folder's README says what the 20 images must cover. Once they are assembled, list them in the suite one by one, with a category each, so the suite stays fixed.

### Budgets

`default.yaml` sets the spec's budgets, for models warm on the reference machine: 10 seconds for Draft, 90 for Mobile prop, and 200 ms of core time per job. The results count the samples over their preset's budget and mark a median that passes it.

## What is measured

The engine measures each stage of a benchmark job:

| Measure | What it is |
|---|---|
| Time | The stage's own time, model load excluded |
| Model load | The load the stage waited for; 0 when the model was warm, none for built-in stages |
| Peak VRAM | The most GPU memory the stage held |
| Peak RAM | The peak resident memory of the process that ran it, sampled 10 times a second |
| GPU utilization | The share of time the GPU ran kernels while the stage ran, from NVIDIA's NVML at 10 Hz; none on other devices |
| Device | Where it ran: `cuda:0`, `mps`, `cpu`, or `cpu-pool` for built-in stages |
| Mesh output | Triangles, vertices, whether it is watertight, and the share of the UV square its triangles cover |

Each sample also records:

- **Image to GLB**: from submitting the job to its GLB, less the time measuring took.
- **Stages** and **loads**: the stages' own time and their model loads, added up.
- **Core overhead**: the core's own time, everything but inference and the wait for a job slot: planning, moving artifacts, reading results and saving state.
- **Asset**: the final GLB's triangles, vertices, materials and LODs.

A quality score (CLIP similarity of rendered views, spec section 10) is not computed yet; the field is always `null`.

The summary adds these up: per preset, the median, 90th percentile and maximum image-to-GLB time of the samples that succeeded, the failures and the samples over budget; per stage, the same spread of times and loads, the highest peaks, the GPU utilization weighted by time, the median triangle count, the share of watertight meshes and the median UV coverage; the core's overhead per job against its budget; and how busy each device kept over the run.

## Results and the report

Each run is kept in the core's data folder ([where it lives](install.md#where-forja-keeps-things)) as `bench/<id>.json` and a report beside it, `bench/<id>.html`. A result has a versioned schema (`"schema": 1`), and records the core's version and commit (and whether it was built from a checkout with uncommitted changes), the engine's version and API version, the device (GPU, VRAM, driver, CPU, RAM, OS) and the options.

```sh
forja bench ls                         # the running benchmark, then every result, newest first
forja bench show <id>                  # its budgets and stages
forja bench report <id> -o run.html    # its HTML report; without -o, on stdout
forja bench cancel <id>                # stop the running benchmark
```

The report is one self-contained HTML file, thumbnails of the inputs included, so it reads offline and travels as one attachment. It has the run's facts, each preset against its budget, the core's overhead, the devices, every stage and every sample, and the images it skipped.

**In the app:** each run is a row of the Benchmark section, with its presets' medians against their budgets. Open one for its stages, and **Open report** for the HTML report.

## Compare two runs

```sh
forja bench compare <baseline> <current>
forja bench compare baseline/bench.json results/bench.json --threshold 5
```

Each side is a result file or the id of a result the core keeps. The comparison reads, per preset, the failures and the median image-to-GLB time; per stage, the median time and the peak VRAM; and the median core overhead per job.

A value is a regression when it grows by more than the threshold, in percent, **and** by at least its noise floor: a 20 ms stage that takes 30 ms once is noise, not a 50% regression. A preset with more failed samples than the baseline is always a regression.

| Setting | Default | Flag | What it is |
|---|---|---|---|
| `bench.threshold_pct` | 10 | `--threshold` | Growth in percent that fails |
| `bench.min_seconds` | 0.05 | `--min-seconds` | Smaller time changes are noise (stage, image and core times) |
| `bench.min_vram_mb` | 64 | `--min-vram-mb` | Smaller VRAM changes are noise |

A flag overrides the setting; a flag left at 0 takes the setting. The settings are read from your `config.toml`, by hand (`forja config edit`):

```toml
[bench]
threshold_pct = 5
min_seconds = 0.1
```

The comparison still runs, with a warning, when the runs used different devices, different suites or suite versions, or different `--cold` or `--batch`, and when the baseline lacks a preset or a stage.

| Exit code | Meaning |
|---|---|
| 0 | No regression past the threshold |
| 1 | The comparison could not run: a side is neither a result file nor a kept id, or a run has not finished |
| 6 | At least one regression |

**In the app:** an open run has **Compare with**, which starts on the latest earlier run of the same suite version on the same device; any other run of the same suite can be picked. Regressions come first.

## The CI gate

- **Pull requests:** CI's bench smoke job (`.github/workflows/ci.yml`) runs `bench/suites/ci.yaml` with the stub models on a GitHub-hosted runner. It must run to the end; its times gate nothing, because hosted runners are too noisy.
- **Release candidates:** `.github/workflows/bench-gate.yml` runs on `v*-rc*` tags, and by hand with an optional baseline tag, on the maintainer's self-hosted GPU runner. It installs the models, runs `bench/suites/default.yaml`, downloads `bench-results.tar.gz` from the latest stable release (or the tag given), and runs `forja bench compare` with `--threshold 10`: a candidate more than 10% slower, or using more than 10% more VRAM, fails. When there is no stable release, or the latest one carries no results, there is nothing to compare, and the gate only records its results.
- **Stable releases:** a stable tag needs the gate green on its commit, and the release attaches the gate's results, which makes them the next baseline ([releases](contributing/releases.md)).
