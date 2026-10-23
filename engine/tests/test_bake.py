import json
import struct

import numpy as np
import pytest
import trimesh
from PIL import Image

from forja_engine.ops import bake
from forja_engine.ops import mesh as m


def plane(n: int, size: float = 1.0) -> trimesh.Trimesh:
    """A grid in XY facing +Z, with UVs = XY, so image right = +X and image up = +Y."""
    g = np.linspace(0, size, n + 1)
    xs, ys = np.meshgrid(g, g)
    verts = np.stack([xs.ravel(), ys.ravel(), np.zeros(xs.size)], axis=1)
    idx = np.arange(verts.shape[0]).reshape(n + 1, n + 1)
    a, b, c, d = idx[:-1, :-1].ravel(), idx[:-1, 1:].ravel(), idx[1:, :-1].ravel(), idx[1:, 1:].ravel()
    faces = np.concatenate([np.stack([a, b, d], 1), np.stack([a, d, c], 1)])
    mesh = trimesh.Trimesh(verts, faces, process=False)
    mesh.visual = trimesh.visual.TextureVisuals(uv=verts[:, :2] / size)
    return mesh


def decode(img) -> np.ndarray:
    return np.asarray(img).astype(np.float64) / 255 * 2 - 1


def test_baking_a_mesh_onto_itself_is_flat():
    low = m.unwrap(trimesh.creation.icosphere(subdivisions=3))
    r = bake.bake(low, low, 128, ao_samples=4)
    n = decode(r.normal)
    assert r.hit_ratio > 0.99
    assert np.percentile(n[..., 2], 5) > 0.97  # (0.5, 0.5, 1): no detail to add


def test_tangent_space_follows_gltf_convention():
    """Tangent space must be +X = image right and +Y = image up (glTF), here equal to world X and Y."""
    low = plane(4)
    high = plane(64)
    high.vertices[:, 2] = 0.05 * (high.vertices[:, 0] + high.vertices[:, 1])  # rises toward +X and +Y
    high = trimesh.Trimesh(high.vertices, high.faces, process=False)
    r = bake.bake(low, high, 64, cage=0.2, ao_samples=4)
    n = decode(r.normal)[16:48, 16:48]
    # The surface rises toward +X and +Y, so its normal leans toward -X and -Y.
    assert n[..., 0].mean() < -0.03 and n[..., 1].mean() < -0.03
    assert abs(n[..., 0].mean() - n[..., 1].mean()) < 0.01


def test_ao_darkens_a_concave_corner():
    floor = trimesh.creation.box((2, 0.1, 2))
    wall = trimesh.creation.box((0.1, 2, 2))
    wall.apply_translation((-0.95, 1.0, 0))
    high = trimesh.util.concatenate([floor, wall])
    low = m.unwrap(high)
    r = bake.bake(low, high, 128, ao_samples=32, ao_radius=0.3)
    ao = np.asarray(r.orm)[..., 0]
    covered = ao[ao < 255]
    assert covered.min() < 140  # texels next to the corner see a quarter of the sky or less
    assert np.asarray(r.orm)[..., 1].min() == 255  # roughness channel passes the factor through


def test_glb_carries_normals_and_tangents(tmp_path):
    mesh = m.unwrap(trimesh.creation.icosphere(subdivisions=2))
    mesh.metadata[m.TANGENTS] = m.vertex_tangents(mesh, m.uvs_of(mesh))
    raw = m.save(mesh, tmp_path / "t.glb").read_bytes()
    json_len = struct.unpack_from("<I", raw, 12)[0]
    doc = json.loads(raw[20 : 20 + json_len])
    attrs = doc["meshes"][0]["primitives"][0]["attributes"]
    assert {"NORMAL", "TANGENT", "TEXCOORD_0"} <= set(attrs)
    acc = doc["accessors"][attrs["TANGENT"]]
    assert acc["type"] == "VEC4" and acc["count"] == len(mesh.vertices)
    view = doc["bufferViews"][acc["bufferView"]]
    start = 20 + json_len + 8 + view["byteOffset"]
    tan = np.frombuffer(raw[start : start + view["byteLength"]], dtype="<f4").reshape(-1, 4)
    assert np.allclose(np.linalg.norm(tan[:, :3], axis=1), 1, atol=1e-4)
    assert set(np.unique(tan[:, 3])) <= {-1.0, 1.0}
    assert len(trimesh.load(tmp_path / "t.glb", force="mesh").faces) == len(mesh.faces)  # still a valid GLB


