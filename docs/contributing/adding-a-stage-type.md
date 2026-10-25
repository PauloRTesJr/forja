# Adding a stage type

A stage type is a step presets can name: `shape`, `mesh.decimate`, `bake`. Models and built-in stages implement stage types; presets choose among them. Adding a model never needs a new stage type. Adding a stage type touches the SDK, the engine, the core's presets and the app, in one change.

Decide first which kind it is:

- **A built-in stage**: deterministic CPU code in the engine, the same on every machine (cleanup, decimation, LODs). No model, no install.
- **A model stage**: run by whichever model declares it in `tasks` (background removal, shape, texture). Presets leave the model open; the core picks one at submit (decision 12).

## 1. The SDK

Add the value to `StageType` in `engine/sdk/src/forja_sdk/types.py`. Name it after what it produces, dotted when it refines another type (`mesh.uv`, `texture.match`). This is an additive change to a published contract: raise `forja-engine-sdk`'s minor version.

For the conformance suite (`engine/sdk/src/forja_sdk/conformance.py` and `samples.py`):

- add to `_EXPECT` what the next stage reads from this stage's first output (its kind, and alpha, triangles, UVs or a texture), and
- add a recipe to `samples.inputs_for` that builds inputs shaped like the pipeline's, so adapters can be tested without real data.

## 2. The engine

- **Built-in**: a class in `engine/src/forja_engine/adapters/builtin.py`, registered in `BUILTINS`, with `version = "1"`. `GET /v1/info` reports it under `stages`. Bump it, with a comment saying what changed (`# 2: floaters are dropped by face mask ...`), when the output changes for the same inputs, params and seed: a new algorithm, a fixed bug that changes results, a new default for a parameter read with `ctx.params.get(name, default)`. Keep it deterministic, and report progress and check cancellation if it takes more than a moment.
- **Model**: nothing in the engine; a model's manifest lists the type in `tasks`.

An engine now accepts a `use` value older engines reject: raise the engine API's minor version, in `engine/src/forja_engine/api/version.py` and `core/internal/engineapi/version.go` together.

## 3. The catalog

Add the value to the `tasks` enum in `catalog/schema/manifest.schema.json`. `engine/tests/test_catalog.py` fails until the schema lists every `StageType`.

## 4. The core

- Use it in presets: `core/internal/presets/builtin/*.yaml`. The planner keys any stage type the same way, from its type, model, params, seed and inputs, so a job runs the same work once ([decision 37](../decisions.md)).
- A stage type that prepares the reference image, run once per view of the subject like `background-removal` and `framing`, joins `prepares` in `core/internal/presets/presets.go`.
- Changing what a built-in preset does is a decision: add a row to `docs/decisions.md`.

## 5. The app

- A label in `stageLabels`, `apps/desktop/src/renderer/src/lib/format.ts`.
- Its phase (Prepare, Shape, Paint, Finish) in `phaseOfUse`, `apps/desktop/src/renderer/src/lib/jobs.ts`.

## 6. Docs and tests

- The stage table in [presets](../presets.md), and the list in [the SDK reference](adapter-sdk.md).
- An engine test that runs the stage through the API (`engine/tests/test_api.py` shows how), a core test for a preset that uses it, and `make check`.
