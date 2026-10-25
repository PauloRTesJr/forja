# Adding a provider

A provider is anything that can run a stage: the local engine, an engine on another computer, a model hub. The job runner sees only one Go interface, so a stage moves from the local GPU to another machine or a paid API by configuration, never by code.

There are two routes:

| To add | How | Core change |
|---|---|---|
| A self-hosted engine (a GPU box, a rented GPU, your own server) | Run `forja-engine serve`, or anything that speaks the engine's `/v1` API, and add it as kind `engine` | None |
| A vendor's API (a hosted model service) | Implement `Provider` in Go | A core pull request |

## A self-hosted engine

Anything that implements the engine API is already a provider: the core talks to it exactly as to the local engine. Start one by hand:

```sh
FORJA_ENGINE_TOKEN=<a long random token> forja engine serve --host 0.0.0.0 --port 7860 \
  --tls-cert cert.pem --tls-key key.pem
```

Binding beyond loopback requires a token and TLS. Add it from another computer with `forja providers add <address> --token-stdin`, or use pairing ([sharing](../sharing.md)) when the computer runs Forja. The engine's Docker image, and the RunPod and Modal providers that run it on rented GPUs, are in [cloud GPUs](../cloud.md).

## A vendor's API

### 1. Implement the interface

`core/internal/provider/provider.go`:

| Method | What it must do |
|---|---|
| `ID()`, `Kind()` | The provider's id from config, and its kind (`engine`, `serverless`, `api`, `comfyui`) |
| `Paid()` | True when it costs money. Paid providers are never an automatic fallback unless `routing.allow_paid_fallback` is on |
| `Health(ctx)` | Cheap reachability and credential check; shown in the app and `forja providers ls` |
| `Info(ctx)` | Versions and, under `Stages`, the version of each built-in stage it runs; they are reported, never part of a stage's key |
| `Models(ctx)` | The models it can run, as `engineapi.Model`: stage types, status, license, price, `adapter_version` and `weights.revision` |
| `SharesStore()` | True only when it reads the core's object store directly (the local engine) |
| `HasArtifact`, `Upload`, `Download` | Move artifacts by content hash; upload only what the remote lacks |
| `Run(ctx, req, events)` | Execute a stage graph, sending engine events (`stage.progress`, `artifact.ready`, ...) until it ends |
| `Cancel(ctx, runID)` | Stop a run |

A provider that manages models on its own machine also implements `provider.Installer` (checks, fixes, install, remove, task progress). A hosted API has nothing to install and does not.

Map the vendor's models onto stage types as `core/internal/provider/fal/` does: from each model's input and output schema, one image in and a GLB out does shape and texture, an image and a mesh in does texture (decision 16). Record what each run costs so spending limits can be enforced.

### 2. Register it

- Put the package at `core/internal/provider/<name>/`. It depends on `provider` and `engineapi`, never on the job runner.
- Build it from config in `core/internal/providers/providers.go` (`Build`), and describe how it is set up, field by field, in `Kinds()`, which the app and `forja providers kinds` show.
- Its key or token lives in the OS keychain under `providers.SecretName(id)`. Config holds the entry's name, never the value, and no API returns it.

### 3. Both clients, and tests

Adding a provider kind is a feature: the app's setup and the CLI's commands reach it through the same core endpoints ([feature parity](../../.claude/skills/feature-parity/SKILL.md)). Test it against a fake server, as `core/internal/provider/fal/fal_test.go` does; tests never call a real vendor or need a real key.

How the job runner picks among providers, falls back from one that fails, prices a paid one and enforces spending limits is in [routing](../routing.md). A paid provider's time is priced by its `price_usd_per_second`, else by its `price` entry, else by the first price table entry named `<vendor>-<kind>` or starting with it; built-in entries live in `core/internal/config/defaults.go`. A provider that bills for waking up implements `provider.Sleeper`, so the catalog describes it from its last answer instead of starting a worker.
