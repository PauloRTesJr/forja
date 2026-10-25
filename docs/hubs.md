# Model hubs (fal.ai)

A model hub runs hosted models for you, for a price. Connecting one gives Forja access to models too large for your GPU, such as Hunyuan3D 3.1 or TRELLIS.2, with one API key. fal.ai is the one hub so far. Local models stay the default: a hosted model runs only when you choose it.

## Connect

Create an API key in your fal.ai account, then:

**In the app:** Settings, **Connect fal.ai**, and paste the key.

**From a terminal**, the key comes on stdin so it stays out of your shell history:

```sh
forja hubs connect fal < fal-key.txt
forja hubs ls
```

Forja tests the key, keeps it in the OS keychain (never in a file), and adds its recommended models: Hunyuan3D 3.1 Pro, TRELLIS.2, Tripo 3.1, Meshy 7.1, Rodin 2.5 and TRELLIS.2 retexture. Each was checked to map onto Forja's stages.

## Find and add other models

Any image-to-3D model in fal's catalog can join when its inputs and outputs map onto a stage:

- one image in, a GLB out: shape and texture in one step;
- an image and a mesh in: texture;
- a field per view (`front_image_url`, `back_image_url`, ...) or a list of images: a model that reads side views. One that requires a view runs only on jobs that have it.

```sh
forja hubs search fal trellis        # catalog models Forja can use
forja hubs add fal fal-ai/trellis-2  # checks the model's schema first
forja hubs rm fal fal-ai/trellis-2   # a stage pinned to it goes back to automatic
```

## Use a hosted model

Hosted models are paid, so a preset never picks one by itself. Pin a stage to one:

```sh
forja models pin shape fal-ai/trellis-2 --provider fal
forja models unpin shape
```

or, in Settings, turn on **Let paid providers step in** (`routing.allow_paid_fallback`) so a stage no free provider can run may use one.

The price shows before a job is queued: on Generate, and with `forja generate --dry-run` or `forja presets show <preset>`. When a hosted model does both shape and texture, the job skips its own later shape and texture stages (and the optional stages that refine them); bake transfers the hosted model's colors onto the low-poly mesh. Inputs go to fal inline; results come back into your library.

## Spending limits

Limits are in US dollars; 0 means no limit. A job is checked when it is submitted, against its paid stages' prices and the costs already recorded.

| Limit | Setting | A job is refused when |
|---|---|---|
| Per image | `routing.budget_usd.per_job` | its paid stages cost more |
| Per day | `routing.budget_usd.per_day` | it would take today's spending past the limit |
| Per month | `routing.budget_usd.per_month` | it would take this month's spending past the limit |

While any limit is set, a hosted model without a fixed price per generation is refused, because its cost cannot be checked in advance.

```sh
forja config set routing.budget_usd.per_day=5
forja spend          # today, this month, and the limits
```

Spend caps for rented GPUs and routing policies beyond these are documented after integration ([routing](routing.md)).
