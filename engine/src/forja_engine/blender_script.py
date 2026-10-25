"""Runs inside Blender (blender --background --factory-startup --python this -- job.json).

Never imported by the engine. Meshes come and go as .npy arrays rather than through glTF, so
no importer merges, splits or turns them. Prints one line, "FORJA <json>": the result or an error.
"""

import json
import math
import sys

import bmesh  # type: ignore[import-not-found]
import bpy  # type: ignore[import-not-found]
import numpy as np


def build(vertices, faces, validate=True):
    me = bpy.data.meshes.new("forja")
    me.vertices.add(len(vertices))
    me.vertices.foreach_set("co", vertices.astype(np.float32).ravel())
    me.loops.add(faces.size)
    me.loops.foreach_set("vertex_index", faces.astype(np.int32).ravel())
    me.polygons.add(len(faces))
    me.polygons.foreach_set("loop_start", np.arange(0, faces.size, 3, dtype=np.int32))
    me.update(calc_edges=True)
    if validate:
        me.validate()
    ob = bpy.data.objects.new("forja", me)
    bpy.context.scene.collection.objects.link(ob)
    bpy.context.view_layer.objects.active = ob
    ob.select_set(True)
    return ob


def triangles(me):
    bm = bmesh.new()
    bm.from_mesh(me)
    bmesh.ops.triangulate(bm, faces=bm.faces[:], quad_method="BEAUTY", ngon_method="BEAUTY")
    v = np.array([x.co[:] for x in bm.verts], dtype=np.float32).reshape(-1, 3)
    bm.verts.index_update()
    f = np.array([[x.index for x in face.verts] for face in bm.faces], dtype=np.int32).reshape(-1, 3)
    bm.free()
    return v, f


# QuadriFlow refuses a mesh with any edge whose ends are within 1e-4 of each other on every axis,
# which a voxel rebuild leaves here and there (two in 350,000 faces is enough). The engine scales
# meshes to 100 units, so merging closer vertices changes nothing anyone could see.
TIDY = 2e-4


def tidy(ob):
    bm = bmesh.new()
    bm.from_mesh(ob.data)
    bmesh.ops.remove_doubles(bm, verts=bm.verts, dist=TIDY)
    bmesh.ops.dissolve_degenerate(bm, edges=bm.edges, dist=TIDY)
    bm.to_mesh(ob.data)
    bm.free()
    ob.data.update()


def quadriflow(ob, quads, seed, sharp):
    tidy(ob)
    try:
        res = bpy.ops.object.quadriflow_remesh(
            mode="FACES",
            target_faces=max(4, int(quads)),
            seed=seed,
            use_preserve_sharp=sharp,
            use_preserve_boundary=False,
            use_mesh_symmetry=False,
            preserve_attributes=False,
            smooth_normals=False,
        )
    except RuntimeError:
        # Its solver fails outright on some face counts ("Remeshing failed") where a nearby count
        # works, so this is a refusal like any other: the caller tries otherwise or keeps a run.
        return False
    return "FINISHED" in res


def remesh(job):
    ob = build(np.load(job["vertices"]), np.load(job["faces"]))
    want = int(job["quads"])
    base = ob.data.copy()
    voxel = False
    if not quadriflow(ob, want, job["seed"], job["sharp"]):
        # QuadriFlow takes only closed, consistently wound meshes: rebuild the surface from voxels
        # a quarter of a quad's edge wide, which closes holes and drops inner shells. A coarser
        # rebuild is tried before giving up: it loses detail the bake brings back.
        area = np.zeros(len(ob.data.polygons), dtype=np.float32)
        ob.data.polygons.foreach_get("area", area)
        source = ob.data.copy()
        edge = math.sqrt(float(area.sum()) / want)
        for fraction in (4, 2, 1):
            ob.data = source.copy()
            ob.data.remesh_voxel_size = edge / fraction
            bpy.ops.object.voxel_remesh()
            base = ob.data.copy()
            voxel = True
            if quadriflow(ob, want, job["seed"], job["sharp"]):
                break
        else:
            return {
                "error": "QuadriFlow could not remesh this shape, even rebuilt from voxels; "
                "turn off Quad remesh for this image to use the usual simplification"
            }
    # QuadriFlow lands near, not on, the face count it is asked for: aim once more from what it made.
    runs = []
    ask = want
    for attempt in range(int(job.get("attempts", 3))):
        if attempt:
            ob.data = base.copy()
            if not quadriflow(ob, ask, job["seed"], job["sharp"]):
                break
        got = len(ob.data.polygons)
        runs.append((got, ob.data.copy()))
        if 0.9 * want <= got <= want:
            break
        ask = max(4, int(0.97 * want * ask / max(got, 1)))
    # The fullest run within the budget, else the smallest (the engine then trims it).
    under = [r for r in runs if r[0] <= want]
    best = max(under, key=lambda r: r[0]) if under else min(runs, key=lambda r: r[0])
    ob.data = best[1]
    v, f = triangles(ob.data)
    np.save(job["out_vertices"], v)
    np.save(job["out_faces"], f)
    return {"quads": best[0], "voxel": voxel}


def smart_uv(job):
    # Not validated: faces must stay one to one with the engine's, whose UVs these become.
    ob = build(np.load(job["vertices"]), np.load(job["faces"]), validate=False)
    bpy.ops.object.mode_set(mode="EDIT")
    bpy.ops.mesh.select_all(action="SELECT")
    margin = float(job["margin"])
    bpy.ops.uv.smart_project(
        angle_limit=math.radians(float(job["angle_limit"])),
        island_margin=margin,
        area_weight=0.0,
        correct_aspect=True,
        scale_to_bounds=False,
    )
    bpy.ops.uv.pack_islands(rotate=True, margin_method="FRACTION", margin=margin)
    bpy.ops.object.mode_set(mode="OBJECT")
    me = ob.data
    uv = np.zeros(len(me.loops) * 2, dtype=np.float32)
    me.uv_layers.active.data.foreach_get("uv", uv)
    np.save(job["out_uv"], uv.reshape(-1, 3, 2))
    return {}


def main():
    with open(sys.argv[sys.argv.index("--") + 1], encoding="utf-8") as f:
        job = json.load(f)
    bpy.ops.wm.read_factory_settings(use_empty=True)
    try:
        result = {"remesh": remesh, "smart_uv": smart_uv}[job["op"]](job)
    except Exception as e:  # reported to the engine, which fails the stage with it
        result = {"error": f"{type(e).__name__}: {e}"}
    print("FORJA " + json.dumps(result), flush=True)


main()
