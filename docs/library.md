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

## Materials

Local painters make base color only, so a generated asset ships with one roughness (0.8) and one metallic (0) for its whole surface: a chest's iron bands shade like its wood. **Materials** lets you say how rough and how metallic each part is. Nothing is guessed for you.

Forja finds the materials as the few dominant colors of the base color texture (at most six, largest first; colors that look alike count as one, and specks are ignored). You set two numbers from 0 to 1 for each, and Forja writes them into the asset's metallic-roughness map (green is roughness, blue is metallic; the occlusion in red is kept), blurring the borders a little so they do not alias. The same texture always gives the same materials.

It edits the asset in place: its GLB is replaced, so exports and drag-outs carry the change, and the geometry, LODs and rig are untouched. The old file goes at the next sweep. Files you exported earlier, and copies made into a project, are not changed: export again.

**In the app:** open an asset; **Materials** lists each color with its share of the texture and a Roughness and a Metallic slider. The change is saved a moment after you stop moving a slider, and the viewer shows the new file.

**From a terminal:**

```sh
forja assets materials <asset-id>                                  # index, color, share, roughness, metallic
forja assets materials <asset-id> --set 1.roughness=0.3 --set 1.metallic=1
```

`--set <index>.roughness=<0 to 1>` and `--set <index>.metallic=<0 to 1>` can be repeated; materials you do not name keep their values. `--json` prints what the core answered.

- **Painted maps.** Some models (TRELLIS.2 Paint, hosted models) paint roughness and metallic themselves. Such an asset shows "This asset's roughness and metallic were painted by its model." instead of sliders, and a command that would replace the painting is refused; **Replace them…** in the app (after you confirm), or `--overwrite`, does it.
- **Textures Forja can edit.** PNG and JPEG textures stored inside the GLB. An asset with KTX2 or WebP textures, or none, says why it cannot be edited.
- **Several textures.** An asset whose materials use different base color textures lists each texture's colors in turn, numbered through; materials that share one texture share one map.

## Delete assets

Deleting an asset removes it and the job that made it from the library (`forja assets rm <asset-id>`). Files you exported stay where they are. Its files in the object store go at the next sweep ([what stays on disk](#what-stays-on-disk)).

## What stays on disk

The object store keeps every asset's GLB, thumbnail and input, every job's input images, and everything an unfinished job uses. A sweep removes the rest: the stage outputs of finished jobs, such as intermediate meshes and textures, and the files of deleted assets.

- A sweep runs when the core starts, after the last running job ends, and after an asset is deleted. It never runs while a job runs, and it leaves alone anything stored in the last hour.
- There is no setting, command or button for it.

Nothing carries over from one job to the next: running the same image again runs every stage from the start ([decision 37](decisions.md)).
