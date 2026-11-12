# The forja command line

`forja` is the core: it runs as the desktop app's background service, and as a command line that does everything the app does, through the same endpoints. Scripts, CI and remote shells get every feature.

## How it reaches the core

A command talks to the running core (the app's, or one started with `forja daemon`) over a local socket, or a named pipe on Windows. When none runs, most commands start a core inside themselves for as long as they run. Commands whose effect must outlive them need a running core: `forja share code`, `forja generate --no-wait` and `forja daemon stop`.

```sh
forja daemon          # run the core in the foreground
forja daemon status   # whether one is running, and where
forja daemon stop     # stop it and its engine
```

## Commands

| Command | What it does |
|---|---|
| `forja setup` | Install what the built-in presets need on this computer, then test them ([install](install.md)) |
| `forja generate <image-or-folder>...` | One job per image (a folder: the PNG, JPEG and WebP files directly inside it, by name), followed until it ends ([install](install.md#make-your-first-asset)); `--with` and `--skip` turn optional stages such as `rig`, `remesh` and `uv` on and off ([presets](presets.md#optional-stages)); `--estimate` shows each stage's expected time, cost and provider instead ([routing](routing.md#estimates-before-queueing)), `--dry-run` the plan |
| `forja jobs ls / show / watch / cancel / sheet` | Jobs: list, inspect, follow, cancel, answer a turnaround sheet |
| `forja assets ls / show / get / export / formats / blender / materials / rm` | The library ([library](library.md)); `ls` and `show` say how an asset is rigged ([rigging](rigging.md#see-the-rig)); `materials` sets how rough and how metallic each material is ([library](library.md#materials)) |
| `forja presets ls / show` | Presets and the plan each would run now, and which optional stages cannot be turned on now and why ([presets](presets.md)) |
| `forja models ls / install / rm / checks / fix / pin / unpin` | Models ([models](models.md)) |
| `forja installs ls / watch / cancel` | Installs, removals and fixes in progress |
| `forja share`, `share on / off / code` | Share this computer's GPU ([sharing](sharing.md)) |
| `forja providers ls / kinds / test / add / rm / token` | Other computers that run models for this one ([sharing](sharing.md)); `add runpod`, `runpod-pod`, `modal` and `comfyui` add a rented GPU or a ComfyUI ([cloud](cloud.md)) |
| `forja hubs ls / connect / search / add / rm` | Model hubs ([hubs](hubs.md)) |
| `forja prices ls / set / rm`, `forja spend` | The price table, and what paid providers cost today and this month ([routing](routing.md#the-price-table)) |
| `forja bench`, `bench ls / show / report / cancel / compare` | Benchmarks: measure a suite of images, compare two runs ([bench](bench.md)) |
| `forja config show / get / set / unset / edit / path` | Settings ([configuration](configuration.md)) |
| `forja secret set / rm` | Store a key in the OS keychain, read from stdin |
| `forja engine serve / unload` | Run an engine for other machines by hand; free GPU memory now |
| `forja info`, `forja diagnostics` | Status of the core, engine and hardware; the report for bug reports |
| `forja events` | The core's events as JSON lines, until interrupted |

`forja <command> --help` describes each command and its flags.

## Flags every command takes

- `--json` prints exactly what the core answered, for scripts: `forja --json generate chest.png` prints the finished jobs, with their stages and asset ids.
- `--socket <address>` talks to another core; the default is `$FORJA_SOCKET`, else this user's own.

## Keys and tokens

Keys never go on the command line, where shell history would keep them. Commands that take one read it from stdin:

```sh
forja hubs connect fal < fal-key.txt
forja secret set my-key < key.txt
forja providers add 192.0.2.10 --token-stdin < token.txt
forja providers add runpod <your-endpoint-id> < runpod-key.txt
```

A command that needs two, such as a RunPod endpoint with its network volume's key, reads one per line ([cloud](cloud.md#runpod-serverless)).

## Questions and scripts

A command asks only at a terminal, and every question has a flag that answers it: `--yes`, `--accept-license`, `--sheet split`. Without a terminal and without the flag, the command takes the safe answer or stops.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | An error, printed on stderr; for `forja generate --estimate`, a job that would be refused |
| 3 | A job failed; for `forja bench`, a sample |
| 4 | Cancelled (Ctrl+C cancels the jobs, or the benchmark, a command follows) |
| 5 | A job waits for an answer no one at a terminal could give; `forja jobs sheet` gives it |
| 6 | `forja bench compare` found a regression past the threshold |
