# Architecture

Forja is three processes. Each owns exactly one thing, and the boundaries are where most review comments come from.

| Process | Language | Owns | Never does |
|---|---|---|---|
| `apps/desktop` | Electron, TypeScript, React, three.js | View state, rendering | Business state, hashing, file I/O, inference |
| `core` (`forja`) | Go | Jobs, plan, routing, config, secrets, the SQLite library, the object store, the stage cache index | Inference |
| `engine` (`forja-engine`) | Python | Running stage batches on its devices: scheduling, VRAM, model workers, artifacts by hash | Durable job state, memoization decisions |

```text
desktop renderer ──IPC bridge──> Electron main ──socket / named pipe──┐
forja CLI ───────────────────────────────────────socket / named pipe──┤
                                                                      v
                                      core: library, plan, cache index, routing
                                        │ Provider interface
              ┌─────────────────────────┼─────────────────────┬───────────────┐
              v                         v                     v               v
        local engine            engine on the LAN       hosted model hub   rented GPUs, ComfyUI
      (same object store)      (TLS, pinned cert)          (fal.ai)        (RunPod, Modal)
              │
       model workers: one process per family, each in its own uv environment
```

## The core

- Runs as a daemon for the app (one per user, on a socket or a named pipe that only that user can open) or in-process for a CLI command. The app reaches it only through Electron's main process (the `forja://` protocol and an IPC bridge); the CLI is a second client of the same `/v1` endpoints (decision 18).
- Keeps the library in SQLite (`modernc.org/sqlite`, pure Go: the core cross-compiles as one static binary without cgo) and every file in a content-addressed object store, named by BLAKE3 hash.
- Plans a job: each stage gets a model and a provider, then a cache key ([the stage cache](stage-cache.md)). Stages already in the cache are not sent anywhere.
- Reads layered TOML config: built-in, system, user, project, environment, flags, job. Every hardcoded default is in `core/internal/config/defaults.go`.
- Keeps secrets in the OS keychain, and redacts every value it has read from its log.

Dependencies point inward: `cmd` → `daemon` and `cli` → `jobs` and `planner` → interfaces. Providers and adapters are leaves.

## The engine

An executor. It receives a stage graph (`POST /v1/runs`), runs it, stores the outputs by hash, streams events, and forgets the run. It never decides what is cached: the core asks `HEAD /v1/artifacts/{hash}` to find what a remote engine already holds, and sends only the stages it lacks.

- **Supervisor**: FastAPI on uvicorn. The API, the scheduler, the VRAM manager and the artifact store.
- **Scheduler**: one serial lane per device. Among ready stages it prefers interactive jobs, then the model already loaded, up to a fairness cap of 8 in a row. Built-in CPU stages run in a process pool, so one job's UV unwrap overlaps the next job's shape stage.
- **VRAM manager**: a budget per device (its memory minus a reserve), starting from manifest estimates and replaced by measured peaks. Loading a model that does not fit evicts the least recently used one, or loads it with offload in `fit-first` mode.
- **Model workers**: one process per model family, started with that family's interpreter (`<data>/envs/<family>`), speaking JSON lines over stdin and stdout. Only file paths cross the pipe, never meshes or images. A crashed worker is restarted and its stage retried once.
- **Built-in stages** (`adapters/builtin.py`): framing, cleanup, decimation, UV unwrap, texture match, bake, LODs, export, view detection and splitting. CPU only, no model.

The engine API is a contract shared by the core, remote engines and serverless handlers: `engine/src/forja_engine/api/version.py` and `core/internal/engineapi/version.go` carry its version, and the core refuses an engine with another major version.

## Models

A model is data plus code: a manifest (`engine/src/forja_engine/manifests/`, or a model package's `manifests/`) and an adapter that implements the SDK's `Adapter` (`engine/sdk/`, published as `forja-engine-sdk`). Adapters of one family share an environment, defined by `family.yaml`. Upstream code with a restrictive license is fetched at install time, pinned by commit and sha256, never committed here. See [adding a model](adding-a-model.md).

## Providers

Everything that can run a stage implements one Go interface, `Provider` in `core/internal/provider/`: the local engine, an engine on another computer or a rented pod, a RunPod serverless endpoint, a Modal gateway, a ComfyUI, a model hub. Code depends on the interface, never on a concrete provider. See [adding a provider](adding-a-provider.md), [cloud GPUs](../cloud.md) and [routing](../routing.md).

## Where things live

```text
apps/desktop/     Electron app; the renderer reaches the core only through src/renderer/src/lib/api.ts
core/cmd/forja/   the binary
core/internal/    api (endpoints), cli (commands), jobs, planner, presets, catalog, provider(s),
                  library, store, config, secrets, ipc, localengine, export
engine/src/forja_engine/   supervisor, scheduler, workers, registry, install, built-in stages, manifests
engine/sdk/       forja-engine-sdk: stage types, Adapter, artifact validators, conformance suite
engine/families/  one folder per model family: family.yaml, requirements/, src/forja_<name>/
catalog/          manifest and family schemas, the catalog index
templates/adapter/  a model package to start an adapter from
bench/            benchmark suites, the M0 spike's reference images and results
docker/           the engine's images, the RunPod worker and the Modal app
scripts/          version, license scan and end-to-end scripts
docs/             this documentation; decisions.md overrides the spec
```
