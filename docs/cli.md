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
| `forja generate <image>...` | One job per image, followed until it ends ([install](install.md#make-your-first-asset)) |
| `forja jobs ls / show / watch / cancel / sheet` | Jobs: list, inspect, follow, cancel, answer a turnaround sheet |
| `forja assets ls / show / get / export / formats / blender / rm` | The library ([library](library.md)) |
| `forja presets ls / show` | Presets and the plan each would run now ([presets](presets.md)) |
| `forja models ls / install / rm / checks / fix / pin / unpin` | Models ([models](models.md)) |
| `forja installs ls / watch / cancel` | Installs, removals and fixes in progress |
| `forja share`, `share on / off / code` | Share this computer's GPU ([sharing](sharing.md)) |
| `forja providers ls / kinds / test / add / rm / token` | Other computers that run models for this one |
| `forja hubs ls / connect / search / add / rm`, `forja spend` | Model hubs and what they cost ([hubs](hubs.md)) |
| `forja cache`, `cache clear` | The stage cache ([library](library.md#the-cache)) |
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
```

## Questions and scripts

A command asks only at a terminal, and every question has a flag that answers it: `--yes`, `--accept-license`, `--sheet split`. Without a terminal and without the flag, the command takes the safe answer or stops.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | An error, printed on stderr |
| 3 | A job failed |
| 4 | Cancelled (Ctrl+C cancels the jobs a command follows) |
| 5 | A job waits for an answer no one at a terminal could give; `forja jobs sheet` gives it |

Benchmarks (`forja bench`) are documented after integration ([bench](bench.md)).
