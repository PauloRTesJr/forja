# The stage cache

Forja never computes a stage twice. Before a job runs, the core gives every stage a key that covers everything that decides its output; a stage whose key is in the cache is not sent to any provider. Changing a job's texture settings therefore reruns only the stages from texture on.

## The key

`core/internal/planner/planner.go`, `stageKey`. The key is the BLAKE3 hash (first 16 bytes, prefixed `stage:`) of this JSON:

| Field | What it is |
|---|---|
| `v` | The key recipe's version, `keyVersion`. Bumping it recomputes every stage for every user, so it changes only when the recipe does. |
| `use` | The stage type, e.g. `shape`. |
| `model` | The model the stage was given at submit, empty for built-in stages. |
| `params` | The stage's parameters after the preset and the job's overrides. Go's JSON encoder sorts map keys, so equal params give equal bytes. |
| `seed` | The job's seed (42 unless set). Fixed by default, so a rerun reuses earlier stages. |
| `runs` | What the routed provider runs for the stage: the adapter version, the weights revision and the family's upstream sha256 (decision 11). |
| `inputs` | For each input, the key of the stage that made it, or the job image's content hash. |
| `views` | Which view each input shows; left out when the stage reads one view, so older keys stay valid. |

Keys chain: a stage's key includes its inputs' keys, so the whole plan is keyed before anything runs and every cache hit is known up front. Change the shape model's version, and the shape key changes, then every key after it.

## Where `runs` comes from

The provider is the only one that knows what it runs, so the core asks it:

- **Built-in stages**: `GET /v1/info` returns `stages`, each built-in's adapter `version` (`engine/src/forja_engine/adapters/builtin.py`).
- **Models**: `GET /v1/models` returns each model's `adapter_version` (from its manifest), `weights.revision`, and `upstream`, the sha256 of its family's pinned source.
- **Hosted models** describe themselves the same way through their provider.

Jobs are routed when they are submitted and keyed when they start. Keying may start the local engine to ask it, even for a job that turns out to be fully cached. A provider that cannot answer is keyed by its last answer in this session; an answer remembered from an earlier session only describes what is installed and never keys the cache.

## When to bump a version

Bump when the output changes for the same inputs, params and seed: a new algorithm, a fixed bug that changes results, a new default for a parameter the adapter reads with `ctx.params.get(name, default)`.

- A built-in stage: its class's `version` in `adapters/builtin.py`, with a comment saying what changed (`# 2: floaters are dropped by face mask ...`).
- A model: the adapter class's `version` and its manifest's `adapter_version`, together. `engine/tests/test_families.py` fails when they differ, and so does `forja-sdk test <manifest>`.
- New weights or new upstream code: change the manifest's `weights.revision` or the family's `upstream` pin; the key follows by itself.

Do not bump `keyVersion` to retire one stage's outputs; it throws away everyone's whole cache.

## Storage and eviction

Outputs live in the core's object store, named by content hash, so two stages that produce the same bytes share a file. The index from key to outputs is the `stage_cache` table in the library database. A co-located engine writes into the same store; a remote one is asked `HEAD /v1/artifacts/{hash}` and uploads or downloads only what is missing.

`core/internal/cache` keeps the cache under `general.cache_max_gb` by removing the least recently used files and forgetting the keys that pointed at them (decision 20). It never runs while a job runs, never touches files used in the last hour, and never removes the library's own files.
