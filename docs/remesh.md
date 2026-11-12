# Quad remesh, Smart UV and baking

Two optional stages rebuild the mesh's topology and UVs with your own Blender: **quad remesh** and **Smart UV Project**. Both are switches on the Environment piece and Hero character presets, off by default. This page also covers how the bake turns the high-poly's detail into maps.

## Quad remesh

**Quad remesh with Blender** (`remesh`, stage type `mesh.remesh`) rebuilds the cleaned-up mesh from evenly sized quads with Blender's QuadriFlow, instead of simplifying it. Even topology is easier to edit by hand, and deforms better when the character is [rigged](rigging.md). The quads are split into triangles in the GLB.

```sh
forja generate rock.png --preset environment-piece --with remesh
forja generate knight.png --preset hero-character --with remesh --with rig
```

- **It replaces the decimation.** While it is on, the preset's `lowpoly` stage (`mesh.decimate`) does not run, and the stages that read it read the remesh. The remesh takes the decimation's parameters, so it keeps the preset's triangle budget: 15,000 triangles on Environment piece, 30,000 on Hero character, and your `lowpoly.target_tris` override if you set one.
- **It stays within the budget.** QuadriFlow is asked for half as many quads as the triangle budget. It lands near the count it is asked for, not on it, so the stage tries up to three times and keeps the fullest result within the budget; in the rare case every try overshoots, the engine trims the result to the budget. A mesh with more than 16 times the budget's faces is decimated first, since QuadriFlow's time follows its input and the bake recovers the detail anyway.
- **Sharp edges.** Environment piece keeps sharp edges (`remesh.preserve_sharp = true`); Hero character does not.
- **Open meshes.** QuadriFlow takes only closed, consistently wound meshes. When it refuses one, the stage rebuilds the surface from voxels a quarter of a quad's edge wide, which closes holes and drops inner shells, then remeshes that, trying coarser voxels (half, then a whole quad's edge) when it refuses again. Points closer than QuadriFlow tolerates are merged before every try.
- **When QuadriFlow cannot do it.** Some shapes defeat QuadriFlow on every try. The stage then simplifies the mesh the usual way, as if the switch were off, so the job still finishes, and says so: the Queue and the asset's page show "Quad remesh: QuadriFlow could not remesh this shape, so it was simplified the usual way instead: triangles, not quads", and `forja generate` and `forja jobs show` print it under the stage. Without Blender the stage still fails instead.
- **Not repeatable.** QuadriFlow makes a different mesh on each run, even with the same seed, so running the same job again gives other quads ([decision 33](decisions.md)).

```sh
forja generate rock.png --preset environment-piece --with remesh --set lowpoly.target_tris=8000
forja generate rock.png --preset environment-piece --with remesh --set remesh.preserve_sharp=false
```

## Smart UV Project

**Unwrap with Blender's Smart UV Project** (`uv`, stage type `mesh.uv.smart`) unwraps the low-poly before painting. It cuts UV islands where faces meet at a sharp angle, which gives fewer, straighter islands than the painter's own unwrap: easier to edit by hand, and suited to hard-surface pieces. Painters keep UVs a mesh brings; without this stage, they unwrap the mesh themselves.

```sh
forja generate crate.png --preset environment-piece --with uv
```

| Parameter | Default | What it does |
|---|---|---|
| `uv.angle_limit` | 66 | Faces meeting at more than this many degrees go to separate islands |
| `uv.margin` | 0.004 | Space between packed islands, as a fraction of the texture |

The stage splits vertices along the new seams and keeps the normals welded across them, so the shading does not change at a seam.

## Blender

Both stages run Blender 4.2 or newer on the computer that runs the engine, on its CPU, as a separate program in the background with factory settings, so add-ons and your startup file cannot change the result. Blender is never linked into Forja. A remesh may take up to 15 minutes before it fails, an unwrap up to 10.

The engine looks for Blender:

1. at `FORJA_BLENDER`. The core passes your `blender.path` setting there to the engine it starts (Settings, "Blender's program", or `forja config set blender.path=<full path to blender>`), and restarts that engine when the setting changes and no job runs;
2. as `blender` in a folder of the `PATH`, absolute folders only, so a Blender left in the folder where you run `forja` is never started;
3. where Blender's installers put it: on Windows, the program `.blend` files open with, `Program Files\Blender Foundation\Blender <version>\blender.exe` (the newest first) and Steam's Blender; on macOS, `/Applications/Blender.app` and `~/Applications/Blender.app`; on Linux, the snap and the Flatpak.

An engine started by hand with `forja engine serve` gets your `blender.path` the same way. One started some other way, such as `forja-engine serve` or the Docker image, reads only `FORJA_BLENDER`, the `PATH` and the installers' locations. Another computer that shares its GPU uses its own Blender.

### Without Blender

An engine without Blender, or with one older than 4.2, does not offer these stages, and says why in `/v1/info`. Routing sends each Blender stage to the first provider that offers it ([routing](routing.md)). When none does:

