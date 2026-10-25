# Configuration

Settings are TOML, read in layers. A later layer overrides an earlier one, key by key:

1. Built-in defaults.
2. The system file: `%PROGRAMDATA%\forja\config.toml` on Windows, `/etc/forja/config.toml` elsewhere.
3. Your file, `config.toml` in the settings folder ([where it lives](install.md#where-forja-keeps-things)).
4. The project's `forja.toml`, found by walking up from the current folder (or `--project`).
5. Environment variables: `FORJA_<SECTION>__<KEY>`, with two underscores between levels, e.g. `FORJA_ENGINE__FIT_MODE=quality-first`.
6. Flags and a job's own overrides.

The Settings screen shows only settings something reads, writes them to your `config.toml` without losing its comments, and says which layer set a value it cannot change. There is no settings import or export: settings describe one machine. The one export is **Copy diagnostics**.

```sh
forja config get                      # the settings the app edits: value, default, and the layer that set it
forja config set engine.fit_mode=quality-first
forja config unset engine.fit_mode    # back to the layer below
forja config show                     # the merged configuration the core uses
forja config edit                     # open config.toml in your editor
forja config path                     # where the files are
```

`--project <folder>` makes `show` and `get` read as that project would, and `set` and `unset` write its `forja.toml`.

## Settings the app edits

| Key | Default | What it does |
|---|---|---|
| `engine.idle_unload_minutes` | 15 | Unload models after this long without work |
| `engine.vram_reserve_gb` | 1.0 | VRAM kept free for the desktop and other apps |
| `engine.fit_mode` | `fit-first` | `fit-first` offloads a model that does not fit; `quality-first` refuses |
| `routing.policy` | `prefer-local` | `prefer-local`, `cheapest`, `fastest` or `offline-only` ([routing](routing.md#routing-policies)) |
| `routing.allow_paid_fallback` | false | Let paid providers run a stage no free one can, or take over one whose provider failed ([routing](routing.md#paid-providers)) |
| `routing.budget_usd.per_job`, `per_day`, `per_month` | 0 | Spending limits for paid providers; 0 is none ([routing](routing.md#spending-limits)) |
| `project.export_dir` | `Assets/Generated` | Where a project's assets are copied, inside the project |
| `project.default_preset` | `mobile-prop` | The preset jobs use when none is given |
| `models.stubs` | false | Allow the deterministic stub models |
| `blender.path` | empty (found by itself) | Blender's program, for exports and Open in Blender |
| `general.cache_max_gb` | 50 | Stage cache limit; 0 is none |

Changing an engine setting this way restarts the engine when no job or install is running; otherwise the change applies when it next starts.

## Settings for hand editing

These live in `config.toml` (`forja config edit`); the CLI's own commands write some of them. The engine reads hand edits when it next starts: `forja daemon stop`, or quit the app.

```toml
[general]
data_dir = "D:/Forja"                 # library, cache, environments and weights

[engine]
command = ["forja-engine"]            # how to start the local engine
manifest_dirs = ["D:/forja-models/mymodel"]  # model packages and manifest folders
share = false                          # forja share on / off
share_port = 7861

[models.stages]                        # forja models pin / unpin
shape = "triposg"

[routing]
priority = ["local"]                   # providers in the order stages try them

[routing.stages]                       # forja models pin --provider: where a stage type runs
shape = "studio-pc"

[presets.mobile-prop.overrides]        # change a preset's values for every job
"lowpoly.target_tris" = 3000

[bench]                                # what forja bench compare counts as a regression
threshold_pct = 10                     # growth in percent that fails
min_seconds = 0.05                     # smaller time changes are noise
min_vram_mb = 64                       # smaller VRAM changes are noise

[prices.runpod-serverless-4090]        # forja prices set; the price table
usd = 0.00031
per = "second"                         # second, hour or generation
label = "RunPod serverless, RTX 4090 24 GB (flex worker)"
```

[Routing](routing.md) explains `[routing.stages]` and the price table, [benchmarks](bench.md#compare-two-runs) the `[bench]` section.

Providers you add are written as `[[providers]]` entries that hold the address and a keychain entry's name, never the key itself. Keys live only in the OS keychain (`forja secret set`, or the app's key fields). Besides `id`, `kind`, `label`, `url`, `secret` and `fingerprint`, an entry can hold:

| Key | For | What it is |
|---|---|---|
| `vendor` | RunPod, Modal, a hub | `runpod`, `modal` or `fal` |
| `paid` | any | Whether it costs money |
| `price_usd_per_second` | rented GPUs, a paid ComfyUI | What its time costs |
| `price` | rented GPUs | A price table entry for its time, when `price_usd_per_second` is not set |
| `endpoint_id` | RunPod serverless | The endpoint's ID |
| `volume`, `region` | RunPod serverless | The endpoint's network volume and its datacenter, such as `EU-RO-1` |
| `workflows` | ComfyUI | Workflow files by stage type, relative to the settings folder |
| `models` | a hub | The hub models you added |

[Cloud GPUs](cloud.md#manage-providers) has examples.
