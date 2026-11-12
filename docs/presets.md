# Presets

A preset is a pipeline: the stages a job runs, in order, with their budgets. It names stage types, not models; each stage gets a model when the job is submitted (see [choosing a model](#which-model-a-stage-uses)).

## Built-in presets

| Preset | id | Triangles | Texture | LODs | Use it for |
|---|---|---|---|---|---|
| Draft | `draft` | as generated | none | none | Judging the shape before spending time on texture |
| Mobile prop | `mobile-prop` | 5,000 | 1K base color, baked normal and AO | 50% and 25% | Props for mobile and the web; the default |
| Environment piece | `environment-piece` | 15,000 | 2K base color, baked normal and AO | 50% and 25% | Buildings, rocks, set dressing |
| Hero character | `hero-character` | 30,000 | 2K base color, baked normal and AO | 50% and 25% | Characters seen up close; optionally rigged |

Every preset starts the same way: remove the background, frame the subject at 1024 pixels, generate the shape, clean it up. The textured presets then reduce the mesh to their triangle budget, paint it (the painter unwraps UVs itself, unless a UV stage did; Mobile prop and Environment piece can paint a denser, 40,000-triangle version instead, see [optional stages](#optional-stages)), bake the high-poly detail into normal and AO maps ([baking](remesh.md#baking)), build the LODs and export a GLB, meshopt-compressed when `gltfpack` is on your `PATH`.

Local painters produce base color only. Roughness and metallic are constants, the paint stage's `roughness` (0.8) and `metallic` (0) parameters.

## Optional stages

Optional stages are switches under the preset on Generate, all off by default. From the CLI, `--with <stage>` turns one on and `--skip <stage>` turns it off.

| Switch | Stage | Presets | What it does |
|---|---|---|---|
| Match the reference colors | `match` | Mobile prop, Environment piece, Hero character | Pulls the painted texture back toward the reference image's colors and projects the reference onto the parts of the asset it shows, and the back and side views too when the job has them. Painting models can drift in color; turn it on when they do |
| Quad remesh with Blender | `remesh` | Environment piece, Hero character | Rebuilds the mesh from even quads in place of the decimation, within the same triangle budget ([quad remesh](remesh.md#quad-remesh)) |
| Unwrap with Blender's Smart UV Project | `uv` | Environment piece, Hero character | Unwraps before painting, in fewer, straighter islands ([Smart UV](remesh.md#smart-uv-project)) |
| Paint a denser mesh | `dense` | Mobile prop, Environment piece | Paints a 40,000-triangle version of the shape instead of the low-poly, then bakes its colors onto the low-poly together with the normals ([details](remesh.md#baking-colors-from-a-denser-mesh)). Slower; not measured on real painters yet, so try it against the default on your own images |
| Rig the character | `rig` | Hero character | Adds a skeleton and skin weights ([rigging](rigging.md)) |

```sh
forja generate chest.png --with match
forja generate chest.png --with dense
forja generate knight.png --preset hero-character --with remesh --with rig
```

A switch that cannot run now, such as a Blender stage where no provider has Blender or the rig with no rig model ready, is greyed out on Generate with the reason; `forja presets show <preset>` prints it, and a job that turns it on is refused.

## Changing a preset's values

For one job, `--set <stage>.<param>=<value>`:

```sh
forja generate chest.png --set lowpoly.target_tris=3000 --set paint.resolution=2048
```

For every job, in `config.toml` or a project's `forja.toml`:

```toml
[presets.mobile-prop.overrides]
"lowpoly.target_tris" = 3000
```

`<stage>.model` and `<stage>.provider` work the same way: `--set shape.model=triposg` runs this job's shape stage on TripoSG.

`forja presets ls` lists presets, whether each can run now and the models it would use; `forja presets show <preset>` lists its stages with model, provider and price. `forja generate --dry-run` shows the same plan for a job without queueing it.

## Your own presets

Presets are YAML files. Put yours in the `presets` folder next to `config.toml`, or in a project's `forja/presets/` folder so it travels with the project's repository. A preset with the same `id` as another replaces it: built-in, then yours, then the project's.

```yaml
id: tiny-prop
name: Tiny prop
description: 1,500 triangles, 512 base color, one LOD.
stages:
  - { id: bg, use: background-removal, inputs: [input:image] }
  - { id: frame, use: framing, params: { size: 1024 } }
  - { id: shape, use: shape }
  - { id: cleanup, use: mesh.cleanup }
  - { id: lowpoly, use: mesh.decimate, params: { target_tris: 1500 } }
  - { id: paint, use: texture, params: { resolution: 512 }, inputs: [lowpoly, frame] }
  - { id: bake, use: bake, inputs: [paint, cleanup] }
  - { id: lods, use: mesh.lods, params: { ratios: [0.5] } }
  - { id: export, use: export, params: { format: glb, compression: meshopt } }
```

A stage reads the previous stage's output unless `inputs` names other stage ids, or `input:image` for the job's image. An `optional:` block with `label`, `help` and `default` makes a stage switchable, and its `replaces: <stage>` makes it stand in for a later stage while it is on ([replacing a stage](remesh.md#replacing-a-stage-in-your-own-presets)). The last stage cannot be optional.

| Stage type | Runs | Reads |
|---|---|---|
| `background-removal` | a model | the image |
| `framing` | built in | the cut-out |
| `shape` | a model | the framed cut-out (and side views) |
| `mesh.cleanup` | built in | a mesh |
| `mesh.decimate` | built in | a mesh; `target_tris`, and `source`, the number of the input to read (default 0: the first) |
| `mesh.remesh` | built in, through Blender | a mesh; `target_tris`, `preserve_sharp` |
| `mesh.uv` | built in | a mesh (painters unwrap for themselves) |
| `mesh.uv.smart` | built in, through Blender | a mesh; `angle_limit`, `margin` |
| `texture` | a model | a mesh and the framed cut-out |
| `texture.match` | built in | the painted mesh and the framed cut-out, plus the framed left, back and right views when the job has them |
| `bake` | built in | the low-poly painted mesh and the high-poly mesh, and optionally the unpainted low-poly as a third mesh with fewer triangles than the first, which then only gives its colors ([denser mesh](remesh.md#baking-colors-from-a-denser-mesh)); `resolution`, `samples`, `cage`, `ao_samples`, `ao_radius` |
| `rig` | a model | the baked low-poly mesh |
| `mesh.lods` | built in | a mesh; `ratios` |
| `export` | built in | a mesh; `format: glb`, `compression: meshopt` |

## Which model a stage uses

When a job is submitted, each stage without a model gets one, first match wins:

1. The job's own `<stage>.model` override.
2. The model pinned for the stage type: `forja models pin shape triposg` (the Models screen does the same), stored under `[models.stages]` in `config.toml`.
3. The highest-ranked model that is installed and ready, on the first provider that has one, in `routing.priority` order as the routing policy sorts it ([routing](routing.md#routing-policies)). For a job with side views, a model that reads them comes first.

A stage with no ready model refuses the job instead of falling back to a lesser stage. Stub models are picked only when `models.stubs = true`.