- **In the app**, the switch is greyed out on Generate, with the reason and an **Install Blender** button. It shows the Blender check with its fix: on Windows, Forja installs Blender with winget, and Windows asks for permission; on macOS and Linux it gives the steps. Once Blender is found the switch turns on. When the Blender settings name a program that is not there, the fix is to correct or clear that path instead.
- **From a terminal**, `forja presets show <preset>` and `forja generate --dry-run` print that the stage cannot be turned on now, and why, and point at `forja models checks --stage mesh.remesh`, which shows the same check; `forja models fix blender` runs its fix. A job that turns the stage on anyway is refused with the reason: "Blender 4.2 or newer was not found on the computer that runs the engine: install it, or set its path in Settings (blender.path) or in FORJA_BLENDER".

## Replacing a stage in your own presets

An optional stage can stand in for another with `replaces:` ([your own presets](presets.md#your-own-presets)):

```yaml
- id: remesh
  use: mesh.remesh
  inputs: [cleanup]
  optional: { label: Quad remesh with Blender, default: false, replaces: lowpoly }
- { id: lowpoly, use: mesh.decimate, inputs: [cleanup], params: { target_tris: 15000 } }
```

While the optional stage is on, the stage it names is left out, the stages that read it read the replacement, and the replacement takes its parameters under its own. The stage it names must come later and must not be optional itself.

## Baking

The bake (`bake`) turns the high-poly's detail into a tangent-space normal map and ambient occlusion on the low-poly's UVs ([decision 35](decisions.md)).

- **MikkTSpace tangents.** The normal map is baked against MikkTSpace tangents, the ones Blender, Unreal, Unity and glTF viewers compute themselves, so it reads the same everywhere, with or without the GLB's tangents. Vertices split where MikkTSpace gives one vertex two frames, and the tangents are written into the GLB; the LODs and the rig keep them.
- **A measured cage.** Each sample's ray starts on a cage just outside the low-poly and travels inward. The cage distance is twice the 95th percentile of how far the high-poly lies from the low-poly, between 0.3% and 5% of the low-poly's diagonal. A ray that misses tries again from twice and four times as far. A fixed 2% cage picked up nearby surfaces on characters; on the hero test the normal error fell from 4.23° to 0.49°, at 2.5 times the speed.
- **4 samples per texel**, on a rotated grid, which smooths jagged edges.
- **Seams.** A ring of 1.5 texels around each UV island is baked as well, continuing the surface past the island's edge, and the rest of each gutter is filled from the island's edge. Neither bilinear filtering nor any mip level pulls a gutter's color across a seam.
- **AO per island.** Occlusion rays are cast from the high-poly hits at half the map's resolution, and the AO is smoothed only within each UV island, so its noise never crosses a seam.

| Parameter | Default | What it does |
|---|---|---|
| `bake.resolution` | the painted texture's size, else 1024 | The maps' size, in pixels |
| `bake.samples` | 4 | Samples per texel: 4, or 1 for speed |
| `bake.cage` | measured | The cage distance, as a fraction of the low-poly's diagonal |
| `bake.ao_samples` | 32 | Occlusion rays per AO texel |
| `bake.ao_radius` | 0.15 | How far occlusion reaches, as a fraction of the diagonal |

When fewer than 95% of the texels' rays reach the high-poly, the engine's log warns and suggests raising `bake.cage`:

```sh
forja generate statue.png --preset hero-character --set bake.cage=0.02
```

Ray casting uses Embree, through `embreex`, on Windows, macOS and Linux on x86-64. Where it has no build, normals fall back to the nearest high-poly vertex and AO is left white, so a bake never fails for lack of it.

### Baking colors from a denser mesh

Painters condition on renders of the mesh they are given, and Hunyuan3D-2 Paint's own tooling reduces meshes to 40,000 faces before painting, never to 5,000. Painted detail on a 5,000-triangle low-poly cannot line up with the detail the bake takes from the high-poly into the normal map. The **Paint a denser mesh** switch (`dense`, off by default, on Mobile prop and Environment piece; [optional stages](presets.md#optional-stages)) paints a 40,000-triangle decimation of the high-poly instead and lets the bake carry its colors onto the low-poly:

- The bake takes the painted dense mesh as its first input, the high-poly as its second and the unpainted low-poly as a third. A third mesh with **fewer triangles** than the first is the low-poly; the first then gives only its colors. With no third mesh, or one with as many triangles as the first, the bake runs as described above.
- The low-poly is unwrapped in the bake when it has no UVs (it does not on Mobile prop, and on Environment piece with the Smart UV switch off; with it on, those UVs are the ones baked into). Normals and AO come from the high-poly as before.
- The base color, and the painter's roughness and metallic map when it made one, are sampled from the painted mesh by rays from the same cage, with the same retries, samples per texel and gutter filling as the normals. Roughness and metallic factors come from the painted mesh's material, and the maps' size is `bake.resolution`, else the painted texture's size.
- A dense mesh with no colors of its own (a hosted model that does shape and texture skips the paint stage) cannot give any: the bake then takes the high-poly's colors, as it does when the paint stage is skipped.

The second set of rays makes the bake about half again as long: on a 5,000-triangle low-poly with a 40,000-triangle painted mesh at 1024 pixels, about 5 seconds instead of 3 on a developer laptop. The painter has more triangles to unwrap too. Whether the painted detail is better in the end has not been measured on real painters; compare both on your own images with `forja generate chest.png` and `forja generate chest.png --with dense`.
