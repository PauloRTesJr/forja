# Contributing to Forja

Thanks for helping. This page covers setting up, the checks a change must pass, and how commits and pull requests are written. Read [AGENTS.md](AGENTS.md) first: it holds the four pillars (Fast, Fluid, Pluggable, Portable) every change is judged against, and which process owns what. [docs/decisions.md](docs/decisions.md) records decisions that override the spec; read it before changing scope, defaults, presets or licensing.

## Set up

You need Git, [Go](https://go.dev/dl/) (the `go` command fetches the version `core/go.mod` names), [uv](https://docs.astral.sh/uv/), Node.js 22 or newer with pnpm 10 (`corepack enable`), and `make`. On Windows, run `make` from Git Bash.

```sh
make setup   # uv sync in engine/, go mod download in core/, pnpm install in apps/desktop/
make help    # every target
```

| Target | What it does |
|---|---|
| `make core` | Builds `core/bin/forja` |
| `make core-all` | Cross-compiles the core for six OS and architecture pairs, without cgo |
| `make engine` | Runs an engine on port 7860 against `./.forja/store` |
| `make desktop` | Builds the core and starts the app in development mode |
| `make test` | Runs the three test suites |
| `make check` | Lints, type-checks and tests everything; what CI runs |

## The three test suites

Every change keeps all three green.

| Suite | Run it alone | What it covers |
|---|---|---|
| Engine (Python) | `cd engine && uv run pytest` | Stages, scheduler, API, installs, families against fake upstream modules, the SDK and catalog rules, the adapter template |
| Core (Go) | `cd core && go test ./...` | Config, planner, cache, jobs, the API, every CLI command against a fake engine |
| Desktop (TypeScript) | `cd apps/desktop && pnpm test` | The renderer's logic |

Tests never need a GPU. Real models are replaced by stub models (`engine/src/forja_engine/adapters/stub/`): deterministic CPU stand-ins. Keep them deterministic for a fixed seed.

`make check` adds `ruff`, `mypy`, `gofmt`, `go vet`, ESLint and `tsc`. A test that needs the network is opt-in; for example, the end-to-end check that a model can be added without core changes:

```sh
make core && cd engine && FORJA_GATE_E2E=1 uv run pytest -q tests/test_gate.py
```

## Try your change on the real binary

Give a development core its own world, so it never touches an installed app's data:

- `FORJA_SOCKET`: a socket path, or on Windows a pipe such as `\\.\pipe\forja-dev`. Set it from PowerShell or cmd; Git Bash rewrites `\\.\pipe\...` into a path that does not exist.
- `FORJA_DATA_DIR` and `FORJA_CONFIG_DIR`: empty folders.
- A `config.toml` in that config folder, so the stub models run on the CPU:

  ```toml
  [models]
  stubs = true

  [engine]
  command = ["uv", "run", "--quiet", "--project", "/path/to/forja/engine", "forja-engine"]
  ```

Then run `core/bin/forja daemon` in one terminal and commands in another. `forja daemon stop` ends it.

## Rules that catch people out

- **Both clients.** Every feature is in the desktop app and the `forja` CLI, through one core endpoint (decision 18). Add, change or remove all three in the same change. The steps are in [.claude/skills/feature-parity/SKILL.md](.claude/skills/feature-parity/SKILL.md); a test fails when an endpoint has no command.
- **Cache keys.** When an adapter's output changes for the same inputs, bump its `version`, and for a model also `adapter_version` in its manifest. See [the stage cache](docs/contributing/stage-cache.md).
- **The engine API.** `/v1` is shared by the core, remote engines and serverless handlers. Changing it means bumping the version in `engine/src/forja_engine/api/version.py` and `core/internal/engineapi/version.go` together.
- **Defaults.** Hardcoded settings live in `core/internal/config/defaults.go` and nowhere else.
- **Portability.** Build paths with `filepath`, `pathlib` or `path`, never by joining strings. The core stays pure Go (no cgo).
- **Comments** say why, when the code cannot.

Guides for the common extensions: [adding a model](docs/contributing/adding-a-model.md), [adding a stage type](docs/contributing/adding-a-stage-type.md), [adding a provider](docs/contributing/adding-a-provider.md).

## Never commit secrets

This repository is public: every committed byte is. Never commit API keys, tokens, passwords, cloud or Hugging Face credentials, `.env` files, private keys, private hostnames or IP addresses, or paths from your own machine. Forja reads secrets at runtime from the OS keychain or environment variables; tests, fixtures, examples and logs use obvious fakes (`fake-key`, `192.0.2.10`, `C:\Users\<you>`). Read `git diff --staged` before every commit.

## Commits

Commits follow [Conventional Commits](https://www.conventionalcommits.org/); the changelog is generated from them.

```text
feat(engine): read side views in the shape stage
fix(desktop): stop the stage track from flickering a scrollbar
docs: explain pairing in the sharing guide
```

Use `feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci` or `chore`, and a scope for the area: `engine`, `core`, `cli`, `desktop`, `sdk`, `catalog`, `library`, `docs`. Mark a breaking change with `!` (`feat(core)!: ...`) and a `BREAKING CHANGE:` footer.

### Sign off your commits (DCO)

Forja uses the [Developer Certificate of Origin](https://developercertificate.org/) instead of a contributor license agreement. Signing off certifies that you wrote the change, or otherwise have the right to submit it under the project's license. Add the sign-off with `-s`:

```sh
git commit -s -m "fix(core): ..."
```

It appends `Signed-off-by: Your Name <you@example.com>`, using your Git name and email. To sign off commits you already made on a branch: `git rebase --signoff main`.

## Pull requests

- One topic per pull request, small enough to review in one sitting.
- `make check` passes.
- User-visible changes update the docs in `docs/`; changes to scope, defaults, presets or licensing add a row to `docs/decisions.md`.
- The template asks for what changed, why, and how you tested it.

CI, releases, versioning, license scanning and the optional pre-commit hooks (`make hooks`) are in [CI, releases and versioning](docs/contributing/releases.md).

## Conduct

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).
