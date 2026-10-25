# Runs inside Blender for the Forja core (core/internal/export/blender.go): converts assets'
# GLBs to other formats, or opens one for editing. The argument after "--" is a JSON job:
#
#   {"mode": "export", "format": "fbx", "items": [{"glb": "<path>", "out": "<path>", "name": "chest"}]}
#   {"mode": "open", "items": [{"glb": "<path>", "name": "chest"}]}
#
# Each exported item prints one line: FORJA {"index": 0, "files": [...]} or {"index": 0, "error": "..."}.
import json
import os
import re
import sys
import traceback

import bpy

LOD = re.compile(r"_LOD(\d+)(\.\d+)?$")


def lod_level(obj):
    m = LOD.search(obj.name)
    return int(m.group(1)) if m else 0


def reset():
    bpy.ops.wm.read_homefile(use_empty=True)


def import_glb(path):
    before = set(bpy.data.objects)
    options = {"import_pack_images": True}
    # Newer importers draw each bone of a rig as a sphere, an object every export would carry.
    if "disable_bone_shape" in bpy.ops.import_scene.gltf.get_rna_type().properties.keys():
        options["disable_bone_shape"] = True
    bpy.ops.import_scene.gltf(filepath=path, **options)
    shapes = {b.custom_shape for o in bpy.data.objects if o.type == "ARMATURE" for b in o.pose.bones}
    for o in [o for o in bpy.data.objects if o in shapes and o not in before]:
        bpy.data.objects.remove(o, do_unlink=True)
    return [o for o in bpy.data.objects if o not in before]


def rigged():
    return any(o.type == "ARMATURE" for o in bpy.context.scene.objects)


def hide_lower_lods(objects):
    # Every level sits at the same place; the full one alone is what a person looks at.
    for o in objects:
        if lod_level(o) > 0:
            o.hide_set(True)
            o.hide_render = True


def keep_full_detail(objects):
    # Formats that have no idea of levels of detail would stack them on top of each other.
    for o in objects:
        if o.type == "MESH" and lod_level(o) > 0:
            bpy.data.objects.remove(o, do_unlink=True)


def select_meshes():
    bpy.ops.object.select_all(action="DESELECT")
    for o in bpy.context.scene.objects:
        if o.type == "MESH":
            o.select_set(True)


def file_word(name):
    """A name without spaces: .mtl readers split lines at them."""
    return re.sub(r"[^\w-]+", "_", name).strip("_")


def name_images(stem):
    # Texture files are named after their images: the asset's name keeps assets exported to
    # one folder from overwriting each other's.
    prefix = file_word(stem) or "asset"
    for i, img in enumerate(list(bpy.data.images)):  # renaming re-sorts bpy.data.images
        img.name = f"{prefix}_" + (file_word(img.name.rsplit(".", 1)[0]) or f"texture{i}")


def save_images(out_dir):
    """Writes packed textures next to the file, for formats that reference them by path."""
    files = []
    for img in bpy.data.images:
        if img.packed_file is None or img.size[0] == 0:
            continue
        ext = ".jpg" if img.file_format == "JPEG" else ".png"
        path = os.path.join(out_dir, img.name + ext)
        img.unpack(method="REMOVE")
        img.filepath_raw = path
        img.file_format = "JPEG" if ext == ".jpg" else "PNG"
        img.save()
        files.append(path)
    return files


def referenced_maps(mtl):
    """The texture files a .mtl names: the last word of each map line."""
    with open(mtl, encoding="utf-8") as f:
        return {line.split()[-1] for line in f if line.startswith(("map_", "norm ", "bump ", "disp "))}


