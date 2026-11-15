"""LODs of a UV-unwrapped mesh: closed along UV seams, and still carrying the right texture."""

import time
from pathlib import Path

import fast_simplification
import numpy as np
import trimesh
from PIL import Image
from scipy.spatial import cKDTree

from forja_engine.adapters.builtin import Lods
from forja_engine.ops import lods as lods_ops
from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType

RATIOS = [0.5, 0.25]
TEX = 256


def ctx(tmp_path: Path, path: Path, **params) -> StageContext:
    out = tmp_path / "lods"
    out.mkdir(exist_ok=True)
    inputs = [StageInput(path, ArtifactKind.MESH_GLB, "")]
    return StageContext("s", StageType.MESH_LODS, inputs, params, 7, out)


def position_textured() -> tuple[trimesh.Trimesh, Image.Image]:
    """An unwrapped sphere whose texture colors encode the 3D position they sit at."""
    sphere = mesh_ops.unwrap(trimesh.creation.icosphere(subdivisions=4))
    uv = mesh_ops.uvs_of(sphere)
    tex = mesh_ops.rasterize_vertex_colors(uv, sphere.faces, (sphere.vertices + 1) / 2, TEX)
    material = trimesh.visual.material.PBRMaterial(baseColorTexture=tex)
    sphere.visual = trimesh.visual.TextureVisuals(uv=uv, material=material)
    return sphere, tex


def run_levels(tmp_path: Path, mesh: trimesh.Trimesh) -> list[trimesh.Trimesh]:
    src = mesh_ops.save(mesh, tmp_path / "in.glb")
    out = Lods().run(ctx(tmp_path, src, ratios=RATIOS, name="t"))[0].path
    scene = trimesh.load(out, file_type="glb", process=False)
    return [scene.geometry[f"t_LOD{i}"] for i in range(len(RATIOS) + 1)]


def old_level(lod0: trimesh.Trimesh, target: int) -> trimesh.Trimesh:
    """LODs as built before the UV islands were kept: simplify the split vertices as they are."""
    verts, tris = fast_simplification.simplify(
        np.asarray(lod0.vertices, dtype=np.float32),
        np.asarray(lod0.faces, dtype=np.int32),
        1.0 - target / len(lod0.faces),
    )
    lod = trimesh.Trimesh(verts, tris, process=True)
    _, tri = cKDTree(lod0.triangles_center).query(lod.vertices)
    bary = np.clip(trimesh.triangles.points_to_barycentric(lod0.triangles[tri], lod.vertices), 0, 1)
    bary /= np.maximum(bary.sum(axis=1, keepdims=True), 1e-9)
    uv = np.einsum("ij,ijk->ik", bary, mesh_ops.uvs_of(lod0)[lod0.faces[tri]])
    lod.visual = trimesh.visual.TextureVisuals(uv=uv)
    return lod


def color_error(lod: trimesh.Trimesh, tex: Image.Image) -> float:
    """Mean distance between the texture sampled at each face's centre and the position there."""
    uv = mesh_ops.uvs_of(lod)[lod.faces].mean(axis=1)
    px = np.clip(np.rint(uv * (TEX - 1)).astype(int), 0, TEX - 1)
    sampled = np.asarray(tex, dtype=np.float64)[TEX - 1 - px[:, 1], px[:, 0]] / 255
    expected = (lod.triangles_center + 1) / 2
    return float(np.linalg.norm(sampled - expected, axis=1).mean())


def test_welding_keeps_face_order_so_a_face_index_names_the_same_lod0_face():
    sphere, _ = position_textured()
    welded = lods_ops.Source(sphere).welded
    assert len(welded.vertices) < len(sphere.vertices)  # the seams were closed
    assert welded.faces.shape == sphere.faces.shape
    assert np.allclose(welded.vertices[welded.faces], sphere.vertices[sphere.faces])


def test_levels_stay_closed_along_uv_seams(tmp_path):
    sphere, _ = position_textured()
    assert mesh_ops.open_edges(sphere) == 0
    levels = run_levels(tmp_path, sphere)
    for ratio, lod in zip(RATIOS, levels[1:], strict=True):
        assert mesh_ops.open_edges(lod) == 0
        assert 0 < len(lod.faces) <= int(len(sphere.faces) * ratio)
        uv = mesh_ops.uvs_of(lod)
        assert uv.min() >= 0 and uv.max() <= 1
        positions = trimesh.Trimesh(lod.vertices, lod.faces, process=True)
        assert len(lod.vertices) > len(positions.vertices)  # seam vertices stay split


def test_the_old_construction_cracked_along_seams():
    sphere, _ = position_textured()
    assert mesh_ops.open_edges(old_level(sphere, len(sphere.faces) // 2)) > 0
    level = lods_ops.Source(sphere).level(len(sphere.faces) // 2)
    assert mesh_ops.open_edges(level.mesh) == 0


def test_every_level_samples_the_texture_where_lod0_does(tmp_path):
    sphere, tex = position_textured()
    levels = run_levels(tmp_path, sphere)
    base = color_error(levels[0], tex)
    assert base < 0.02
    for ratio, lod in zip(RATIOS, levels[1:], strict=True):
        old = color_error(old_level(sphere, int(len(sphere.faces) * ratio)), tex)
        new = color_error(lod, tex)
        # Geometry shrinks inside the sphere with each level, which alone costs some error.
        assert new < 0.02
        assert new <= old


def test_copies_of_a_vertex_share_position_and_normal_but_not_uv():
    sphere, _ = position_textured()
    mesh = lods_ops.Source(sphere).level(len(sphere.faces) // 4).mesh
    _, group = np.unique(np.round(mesh.vertices, 6), axis=0, return_inverse=True)
    group = group.ravel()
    uv = mesh_ops.uvs_of(mesh)
    seam = np.bincount(group)[group] > 1
    assert seam.any()
    for g in np.unique(group[seam]):
        assert np.ptp(mesh.vertex_normals[group == g], axis=0).max() < 1e-6
    assert max(np.ptp(uv[group == g], axis=0).max() for g in np.unique(group[seam])) > 1e-3


def test_a_level_without_a_texture_island_per_vertex_still_works():
    # A mesh unwrapped into one island has no seam copies at all.
    box = trimesh.creation.box().subdivide()
    box.visual = trimesh.visual.TextureVisuals(uv=np.random.default_rng(0).random((len(box.vertices), 2)))
    level = lods_ops.Source(box).level(len(box.faces) // 2)
    assert len(level.mesh.vertices) == len(level.tris) == len(level.bary)
    assert np.allclose(level.bary.sum(axis=1), 1)


def test_building_levels_of_a_30k_triangle_mesh_is_fast():
    sphere = mesh_ops.unwrap(trimesh.creation.uv_sphere(count=[90, 90]))
    assert len(sphere.faces) >= 30_000
    start = time.perf_counter()
    source = lods_ops.Source(sphere)
    for ratio in RATIOS:
        source.level(int(len(sphere.faces) * ratio))
    # About 0.5 s measured; the bound only catches a Python loop per face or vertex.
    assert time.perf_counter() - start < 5
