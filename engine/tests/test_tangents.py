import json
import subprocess

import numpy as np
import pytest
import trimesh

from forja_engine import blender
from forja_engine.ops import mesh as m
from forja_engine.ops import tangents

needs_blender = pytest.mark.skipif(blender.problem() is not None, reason="Blender 4.2+ is not installed")


def mirrored_quads() -> trimesh.Trimesh:
    """Two quads side by side; the right one's UVs are mirrored in u, and the seam vertices have
    the same position, normal and UV on both sides, so MikkTSpace welds them."""
    v = np.array(
        [[0, 0, 0], [1, 0, 0], [1, 1, 0], [0, 1, 0], [2, 0, 0], [2, 1, 0], [1, 0, 0], [1, 1, 0]], float
    )
    uv = np.array([[0, 0], [0.5, 0], [0.5, 1], [0, 1], [0, 0], [0, 1], [0.5, 0], [0.5, 1]], float)
    q = trimesh.Trimesh(v, [[0, 1, 2], [0, 2, 3], [6, 4, 5], [6, 5, 7]], process=False)
    q.visual = trimesh.visual.TextureVisuals(uv=uv)
    return m.weld_normals(q)


def test_mirrored_uvs_flip_the_bitangent_and_keep_the_tangent_along_u():
    q = mirrored_quads()
    t = tangents.mikktspace(q.vertices, q.vertex_normals, m.uvs_of(q), q.faces)
    assert np.allclose(t[[0, 1, 2, 3]], [1, 0, 0, 1], atol=1e-6)  # u runs along +x
    assert np.allclose(t[[4, 5, 6, 7]], [-1, 0, 0, -1], atol=1e-6)  # u runs along -x, mirrored


def test_corners_of_one_vertex_in_two_charts_split_into_two_vertices():
    # Two triangles share vertex 0 (same position, normal and UV) but no edge: a bowtie in UV space,
    # each wing laid out along a different direction.
    v = np.array([[0, 0, 0], [1, 0, 0], [1, 1, 0], [-1, 0, 0], [-1, -1, 0]], float)
    uv = np.array([[0.5, 0.5], [0.9, 0.5], [0.9, 0.9], [0.5, 0.1], [0.1, 0.1]], float)
    mesh = trimesh.Trimesh(v, [[0, 1, 2], [0, 3, 4]], process=False)
    mesh.vertex_normals = np.tile([0.0, 0.0, 1.0], (5, 1))
    corner, groups = tangents.corner_tangents(mesh.vertices, mesh.vertex_normals, uv, mesh.faces)
    assert groups[0, 0] != groups[1, 0]
    src, faces, tan = tangents.split(mesh.faces, corner, groups)
    assert len(src) == 6 and sorted(src.tolist()).count(0) == 2
    a, b = faces[0, 0], faces[1, 0]
    assert a != b and not np.allclose(tan[a], tan[b])


def test_tangents_are_unit_and_finite_for_degenerate_input():
    mesh = m.unwrap(trimesh.creation.box())
    t = m.vertex_tangents(mesh, np.zeros((len(mesh.vertices), 2)))  # no UV area anywhere
    assert np.isfinite(t).all() and np.allclose(np.linalg.norm(t[:, :3], axis=1), 1, atol=1e-5)


_DUMP = """
import bpy, json, sys
glb, out = sys.argv[sys.argv.index("--") + 1:]
bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=glb)
me = next(o for o in bpy.context.scene.objects if o.type == "MESH").data
me.calc_tangents()
rows = [[p.index, k, list(me.loops[li].tangent), me.loops[li].bitangent_sign]
        for p in me.polygons for k, li in enumerate(p.loop_indices)]
json.dump(rows, open(out, "w"))
"""


@needs_blender
def test_tangents_match_blenders_mikktspace(tmp_path):
    """Blender recomputes MikkTSpace on import, as Unreal and Unity do for FBX: same frames."""
    rng = np.random.default_rng(1)
    ico = trimesh.creation.icosphere(subdivisions=3)
    ico.vertices += rng.normal(0, 0.01, ico.vertices.shape)
    for name, mesh in (("ico", m.unwrap(ico)), ("mirrored", mirrored_quads())):
        corner, groups = tangents.corner_tangents(
            mesh.vertices, mesh.vertex_normals, m.uvs_of(mesh), mesh.faces
        )
        src, faces, tan = tangents.split(mesh.faces, corner, groups)
        out = trimesh.Trimesh(mesh.vertices[src], faces, process=False)
        out.visual = trimesh.visual.TextureVisuals(uv=m.uvs_of(mesh)[src])
        out.vertex_normals = np.asarray(mesh.vertex_normals)[src]
        glb = m.save(out, tmp_path / f"{name}.glb")
        script = tmp_path / "dump.py"
        script.write_text(_DUMP)
        dump = tmp_path / f"{name}.json"
        subprocess.run(
            [
                blender.find().path,
                "--background",
                "--factory-startup",
                "--python",
                str(script),
                "--",
                str(glb),
                str(dump),
            ],
            check=True,
            capture_output=True,
            timeout=300,
        )
        for poly, k, t, sign in json.loads(dump.read_text()):
            ours = tan[faces[poly, k]]
            theirs = np.array([t[0], t[2], -t[1]])  # Blender is Z-up, glTF Y-up
            theirs /= np.linalg.norm(theirs)
            assert np.degrees(np.arccos(np.clip(ours[:3] @ theirs, -1, 1))) < 0.2, name
            assert np.sign(ours[3]) == np.sign(sign), name