def export(fmt, out):
    out_dir = os.path.dirname(out)
    stem = os.path.splitext(os.path.basename(out))[0]
    objects = list(bpy.context.scene.objects)
    name_images(stem)
    files = [out]
    if fmt == "fbx":
        # Unity and Unreal read the _LOD<n> names as a LOD group. Textures go inside the file.
        # Blender documents baking the axis conversion as broken with armatures, so a rig keeps
        # it in the file's axis settings instead; with no animation there is no take to bake.
        rig = rigged()
        bpy.ops.export_scene.fbx(
            filepath=out, apply_scale_options="FBX_SCALE_ALL", bake_space_transform=not rig,
            mesh_smooth_type="FACE", add_leaf_bones=False, path_mode="COPY", embed_textures=True,
            object_types={"EMPTY", "MESH", "ARMATURE"}, bake_anim=not rig,
        )
    elif fmt == "gltf":
        before = set(os.listdir(out_dir))
        bpy.ops.export_scene.gltf(filepath=out, export_format="GLTF_SEPARATE", export_yup=True)
        files = [os.path.join(out_dir, f) for f in sorted(set(os.listdir(out_dir)) - before)]
    elif fmt == "obj":
        keep_full_detail(objects)
        images = save_images(out_dir)
        bpy.ops.wm.obj_export(filepath=out, export_materials=True, path_mode="RELATIVE")
        mtl = os.path.join(out_dir, stem + ".mtl")
        used = referenced_maps(mtl)
        for path in images:
            if os.path.basename(path) in used:
                files.append(path)
            else:
                os.remove(path)  # an OBJ material has no slot for it
        files.append(mtl)
    elif fmt == "stl":
        keep_full_detail(objects)
        # STL has no unit, and slicers read millimetres: keep the asset's real size.
        bpy.ops.wm.stl_export(filepath=out, global_scale=1000.0)
    elif fmt == "usdz":
        keep_full_detail(objects)
        bpy.ops.wm.usd_export(
            filepath=out, export_materials=True, generate_preview_surface=True, convert_orientation=True,
            export_global_forward_selection="NEGATIVE_Z", export_global_up_selection="Y",
            export_lights=False, export_cameras=False,
        )
    elif fmt == "blend":
        hide_lower_lods(objects)
        bpy.ops.file.pack_all()
        bpy.ops.wm.save_as_mainfile(filepath=out, compress=True, copy=True)
    else:
        raise ValueError(f"unknown format {fmt!r}")
    return [f for f in files if os.path.isfile(f)]


def frame_when_ready(objects):
    """Shows the textured asset, framed, once the window has drawn its 3D view."""

    def frame():
        for win in bpy.context.window_manager.windows:
            for area in win.screen.areas:
                if area.type != "VIEW_3D":
                    continue
                area.spaces.active.shading.type = "SOLID"
                area.spaces.active.shading.color_type = "TEXTURE"
                region = next((r for r in area.regions if r.type == "WINDOW"), None)
                if region is None:
                    continue
                with bpy.context.temp_override(window=win, area=area, region=region):
                    bpy.ops.view3d.view_selected()
        return None

    bpy.ops.object.select_all(action="DESELECT")
    for o in objects:
        if o.type == "MESH" and lod_level(o) == 0:
            o.select_set(True)
            bpy.context.view_layer.objects.active = o
    bpy.app.timers.register(frame, first_interval=0.3)


def report(**kv):
    print("FORJA " + json.dumps(kv), flush=True)


def tell(message):
    """Shows an error in Blender's window, where the person who asked is looking."""

    def popup():
        def draw(menu, _context):
            for line in message.splitlines()[:12]:
                menu.layout.label(text=line)

        bpy.context.window_manager.popup_menu(draw, title="Forja could not open the asset", icon="ERROR")
        return None

    bpy.app.timers.register(popup, first_interval=0.5)


def main():
    arg = sys.argv[sys.argv.index("--") + 1]
    if arg.startswith("{"):
        job = json.loads(arg)
    else:
        with open(arg, encoding="utf-8") as f:
            job = json.load(f)
    if job["mode"] == "open":
        try:
            reset()
            objects = []
            for item in job["items"]:
                objects += import_glb(item["glb"])
            hide_lower_lods(objects)
            frame_when_ready(objects)
        except Exception as e:
            traceback.print_exc()
            tell(f"{type(e).__name__}: {e}")
        return
    for i, item in enumerate(job["items"]):
        try:
            reset()
            import_glb(item["glb"])
            report(index=i, files=export(job["format"], item["out"]))
        except Exception as e:  # one asset that fails leaves the others to export
            traceback.print_exc()
            report(index=i, error=f"{type(e).__name__}: {e}")


main()
