# Routing, estimates and spending

When a job is submitted, the core picks a provider and a model for every stage: this computer, another computer, a rented GPU, a ComfyUI or a model hub. This page says how it picks, what it expects a job to take and cost before it is queued, and how spending limits keep paid providers in check.

## Where a stage can run

Providers are tried in the order of `routing.priority`: `["local"]` (this computer) at first, with each provider you add appended at the end. Reorder them by hand in `config.toml`:

```toml
[routing]
priority = ["local", "studio-pc", "runpod-abc123xyz789"]
```

A provider can take a stage when:

- the [routing policy](#routing-policies) allows it;
- it has what the stage needs: a ready model for a model stage, the built-in for a built-in stage (framing, cleanup, bake...). A built-in that runs through Blender needs Blender on that provider's machine;
- it is free, or [paid providers may take part](#paid-providers);
- the stage has not already failed on it in this job ([fallback](#fallback)).

Which model a stage gets is in [presets](presets.md#which-model-a-stage-uses). Where it runs follows one of three rules.

**Named provider.** A stage routed to a provider runs only there, and never falls back away from it. It is named by:

- pinning a model with its provider: picking a model under a provider's name on Generate or the Models screen, or `forja models pin shape triposg --provider studio-pc` (without `--provider`, the command pins the one provider that has the model, and refuses when several have it). Both write `[models.stages]` and `[routing.stages]` in `config.toml`;
- `--set <stage>.provider=<id>` for one job, or a preset stage's `provider:`.

**Named model (capability routing).** A model named without a provider, by `[models.stages]` alone, a preset stage's `model:` or `--set shape.model=triposg`, runs on the first provider, in the policy's order, where that model is ready: installed, with a device it can use there.

**Open stage.** A stage with no model named gets the best ready model on the first provider, in the policy's order, that has one.

## Routing policies

**In the app:** Settings, **Where models run**, "Which computer a stage uses". **In config:** `routing.policy`.

| Policy | In the app | Order of the providers that can run a model stage |
|---|---|---|
| `prefer-local` (default) | This computer first | `routing.priority` as written: this computer, then the others for models it cannot run |
| `cheapest` | Cheapest | By what the stage is expected to cost there: free providers first (they cost nothing), then paid ones by price. A paid provider whose cost cannot be worked out goes last |
| `fastest` | Fastest | By the median of the last 20 times this stage type and model took on that provider. Providers where it never ran go after those where it did |
| `offline-only` | Never leave this network | `routing.priority` as written, with only this computer and the engines you added as `engine` providers: other computers, and RunPod pods, which run an engine too. Never a model hub, a serverless endpoint or a ComfyUI |

Ties, and providers without a number, keep the `routing.priority` order. Policies sort only model stages: built-in stages go to the first provider in priority order that has them. Under `offline-only`, a stage routed by name to a provider the policy forbids refuses the job.

```sh
forja config set routing.policy=fastest
```

## Paid providers

Model hubs, RunPod, Modal and a ComfyUI given a price are paid. A stage uses one only when:

- you routed the stage to it by name, or pinned a model that no free provider offers, such as a hosted model ([model hubs](hubs.md#use-a-hosted-model)); or
- **Let paid providers step in** is on (Settings, shown once you have a paid provider; `routing.allow_paid_fallback`).

With it on, paid providers take part in routing and in fallback like the free ones, in the policy's order. Its price counts toward your [spending limits](#spending-limits).

## Fallback

When a stage fails because its provider could not be reached, or because it ran out of memory there (PyTorch's or CUDA's out-of-memory errors, or an engine in `quality-first` mode refusing a model that does not fit), the job plans the failed stages again without that provider, by the same rules:

- they go to the next place that can run them, in the policy's order;
- never to a paid provider unless `routing.allow_paid_fallback` is on;
- never away from a provider a stage was routed to by name.

When the provider could not be reached, the stages it had still to run move with them. What already succeeded stays and is reused. A stage that fails again moves again, so a stage can fall back along a chain of providers, until no place is left and the job fails with the last error. Other failures, such as a model error on a bad image, do not fall back.

Every move is kept with the job:

- the Queue says "Shape moved to Studio PC: This computer ran out of memory.", and each stage's line names where it ran and why: "Studio PC, after This computer ran out of memory";
- `forja generate` prints each move as it happens, and `forja jobs show <job>` lists them: `shape moved from local to studio-pc: it ran out of memory`;
- `forja events` carries a `job.fallback` event for scripts.

## Where it runs

Each stage says where it runs, before and after:

- **Generate**: under each step's model, "Runs on Studio PC" when it is not this computer.
- **Queue**: each stage's line names its model and, when it is not this computer, where it ran.
- **Models screen**: the "Runs on" column says where each model runs, or would once installed: "This computer", with its device (Apple GPU, CPU, `cuda:0`), "in VRAM" when it is loaded and "with offload, slower" when it does not fit; another provider's name; "Another computer" for a model too big or unsupported here; a hosted model's provider, "In the cloud".
- **CLI**: the `RUNS ON` column of `forja generate --estimate`, and the plans of `forja generate --dry-run` and `forja presets show <preset>`.

## Estimates before queueing

**In the app:** Generate shows, under the steps, how long an image should take ("About 1 min 12 s per image") and, when a stage is paid, what it costs and where ("$0.40 per image on fal.ai"). Notes say which stages the time leaves out, which paid stages have no price, and the spending so far against a daily or monthly limit. When the job would be refused, it says why.

**From a terminal:**

```sh
forja generate chest.png --estimate
forja generate props/*.png --preset environment-piece --estimate
```

It lists each stage with its model, where it runs, its time and its cost, then the totals per image and for all the images given, and queues nothing. It exits with 1, and says why, when the job would be refused: a stage with no ready model, a view a model needs, or a spending limit.

- **Time** is the median of the last 20 runs of that stage type and model on that provider, else on any provider ("measured elsewhere"). A stage that never ran is left out of the total, and named.
- **Cost** is the stage's price per generation, plus its expected time at its rate, from [the price table](#the-price-table) and the prices hosted models report. Free providers cost nothing.

## The price table

The price table says what paid providers charge, in US dollars: a second or an hour of a rented GPU, or one generation of a hosted model. Estimates, the `cheapest` policy and the spending limits all read it. Vendors change their prices, so the table is yours to correct.

| Entry | Built-in price | For |
|---|---|---|
| `runpod-serverless-4090` | $0.00031 per second | RunPod serverless, RTX 4090 24 GB (flex worker) |
| `runpod-pod-4090` | $0.34 per hour | RunPod pod, RTX 4090 on demand |

A provider's time is priced by, first match wins:

1. its own price, `price_usd_per_second` (the GPU price you gave when you added it; a pod's price per hour is stored per second);
2. the entry its `price` key names, in its `[[providers]]` entry;
3. the first entry named after its vendor and kind, `<vendor>-<kind>` or starting with it: `runpod-serverless-4090` prices a RunPod serverless endpoint. A RunPod pod is kind `engine`, so `runpod-pod-4090` does not apply to one by itself: give the pod its price, or set `price = "runpod-pod-4090"`.

A provider without a price has no known cost. Hosted models report their own price; an entry named after a model's id replaces it. A price by time on a model replaces its provider's rate instead of adding to it.

**In the app:** Settings, **Where models run**, **Prices** (shown once you have a paid provider). Each entry says what uses it and where its price comes from; change a price in place, and **Reset** a price you set back to the built-in one or the vendor's.

**From a terminal:**

```sh
forja prices ls                                              # every entry, what it prices, and who set it
forja prices set runpod-serverless-4090 0.00034 --per second
forja prices set fal-ai/trellis-2 0.30                       # per generation, the default
forja prices set modal-serverless-l40s 0.0006 --per second --label "Modal L40S"   # prices Modal gateways
forja prices rm runpod-serverless-4090                       # back to the built-in price
```

`--per` is `second`, `hour` or `generation`; prices are 0 or more. Entries are written to your `config.toml`:

```toml
[prices.runpod-serverless-4090]
usd = 0.00034
per = "second"

[prices."fal-ai/trellis-2"]
usd = 0.3
per = "generation"
```

`forja prices rm` removes only an entry from your `config.toml`; one set in the system file, a project or the environment is changed there.

## Spending limits

Limits are in US dollars, for every paid provider; 0 means no limit.

| Limit | Setting | A job is refused, or stopped, when |
|---|---|---|
| Per image | `routing.budget_usd.per_job` | its paid stages would cost more |
| Per day | `routing.budget_usd.per_day` | it would take today's spending past the limit |
| Per month | `routing.budget_usd.per_month` | it would take this month's spending past the limit |

Days and months follow this computer's clock. A limit is a hard stop:

1. **At submit**, a job's expected cost, added to what the queued and running jobs are expected to spend and what was already spent, must fit every limit.
2. **Before each paid batch** starts, it is checked again.
3. **While a batch billed by time runs**, it is metered every second against the limits, read afresh, so a lowered limit applies at once; once it would pass one, it stops: "stopped by the daily spending limit of $5.00: spending would reach $5.01".

While any limit is set, a paid stage with no known price is refused, because its cost cannot be checked: give it a price in the table, or set the limits to 0.

What a paid batch cost is recorded whenever the provider billed, failed stages included: a price per generation once the provider ran the stage, each stage's seconds at its rate, and the batch's time outside its stages at the provider's rate.

**In the app:** Settings, **Where models run**, the spending limits (shown once you have a paid provider), with today's and this month's spending.

**From a terminal:**

```sh
forja config set routing.budget_usd.per_day=5
forja spend          # today, this month, and the limits
```