def test_seam_normals_are_welded():
    mesh = m.unwrap(trimesh.creation.icosphere(subdivisions=3))
    _, inverse = trimesh.grouping.unique_rows(mesh.vertices, digits=6)
    normals = np.asarray(mesh.vertex_normals)
    for group in range(inverse.max() + 1):
        same = normals[inverse == group]
        assert np.allclose(same, same[0])
    assert np.allclose(np.linalg.norm(normals, axis=1), 1)


def test_bake_stage_writes_pbr_glb(tmp_path):
    from forja_engine.adapters.builtin import Bake
    from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType

    high = trimesh.creation.icosphere(subdivisions=5)
    low = m.decimate(high, 400)
    low = m.textured(m.unwrap(low), Image.new("RGB", (256, 256), (180, 120, 60)), 0.6, 0.1)
    paths = [m.save(low, tmp_path / "low.glb"), m.save(high, tmp_path / "high.glb")]
    ctx = StageContext(
        "bake", StageType.BAKE, [StageInput(p, ArtifactKind.MESH_GLB, "") for p in paths], {}, 0, tmp_path
    )
    out = m.load(Bake().run(ctx)[0].path)
    mat = out.visual.material
    assert mat.normalTexture.size == (256, 256) and mat.occlusionTexture.size == (256, 256)
    assert mat.roughnessFactor == pytest.approx(0.6) and mat.metallicFactor == pytest.approx(0.1)


def test_colors_of_a_textured_high_poly_are_baked_into_the_low_poly():
    """A model that does shape and texture in one pass hands bake a textured high-poly."""
    high = plane(16)
    tex = np.zeros((64, 64, 3), dtype=np.uint8)
    tex[:, :32] = (255, 0, 0)  # left half red (image left = -X)
    tex[:, 32:] = (0, 0, 255)  # right half blue
    high.visual = trimesh.visual.TextureVisuals(
        uv=np.asarray(high.visual.uv),
        material=trimesh.visual.material.PBRMaterial(baseColorTexture=Image.fromarray(tex)),
    )
    low = plane(2)
    r = bake.bake(low, high, 64, ao_samples=2, color=True)
    rgb = np.asarray(r.color)
    assert rgb[32, 8].tolist() == [255, 0, 0] and rgb[32, 56].tolist() == [0, 0, 255]


def test_vertex_colors_bake_too():
    high = trimesh.creation.icosphere(subdivisions=3)
    green = np.tile([0, 200, 0, 255], (len(high.vertices), 1))
    high.visual = trimesh.visual.ColorVisuals(high, vertex_colors=green)
    low = m.unwrap(trimesh.creation.icosphere(subdivisions=2))
    r = bake.bake(low, high, 64, ao_samples=2, color=True)
    assert r.color is not None and np.asarray(r.color)[..., 1].mean() > 150
    plain = trimesh.creation.icosphere(subdivisions=3)
    assert bake.bake(low, plain, 32, ao_samples=2, color=True).color is None


def test_bake_stage_unwraps_and_colors_an_unpainted_low_poly(tmp_path):
    from forja_engine.adapters.builtin import Bake
    from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType

    high = trimesh.creation.icosphere(subdivisions=4)
    blue = np.tile([10, 20, 230, 255], (len(high.vertices), 1))
    high.visual = trimesh.visual.ColorVisuals(high, vertex_colors=blue)
    low = trimesh.creation.icosphere(subdivisions=2)  # as decimated: no UVs, no texture
    paths = [m.save(low, tmp_path / "low.glb"), m.save(high, tmp_path / "high.glb")]
    ctx = StageContext(
        "bake",
        StageType.BAKE,
        [StageInput(p, ArtifactKind.MESH_GLB, "") for p in paths],
        {"resolution": 64, "ao_samples": 2},
        0,
        tmp_path,
    )
    base = m.load(Bake().run(ctx)[0].path).visual.material.baseColorTexture
    assert base is not None and np.asarray(base.convert("RGB"))[..., 2].mean() > 150
