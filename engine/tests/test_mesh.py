import json
import struct

import numpy as np
import trimesh

from forja_engine.ops import mesh as m


def glb_json(path) -> dict:
    """The JSON chunk, parsed as strictly as browsers and glTFast do: NaN is not JSON."""
    raw = path.read_bytes()
    n = struct.unpack_from("<I", raw, 12)[0]

    def refuse(token: str) -> None:
        raise ValueError(f"{token} is not valid JSON")

    return json.loads(raw[20 : 20 + n], parse_constant=refuse)


def test_tangents_go_only_into_the_tangent_attribute(tmp_path):
    mesh = m.unwrap(trimesh.creation.icosphere(subdivisions=2))
    mesh.metadata[m.TANGENTS] = m.vertex_tangents(mesh, m.uvs_of(mesh))
    doc = glb_json(m.save(mesh, tmp_path / "t.glb"))
    prim = doc["meshes"][0]["primitives"][0]
    assert "TANGENT" in prim["attributes"]
    assert m.TANGENTS not in doc["meshes"][0].get("extras", {})
    assert m.TANGENTS in mesh.metadata  # the caller's mesh keeps them


def test_a_vertex_without_a_normal_still_gets_a_finite_tangent():
    mesh = m.unwrap(trimesh.creation.box())
    # A sliver: three collinear vertices make a face with no area, hence no normal.
    v = np.vstack([mesh.vertices, [[2, 0, 0], [3, 0, 0], [4, 0, 0]]])
    uv = np.vstack([m.uvs_of(mesh), [[0.1, 0.1], [0.2, 0.1], [0.3, 0.1]]])
    faces = np.vstack([mesh.faces, [[len(v) - 3, len(v) - 2, len(v) - 1]]])
    sliver = trimesh.Trimesh(v, faces, process=False)
    t = m.vertex_tangents(sliver, uv)
    assert np.isfinite(t).all()
    assert np.allclose(np.linalg.norm(t[:, :3], axis=1), 1, atol=1e-5)


def test_decimation_leaves_no_zero_area_faces_or_normal_less_vertices():
    # Both inputs are ones where plain edge collapse left slivers or orphaned vertices behind.
    box = trimesh.creation.box().subdivide().subdivide().subdivide().subdivide()
    low = m.decimate(box, 1000)
    assert low.nondegenerate_faces().all()
    staff = trimesh.util.concatenate(
        [
            trimesh.creation.capsule(height=1, radius=0.3, count=[64, 64]),
            trimesh.creation.cylinder(radius=0.01, height=2, sections=48).subdivide(),
        ]
    )
    staff.vertices += np.random.default_rng(2).normal(0, 0.002, staff.vertices.shape)
    low = m.decimate(staff, 2000)
    assert len(np.unique(low.faces)) == len(low.vertices)
    # The thin cylinder folds into a two-sided sheet whose normals cancel: tangents must cope.
    assert np.isfinite(m.vertex_tangents(low, np.zeros((len(low.vertices), 2)))).all()


def test_loading_drops_tangents_older_files_kept_in_extras(tmp_path):
    mesh = trimesh.creation.box()
    mesh.metadata[m.TANGENTS] = np.zeros((len(mesh.vertices), 4)).tolist()
    path = tmp_path / "old.glb"
    path.write_bytes(trimesh.exchange.gltf.export_glb(trimesh.Scene(mesh)))  # the old writer path
    assert m.TANGENTS not in m.load(path).metadata


def test_cleanup_keeps_the_texture_of_a_mesh_with_floaters(tmp_path):
    """A hosted model's textured mesh has UV seams; cleanup must neither crash nor drop colors."""
    import trimesh
    from PIL import Image

    from forja_engine.ops import bake
    from forja_engine.ops import mesh as m

    body = m.unwrap(trimesh.creation.icosphere(subdivisions=3))
    floater = m.unwrap(trimesh.creation.icosphere(subdivisions=1, radius=0.05))
    floater.apply_translation([3, 0, 0])
    material = trimesh.visual.material.PBRMaterial(baseColorTexture=Image.new("RGB", (16, 16), (200, 30, 30)))
    for part in (body, floater):
        part.visual = trimesh.visual.TextureVisuals(uv=part.visual.uv, material=material)
    path = m.save(trimesh.util.concatenate([body, floater]), tmp_path / "in.glb")
    clean = m.cleanup(m.load(path))
    assert len(clean.faces) == len(body.faces)
    assert bake.has_color(clean)
