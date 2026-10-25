# Cloud GPUs

A GPU you rent runs stages the way another computer on your network does: Forja's engine runs there, and the core sends it only the files it lacks, by content hash. Outputs come back into your library. ComfyUI joins the same way, with your own workflows, wherever it runs.

| Kind | What runs there | Billed | Files travel |
|---|---|---|---|
| [RunPod serverless](#runpod-serverless) | The engine's RunPod worker image, behind RunPod's job queue; scales to zero | Per second of GPU time | Through the endpoint's network volume, or inside each job |
| [RunPod pod](#runpod-pod) | The engine image on a pod you start and stop | Per hour while the pod runs | Like another computer: uploaded once by hash |
| [Modal](#modal) | A small gateway that answers at once, and a GPU worker that wakes to run stages | Per second of GPU time | Like another computer, through the gateway |
| [ComfyUI](#comfyui) | Your ComfyUI workflows, one per stage type | Free, unless you give it a price | Uploaded into ComfyUI's input folder |

RunPod and Modal are paid, and your images and meshes are sent to them. A paid provider runs a stage only when you pin a model to it, or when you turn on **Let paid providers step in** (`routing.allow_paid_fallback`); its price counts toward your spending limits ([routing](routing.md)).

## The engine image

Forja publishes no engine image yet: build the images yourself and push them to a registry you own. There is one image per CUDA line, with no weights inside. From the repository root:

```sh
docker build -f docker/engine/Dockerfile --build-arg VERSION=0.1.0 --build-arg CUDA=cu124 \
  -t forja-engine:0.1.0-cu124 .
```

| Build argument | Default | What it does |
|---|---|---|
| `VERSION` | `0.0.0-dev` | The version the image is labelled with |
| `CUDA` | `cu124` | `cu124` builds on NVIDIA's CUDA 12.4 devel image, whose `nvcc` compiles the CUDA extensions some families build at install. `cpu` builds an image for the stub models and the built-in stages |

Images are named `forja-engine:<version>-<cuda line>`; a [baked image](#baked-images) adds `-<model>`, and the [RunPod worker](#runpod-serverless) adds `-runpod`.

### Run it

```sh
docker run -d --gpus all -p 7861:7861 -v forja-data:/data \
  -e FORJA_ENGINE_TOKEN=<a long random token> \
  -e FORJA_MODELS=triposg,hunyuan3d2-paint \
  forja-engine:0.1.0-cu124
```

| Variable | What it does |
|---|---|
| `FORJA_ENGINE_TOKEN` | Required: the token cores send. Without it the container exits, because an engine other machines reach needs one |
| `FORJA_PORT` | The port to serve on (default 7861) |
| `FORJA_TLS_CERT`, `FORJA_TLS_KEY` | A certificate and key to serve. Without them, the container makes a self-signed certificate once, in `/data/tls` |
| `FORJA_MODELS` | Models to install before serving, comma separated. You can also install them from Forja afterwards (`forja models install <id> --provider <provider>`) |
| `FORJA_ENGINE_ARGS` | More `forja-engine serve` flags, such as `--fit-mode quality-first` on a large GPU |

The volume at `/data` holds the model environments, the weights, the artifact store, uv's cache and the certificate. Keep it, and a restarted container downloads nothing again.

Any other command runs in place of the engine, for example to install a model without serving:

```sh
docker run --rm --gpus all -v forja-data:/data forja-engine:0.1.0-cu124 \
  forja-engine install triposg --store /data/store
```

### Add it, and check its certificate

When it starts, the container prints its certificate's fingerprint:

```text
forja-engine: certificate SHA-256 AB:12:CD:34:EF:56:78:90:...
```

Add the engine with its token: in the app, Settings, **Add another computer**, with the access token instead of a pairing code; from a terminal:

```sh
forja providers add 192.0.2.10 --token-stdin < engine-token.txt
```

A self-signed certificate has no authority to vouch for it, so Forja pins its SHA-256 the first time it connects, and from then on talks to that address only through that certificate. It shows the first 16 hex digits, `AB12 CD34 EF56 7890` in this example: check them against the start of the line the container printed. The certificate lives on the volume, so it survives restarts; a new volume makes a new certificate, which the pin refuses, so remove the provider and add it again. A certificate of your own (`FORJA_TLS_CERT`) that your system trusts is checked the usual way and not pinned.

### Baked images

A baked image carries one model's environment and weights, for serverless workers whose cold start should download nothing. It is larger than the plain image; build one only for the models an endpoint serves:

```sh
docker build -f docker/engine/baked.Dockerfile --build-arg BASE=forja-engine:0.1.0-cu124 \
  --build-arg MODELS=triposg -t forja-engine:0.1.0-cu124-triposg .
```

| Build argument | Default | What it does |
|---|---|---|
| `BASE` | `forja-engine:0.1.0-cu124` | The engine image to start from |
| `MODELS` | none (required) | Models to bake in, comma separated |
| `LANE` | `cuda` | The torch lane to install for; the build has no GPU to detect one |
| `TORCH_CUDA_ARCH_LIST` | `8.0;8.6;8.9;9.0` | GPU architectures the CUDA extensions compile for (Ampere to Hopper) |

Licenses still apply to an image you build: an image holding a restricted model's weights, such as Hunyuan3D's, must not be shared where its license does not allow ([models and licenses](models.md)).

## RunPod serverless

A serverless endpoint runs the engine behind RunPod's job queue. It scales to zero and bills each second a worker runs.

1. **Build the worker image** on an engine image, plain or baked:

   ```sh
   docker build -f docker/runpod/Dockerfile --build-arg BASE=forja-engine:0.1.0-cu124 \
     -t forja-engine:0.1.0-cu124-runpod .
   ```

2. **Push it** to a registry you own, under a name such as `<your-registry>/forja-engine:0.1.0-cu124-runpod`.
3. **Create a serverless endpoint** from it in the RunPod console, with a network volume attached. RunPod mounts the volume at `/runpod-volume` on every worker. Set the endpoint's environment variables:

   | Variable | What it does |
   |---|---|
   | `FORJA_DATA_DIR` | Not needed with a network volume: the worker keeps environments, weights and the artifact store in `/runpod-volume/forja`, where the core's S3 transfers look, whatever this says. Without a volume it is where they go (the image sets `/data`, which goes away with each worker) |
   | `FORJA_MODELS` | Models each worker needs, comma separated; installed on the volume once, before the worker takes its first job |
   | `FORJA_ENGINE_ARGS` | More engine flags, such as `--fit-mode quality-first` on a 24 GB GPU or larger. A baked image sets this variable itself, to `--envs /opt/forja/baked/envs --models /opt/forja/baked/models`: setting it on the endpoint replaces that, so repeat those flags in your value |

4. **Add it in Forja.** In the app: Settings, **Where models run**, **Add a cloud GPU or ComfyUI**, then **RunPod serverless**. Fill in the endpoint ID from the endpoint's page, a RunPod API key, and optionally the GPU price in USD per second. For the network volume, add its ID, its datacenter (such as `EU-RO-1`) and an S3 API key (an access key and its secret, from Settings, S3 API Keys, in the RunPod console). **Try it** checks everything without saving; **Add** saves it.

   From a terminal, keys come on stdin, one per line: the API key, then the volume's S3 secret.

   ```sh
   forja providers add runpod <your-endpoint-id> --price 0.00031 < runpod-key.txt

   forja providers add runpod <your-endpoint-id> --volume <your-volume-id> --datacenter EU-RO-1 \
     --volume-key-id <your-s3-access-key> < runpod-keys.txt
   ```

   `forja providers test runpod ...` takes the same arguments and saves nothing.

The check asks RunPod's health endpoint about the endpoint's workers and queue, which costs nothing and wakes no worker, and tries the S3 key on the volume. Once the endpoint is added, Forja asks a worker what it runs, which starts one. After that, while the endpoint sleeps, Forja uses the worker's last answer instead of waking it, and only a job wakes it.

### How files travel

- **With the volume's S3 key**, the engine's store lives on the volume (`forja/store`), and the core reaches it through RunPod's S3 API for that datacenter. Before a run, it checks each input's hash and uploads only what the volume lacks; after, it downloads only the outputs this computer lacks. Intermediate files stay on the volume between runs.
- **Without it**, the inputs ride in the job as base64, and every output comes back in the job's result. RunPod caps a job's payload at 10 MB, and base64 grows data by a third, so each direction is capped at 7 MB. A stage whose inputs or outputs pass that fails with a message asking for the volume's S3 key. Nothing stays on the endpoint between runs.

Each run is one queue job. The worker streams the engine's events back while it runs, so progress shows as it does for a local engine; cancelling the job in Forja cancels it on RunPod. A failed look at the job is tried again a few times, waiting longer each time; when RunPod still does not answer, Forja cancels the job there before it fails the stage, so a job it no longer follows does not run on and bill.

### What it costs

The GPU price you give is what Forja counts per second of a run. Without one, it uses the price table's `runpod-serverless-4090` entry, $0.00031 per second ([the price table](routing.md#the-price-table)).

## RunPod pod

A pod is a rented GPU that runs, and bills, until you stop it. It runs the plain engine image, reached at its public address like another computer.

1. Create a pod from your pushed `forja-engine:<version>-cu124` image, with a volume mounted at `/data`, TCP port 7861 exposed, and `FORJA_ENGINE_TOKEN` set (and `FORJA_MODELS`, if you like). The variables are those of [the engine image](#run-it).
2. Add it with the pod's public IP and the TCP port mapped to 7861. In the app: **Add a cloud GPU or ComfyUI**, then **RunPod pod**, with the address, the token and optionally the pod's price per hour. From a terminal, the token on stdin:

   ```sh
   forja providers add runpod-pod 192.0.2.10:40123 --price-per-hour 0.34 < pod-token.txt
   ```

Forja pins the pod's certificate as it does any engine's ([check it](#add-it-and-check-its-certificate)). The price per hour is counted by the second, for the time the pod spends on your runs. A pod added without a price is priced by the price table's `runpod-pod-4090` entry, $0.34 an hour, which you can edit; Forja writes `price = "runpod-pod-4090"` in its `[[providers]]` entry. Stop the pod in the RunPod console when you are done; Forja does not stop it.

## Modal

On Modal, a CPU gateway serves the engine's API: health, artifacts by hash and run state. It wakes a GPU worker only to run stages, so a core reaches it like any remote engine and checking it costs no GPU time.

Deploy it from the repository root with your own Modal account and the Modal CLI:

```sh
modal secret create forja-engine FORJA_ENGINE_TOKEN=<a long random token>
modal deploy docker/modal/app.py
```

The image builds on Modal from `docker/engine/Dockerfile`; nothing is pushed to a registry. The app, `forja-engine`, has:

- a gateway on 1 CPU and 1 GB of memory, taking up to 32 requests at once;
- a GPU worker, one container at most, which scales down after 120 seconds without work and runs a job for up to an hour;
- two volumes: `forja-data` for environments and weights, and `forja-artifacts` for the store the gateway and the worker share; and a dictionary, `forja-board`, that carries run state and events from the worker to the gateway.

Settings for the deploy, as environment variables of the `modal deploy` command:

| Variable | Default | What it does |
|---|---|---|
| `FORJA_MODAL_GPU` | `L40S` (48 GB) | The worker's GPU type; `A10G` or `L4` for models that fit in 24 GB |
| `FORJA_CUDA` | `cu124` | The engine image's CUDA line |
| `FORJA_VERSION` | `0.1.0` | The version the image is labelled with |

and in the `forja-engine` secret, besides the token: `FORJA_MODELS`, the models the worker installs on its volume before its first run, and `FORJA_ENGINE_ARGS`, more engine flags.

Add it with the gateway address `modal deploy` prints. In the app: **Add a cloud GPU or ComfyUI**, then **Modal**, with the address, the token and optionally the GPU price in USD per second. From a terminal:

```sh
forja providers add modal https://<you>--forja-engine-gateway.modal.run --price <usd-per-second> < modal-token.txt
```

The check sends one artifact lookup, which needs the token and wakes no GPU. Once the gateway is added, Forja asks what it runs, and the gateway wakes the GPU worker to answer; while it is still starting, the gateway says "the GPU worker is still starting; try again in a minute". The price table has no Modal entry, so give the GPU price when you add it.

## ComfyUI

A ComfyUI provider runs stages as your own ComfyUI workflows, wherever ComfyUI runs: this computer, another one on the network, or a rented GPU. Each stage type you map gets one workflow: background removal, shape or texture.

### Prepare a workflow

1. Build the workflow in ComfyUI and save it with **Export (API)**. Forja refuses the editor's format.
2. Put placeholders where Forja's inputs go:

   | Placeholder | Becomes |
   |---|---|
   | `{{image}}` (or `{{image:front}}`) | The front image, as a file name in ComfyUI's input folder. Every workflow needs it |
   | `{{image:left}}`, `{{image:back}}`, `{{image:right}}` | Another view of the subject; the workflow then runs only on jobs that have that view |
   | `{{mesh}}` | The mesh to paint, as a GLB file name in ComfyUI's input folder. A texture workflow needs it |
   | `{{seed}}` | The job's seed |
   | `{{param:<name>}}` | A parameter of the preset's stage, such as `{{param:resolution}}` |

   A value that is only a placeholder takes the type of what replaces it, so `"seed": "{{seed}}"` becomes a number. Inside longer text, a placeholder is replaced as text.

3. End the workflow with a node that saves its result: a GLB for shape and texture, a PNG or WebP for background removal. Forja downloads the first such file the finished prompt lists.

A fragment of a shape workflow in the API format:

```json
{
  "1": { "class_type": "LoadImage", "inputs": { "image": "{{image}}" } },
  "7": { "class_type": "KSampler", "inputs": { "seed": "{{seed}}", "steps": "{{param:steps}}" } }
}
```

### Add it

In the app: **Add a cloud GPU or ComfyUI**, then **ComfyUI**. Give its address, choose a workflow file for each stage it does, and an access token only for a ComfyUI behind a proxy that asks for one. From a terminal:

```sh
forja providers add comfyui 192.0.2.10:8188 --workflow shape=hunyuan-shape.json --workflow texture=paint.json
```

An address without a scheme means `http://`, and without a port, 8188. `--token-stdin` reads a proxy's token from stdin; `--price <usd-per-second>` makes it a paid provider.

A token goes only over https: a ComfyUI that takes one needs its proxy's `https://` address, unless it runs on this computer (`localhost`, `127.0.0.1`). Forja refuses to add one at a plain `http://` address on another machine, and refuses to call one whose address in `config.toml` was changed to that, rather than send the token in the clear.

```sh
forja providers add comfyui https://comfy.example.com --workflow shape=hunyuan-shape.json --token-stdin < token.txt
```

Forja checks each workflow (the API format, known placeholders, the inputs its stage needs) and asks ComfyUI's `/system_stats`, then copies each workflow beside `config.toml`, to `comfyui/<provider id>/<stage type>.json`. To change a workflow, edit that copy; the core reads it when it starts. A workflow is keyed by its content, so an edited workflow never reuses results cached from the old one.

### Use it

Each workflow is a model named after its stage type: `comfyui-background-removal`, `comfyui-shape` and `comfyui-texture`. Pin one to a stage like any model:

```sh
forja models pin shape comfyui-shape --provider comfyui
```

Without a price, a ComfyUI is free, and it takes part in routing like another computer: after this computer in `routing.priority`, it runs a stage this computer has no ready model for. The `offline-only` policy leaves it out. Inputs upload to ComfyUI's input folder once per session, named after their hash (`forja-<hash>.png`, or `.jpg`, `.webp`, `.glb`). Progress comes from ComfyUI's event socket, or from its history when a proxy passes no WebSockets. Its nodes and models carry their own licenses, which Forja cannot read.

## Manage providers

```sh
forja providers kinds            # every kind of provider, and what each needs
forja providers ls               # health, key and certificate of each
forja providers token <provider> < new-key.txt   # replace a key once the new one works
forja providers rm <provider>    # forget it, its keys and the stages routed to it
```

Keys and tokens live only in the OS keychain. A RunPod endpoint's volume key is a second keychain entry beside its API key. `config.toml` keeps the rest, in a `[[providers]]` entry:

```toml
[[providers]]
id = "runpod-abc123xyz789"
label = "RunPod abc123xyz789"
kind = "serverless"
vendor = "runpod"
secret = "provider-runpod-abc123xyz789"
paid = true
endpoint_id = "abc123xyz789"
price_usd_per_second = 0.00031
volume = "<your-volume-id>"
region = "EU-RO-1"

[[providers]]
id = "comfyui"
label = "ComfyUI"
kind = "comfyui"
url = "http://192.0.2.10:8188"
workflows = { shape = "comfyui/comfyui/shape.json" }
```

| Key | What it is |
|---|---|
| `kind` | `engine` (another computer, a RunPod pod), `serverless` (RunPod, Modal), `api` (a model hub) or `comfyui` |
| `vendor` | `runpod` or `modal`; for a hub, `fal` |
| `url` | The address of an engine, a Modal gateway or a ComfyUI |
| `secret` | The keychain entry that holds the key, never the key itself |
| `fingerprint` | The pinned SHA-256 of an engine's certificate |
| `endpoint_id` | A RunPod serverless endpoint's ID |
| `volume`, `region` | A RunPod endpoint's network volume and its datacenter |
| `workflows` | A ComfyUI's workflow files by stage type, relative to the settings folder |
| `paid` | Whether it costs money |
| `price_usd_per_second` | What its time costs |
| `price` | A [price table](routing.md#the-price-table) entry for its time, used when `price_usd_per_second` is not set |
