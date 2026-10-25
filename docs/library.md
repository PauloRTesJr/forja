# Library, export and Blender

Every finished job adds an asset to the library: its GLB, a thumbnail, the input image, and the job that made it, with each stage's model and timing. The core reads the triangle, vertex, material and LOD counts from the GLB.

```sh
forja assets ls [search]          # newest first; a search matches names and presets
forja assets show <asset-id>      # its mesh, where it was exported, the job that made it
forja assets get <asset-id> -o .  # save the GLB
```

## Export to other formats

The library exports to GLB, FBX, OBJ, glTF, USDZ, STL and Blender's .blend, one asset to a file or several into a folder. GLB is a copy. Every other format is converted by your own Blender, 4.2 or newer, run in the background with factory settings so add-ons cannot change the files.

| Format | LODs | Notes |
|---|---|---|
| GLB | kept | A copy of the library's file |
| FBX | kept | LOD names make a LOD group in Unity and Unreal |
| glTF | kept | A `.gltf` with its textures next to it |
| .blend | kept | The lower LODs are hidden |
| OBJ, USDZ | full detail only | |
| STL | full detail only | In millimetres, for printing |

A rigged asset's GLB carries its skin, and FBX, glTF and .blend keep its armature ([rigging](rigging.md#lods-and-exports)).

**In the app:** select one or more assets in the Library, or open one, and pick a format from the export menu.

**From a terminal:**

```sh
forja assets formats                           # the formats, and the Blender that converts them
forja assets export <asset-id> --format fbx    # into the current folder, named after the asset
forja assets export <id> <id> -f obj -o exports/
```

Forja looks for Blender on your `PATH` and where its installers put it. Set it when it is elsewhere: Settings, "Blender's program", or `forja config set blender.path=<full path to blender>`.

## Open in Blender

**Open in Blender** on an asset (or `forja assets blender <asset-id>`) starts Blender as you set it up, add-ons and preferences included, with the asset imported into a new unsaved file and its lower LODs hidden.

## Drag assets out

Library cards, with the rest of a selection, and **Drag out** on an asset's page drag out as GLB files: into a running Blender 4.2 or newer (which imports a dropped glTF), into Unity's Project window, or into a folder. The core writes a named copy to a temporary folder, kept for a day, before the drag begins. For an FBX, export it first.

## Export into a project

Run a job inside a project, a folder with a `forja.toml` such as the root of a game project, and the core copies each finished asset into the project's export folder: `<project>/Assets/Generated/<name>/<name>.glb` by default, with a `<name>.forja.json` that records the job, preset, input and each stage's model and license. Change the folder with `project.export_dir` (Settings, "Export folder inside a Unity project"); it must stay inside the project.

```toml
# forja.toml at the project's root
[project]
name = "My game"
export_dir = "Assets/Generated"
default_preset = "environment-piece"
```

## Delete assets

Deleting an asset removes it and the job that made it from the library (`forja assets rm <asset-id>`). Files you exported stay where they are. Its stage outputs stay in the cache, so regenerating it with other settings is fast, until eviction removes them.

## The cache

Every stage's output is kept by content and reused: a job that only changes the texture settings reruns only the stages after the shape. The cache is the store's files that no asset, job image or unfinished job uses.

- `general.cache_max_gb` (Settings, "Cache size limit") caps it: 50 GB by default, 0 for no limit. Past the limit, the core removes the least recently used files down to nine tenths of it.
- Eviction runs when the core starts, after the last running job ends, and when the limit or the library changes; never while a job runs, and never on files used in the last hour.
- The library's own files never count against the limit and are never removed.

```sh
forja cache          # the disk the cache and the library use
forja cache clear    # remove every reusable stage output now (Settings, "Clear the cache")
```
