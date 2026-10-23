# Forja

Local-first desktop app that turns a reference image into a game-ready 3D asset (GLB) using open models on the user's own GPU, a GPU box on the LAN, or a cloud the user configures.

- Full technical spec: https://claude.ai/artifact/9Ka8P3gaKVfuLKsqSoS8a7 (read through the Claude Docs tools, never web-fetch it).
- Decisions that override or sharpen the spec: [docs/decisions.md](docs/decisions.md). Read it before changing scope, defaults, presets or licensing.
- Commands: `make help`. Each package's own commands live in its manifest (`core/go.mod`, `engine/pyproject.toml`, `apps/desktop/package.json`).

## Pillars

Every change is judged against four words. When two conflict, the order below decides.

1. **Fast.** Performance is a feature with a budget, not an intent.
   - The GPU is the bottleneck: everything else exists to keep it fed. CPU stages of one job overlap GPU stages of the next.
   - Never compute twice: every stage output is memoized by content hash (cache key = stage, adapter version, model revision, input hashes, normalized params, seed).
   - Never move bytes twice: content-addressed artifacts; file paths when co-located, uploads only when the remote lacks the hash.
   - Measure first: an optimization lands with a `forja bench` number showing the gain. Budgets are in the spec, section 10; a >10% regression fails CI.
   - Hot paths stay allocation-light: stream files, hash in parallel (BLAKE3), prepared SQLite statements, no polling loops.
2. **Fluid.** Every step the user sees moves smoothly.
   - Every state change animates: job queued → running → stage progress → preview → done. Nothing pops in or jumps.
   - Animate only `transform` and `opacity` (compositor-only), 150–300 ms, using the shared tokens in `apps/desktop/src/renderer/src/styles/motion.css`. Respect `prefers-reduced-motion`.
   - The renderer thread never blocks: decoding (GLB, meshopt, KTX2) runs in Web Workers, lists are virtualized, data arrives by push (WebSocket), never by polling.
   - The 3D viewer renders on demand (on interaction or data change), so it leaves the GPU to inference while still orbiting at 60 fps.
   - Show something early: skeletons over spinners, the untextured mesh as soon as the shape stage ends.
3. **Pluggable.** The core knows stage *types*, never specific models or vendors.
   - A model is a manifest plus an adapter implementing typed stage interfaces (`engine/src/forja_engine/sdk/`). Adding one needs zero core changes.
   - Every inference target (local engine, LAN engine, serverless, managed API) implements the one `Provider` interface in `core/internal/provider/`. Code depends on the interface, never on a concrete provider.
   - Settings flow through layered TOML config (built-in → system → user → project → env → flags → job). Hardcoded values are defaults in `core/internal/config/defaults.go`, nowhere else.
   - Dependencies point inward: `cmd` → `daemon`/`cli` → `jobs`/`planner` → interfaces. Adapters and providers are leaves.
4. **Portable.** Windows, macOS and Linux behave identically for UI and core.
   - Build paths with `filepath`/`pathlib`/`path`, never string concatenation. Short hashed object-store paths keep Windows path limits safe.
   - IPC: Unix socket on macOS/Linux, named pipe on Windows, behind `core/internal/ipc`.
   - Detect hardware at runtime (CUDA, MPS, ROCm, CPU) and mark each model runnable, runnable-with-offload or remote-only. Never assume a backend.
   - Core cross-compiles as one static binary: pure-Go dependencies only (no cgo; SQLite is `modernc.org/sqlite`).

## Architecture

Three processes; each owns exactly one thing.

| Process | Language | Owns | Never does |
|---|---|---|---|
| `apps/desktop` | Electron, TypeScript, React, three.js | View state, rendering | Business state, hashing, file I/O, inference |
| `core` (`forja`) | Go | Jobs, plan, routing, config, secrets, SQLite library, object store, **the stage cache index** | Inference |
| `engine` (`forja-engine`) | Python | Running stage batches on its devices: scheduling, VRAM, model workers, artifact store by hash | Durable job state, memoization decisions |

- The engine is an **executor**: it receives stage batches, runs them, stores outputs by hash, and forgets the batch. The core decides what is cached and asks `HEAD /v1/artifacts/{hash}` to find outputs a remote engine already holds.
- Model workers are separate processes, one per model family, each in its own uv virtual environment. Upstream code with a restrictive license is fetched at install time (pinned commit plus hash), never committed here.
- The UI reaches the core only through the Electron main process (custom `forja://` protocol and IPC bridge), which talks to the core over the local socket.

## Working in this repo

- This repo will be open source: every committed byte is public. Never commit credentials (API keys, tokens, passwords, cloud or HuggingFace secrets, `.env` files, private keys), private hostnames or IPs, or personal paths. Secrets are read at runtime from the core's secrets store or env vars; tests, fixtures, examples and logs use obvious fakes. Review `git diff --staged` before every commit.
- Each change keeps all three test suites green: `make test`. CI-equivalent checks: `make check`.
- Real models live in **model families** (`engine/families/<name>/`): `family.yaml` pins the Python version, lanes, upstream source (URL + sha256) and CUDA builds; `requirements/<lane>.txt` lists dependencies per torch lane; `src/forja_<name>/` is the adapter. `forja models install <id>` builds `<data>/envs/<family>` with uv and fetches weights pinned in the model's manifest (`engine/src/forja_engine/manifests/`). Pin every new upstream commit, weight revision and file hash; adapter tests run against fake upstream modules (`engine/tests/test_families.py`).
- When an adapter's output changes for the same inputs, bump its `version` (for a model, also `adapter_version` in its manifest). The cache key includes it, so the bump is what retires stale cached outputs.
- Stub models (`engine/src/forja_engine/adapters/stub/`) are deterministic and CPU-only; they stand in for real models in tests and on machines without a GPU. Keep them deterministic for a fixed seed.
- The engine API (`/v1`) is a contract shared by the core, remote engines and serverless handlers. Changing it means bumping the API version in `engine/src/forja_engine/api/version.py` and `core/internal/engineapi/version.go` together.
- Write comments only for the *why* a reader cannot see in the code.
- Commits follow Conventional Commits (the changelog is generated from them).
