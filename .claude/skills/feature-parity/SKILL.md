---
name: feature-parity
description: Build, change or remove a Forja feature in both clients at once, the desktop app and the forja CLI, through one core endpoint. Use when a change touches what a user can do: a core API endpoint, an app screen or control, or a CLI command or flag.
---

# Feature parity

Every feature reaches users through two clients of one core: the desktop app and the `forja` CLI (decision 18 in `docs/decisions.md`). The core owns the feature's logic once; each client is a thin caller. A change lands in all three places together, in the same change.

The work is done when the feature runs from both clients: `TestEveryEndpointIsReachableFromTheCommandLine` (`core/internal/cli/routes_test.go`) passes, the core, the app and the CLI each have a test that drives the change, and `make test` is green.

## 1. Core endpoint

The feature's logic lives in `core/internal/api`, behind one endpoint per action.

- Add the endpoint to the table in `routes()` (`api.go`).
- Answer with an exported type, such as `api.ProviderRow` or `api.SettingsView`. The CLI decodes that same type, so a change to it breaks the CLI at compile time instead of in a user's terminal.
- Validate input here. Both clients rely on the core's checks; neither repeats them.
- When state changes, publish an event on `s.Hub` (`api.EvSettingsChanged`, `jobs.EvJobUpdated`, …). The app updates by push from it, and `forja events` shows it.

Done when an API test drives the endpoint (`newServer` and `call` in `core/internal/api/models_test.go`).

## 2. App

- Add a typed call to `apps/desktop/src/renderer/src/lib/api.ts`, its types mirroring the Go type's JSON.
- Build the screen or control under `screens/` or `components/`, following the Fluid pillar in `AGENTS.md`.
- For a new event, apply it in `lib/events.ts`: set or invalidate the query keys that show the changed state.

Done when the renderer reaches the endpoint only through `lib/api.ts`, and `pnpm lint`, `pnpm typecheck` and `pnpm test` pass in `apps/desktop`.

## 3. CLI

Commands live in `core/internal/cli`, one file per area (`jobs.go`, `models.go`, `providers.go`, …).

- Name the command and its flags in the app's words: Settings says "Share this computer's GPU", so the command is `forja share`.
- Wrap the command in `calls(cmd, routes...)`, naming every endpoint it reaches. A command that follows a job adds `watchRoutes`; one that follows an install task adds `followRoutes`.
- Print with `a.print(v, human)`, where `v` is the API's own type, so `--json` prints exactly what the core answered. Draw the human form with `a.table` and `a.printf`.
- Read keys and tokens with `a.readSecret`, from stdin; flags end up in shell history.
- Ask questions only when `a.tty` is true, and give scripts a flag for the same answer (`--yes`, `--accept-license`, `--sheet split`).
- Reach the core with `a.client()`, which starts a core in-process when none runs. When the effect must outlive the command (a pairing code, a job left running), use `a.daemonClient(...)` instead, which asks for a running core.

Done when a harness test in `core/internal/cli/commands_test.go` drives the command: `newHarness`, then `h.ok`, `h.okJSON` or `h.cli` for exit codes and stderr. The harness serves the real API with a fake engine.

## 4. When only the app can use an endpoint

Some endpoints exist for the app's page alone: it renders pixels, or relays bytes the page may only load from the core. List each in `appOnly` in `routes_test.go`, with the reason a terminal has no use for it. Every other endpoint gets a command.

## Changing or removing a feature

Find both callers before editing: search for the route pattern (`"GET /v1/assets"`) in `core/internal/cli`, and for its path (`/v1/assets`) in `apps/desktop/src/renderer/src`. Change the type, the app and the command together. Removing a feature removes its route, its command and its UI in the same change; the coverage test reports a command that still names a route the core no longer serves.

## Trying the real binary

The tests use a fake engine. To watch a feature run on the real one without touching an installed app, build `forja` (`make core`) and give it its own world through environment variables:

- `FORJA_SOCKET`: a socket path, or on Windows a pipe such as `\\.\pipe\forja-dev`. Set it from PowerShell or cmd: Git Bash rewrites `\\.\pipe\…` into a path that does not exist.
- `FORJA_DATA_DIR` and `FORJA_CONFIG_DIR`: empty folders.
- A `config.toml` in that config folder with `[models] stubs = true` and `[engine] command = ["uv", "run", "--quiet", "--project", "<repo>/engine", "forja-engine"]`, so the deterministic stub models run on the CPU.

Then run `forja daemon` in one terminal and the commands in another; `forja daemon stop` ends it.

## Decisions

When the feature changes scope, defaults, presets or licensing, record it in `docs/decisions.md` in the same change.
