"""Mesh primitives shared by built-in stages and stub models. CPU only, numpy vectorized."""

from __future__ import annotations

from pathlib import Path

import fast_simplification
import numpy as np
import trimesh
import xatlas
from numpy.typing import NDArray
from PIL import Image


def load(path: Path) -> trimesh.Trimesh:
    mesh = trimesh.load(path, file_type="glb", force="mesh", process=False)
    if not isinstance(mesh, trimesh.Trimesh):
        raise ValueError(f"{path} does not contain a triangle mesh")
    return mesh


def save(mesh: trimesh.Trimesh | trimesh.Scene, path: Path) -> Path:
    path.write_bytes(trimesh.exchange.gltf.export_glb(_as_scene(mesh)))
    return path


def _as_scene(mesh: trimesh.Trimesh | trimesh.Scene) -> trimesh.Scene:
    return mesh if isinstance(mesh, trimesh.Scene) else trimesh.Scene(mesh)


def cleanup(mesh: trimesh.Trimesh, min_component_ratio: float = 0.01) -> trimesh.Trimesh:
    """Drop floaters, merge duplicate vertices, drop degenerate faces, make normals consistent."""
    mesh = mesh.copy()
    mesh.merge_vertices()
    mesh.update_faces(mesh.nondegenerate_faces())
    mesh.remove_unreferenced_vertices()
    parts = mesh.split(only_watertight=False)
    if len(parts) > 1:
        areas = np.array([p.area for p in parts])
        keep = [p for p, a in zip(parts, areas, strict=True) if a >= areas.max() * min_component_ratio]
        mesh = trimesh.util.concatenate(keep)
    trimesh.repair.fix_normals(mesh)
    return mesh


def decimate(mesh: trimesh.Trimesh, target_tris: int) -> trimesh.Trimesh:
    faces = len(mesh.faces)
    if faces <= target_tris:
        return mesh.copy()
    reduction = 1.0 - target_tris / faces
    verts, tris = fast_simplification.simplify(
        np.asarray(mesh.vertices, dtype=np.float32), np.asarray(mesh.faces, dtype=np.int32), reduction
    )
    return trimesh.Trimesh(verts, tris, process=True)


def unwrap(mesh: trimesh.Trimesh) -> trimesh.Trimesh:
    """xatlas UV unwrap. Returns a mesh whose vertices are split along seams, with UVs attached."""
    vmapping, indices, uvs = xatlas.parametrize(
        np.asarray(mesh.vertices, dtype=np.float32), np.asarray(mesh.faces, dtype=np.uint32)
    )
    out = trimesh.Trimesh(mesh.vertices[vmapping], indices, process=False)
    out.visual = trimesh.visual.TextureVisuals(uv=uvs)
    return out


def uvs_of(mesh: trimesh.Trimesh) -> NDArray[np.float64]:
    uv = getattr(mesh.visual, "uv", None)
    if uv is None:
        raise ValueError("mesh has no UVs; run the mesh.uv stage first")
    return np.asarray(uv, dtype=np.float64)


def rasterize_vertex_colors(
    uv: NDArray[np.float64], faces: NDArray[np.int64], colors: NDArray[np.float64], size: int
) -> Image.Image:
    """Bake per-vertex RGB (0..1) into a UV-space texture with barycentric interpolation.

    Vectorized per triangle bounding box; texels no triangle covers are filled by dilation so
    mip-mapping and bilinear filtering never bleed black into seams.
    """
    tex = np.zeros((size, size, 3), dtype=np.float32)
    covered = np.zeros((size, size), dtype=bool)
    px = uv * (size - 1)
    px[:, 1] = (size - 1) - px[:, 1]  # glTF UV origin is top-left after this flip
    tri = px[faces]
    col = colors[faces]
    lo = np.clip(np.floor(tri.min(axis=1)).astype(int), 0, size - 1)
    hi = np.clip(np.ceil(tri.max(axis=1)).astype(int), 0, size - 1)
    for t in range(len(faces)):
        (x0, y0), (x1, y1) = lo[t], hi[t]
        xs, ys = np.meshgrid(np.arange(x0, x1 + 1), np.arange(y0, y1 + 1))
        a, b, c = tri[t]
        det = (b[1] - c[1]) * (a[0] - c[0]) + (c[0] - b[0]) * (a[1] - c[1])
        if abs(det) < 1e-12:
            continue
        w0 = ((b[1] - c[1]) * (xs - c[0]) + (c[0] - b[0]) * (ys - c[1])) / det
        w1 = ((c[1] - a[1]) * (xs - c[0]) + (a[0] - c[0]) * (ys - c[1])) / det
        w2 = 1.0 - w0 - w1
        inside = (w0 >= -1e-3) & (w1 >= -1e-3) & (w2 >= -1e-3)
        if not inside.any():
            continue
        rgb = w0[..., None] * col[t, 0] + w1[..., None] * col[t, 1] + w2[..., None] * col[t, 2]
        tex[ys[inside], xs[inside]] = rgb[inside]
        covered[ys[inside], xs[inside]] = True
    tex = _dilate(tex, covered, iterations=4)
    return Image.fromarray((np.clip(tex, 0, 1) * 255).astype(np.uint8), "RGB")


def _dilate(tex: NDArray[np.float32], covered: NDArray[np.bool_], iterations: int) -> NDArray[np.float32]:
    for _ in range(iterations):
        grown = tex.copy()
        grown_mask = covered.copy()
        for dy, dx in ((0, 1), (0, -1), (1, 0), (-1, 0)):
            shifted = np.roll(np.roll(tex, dy, axis=0), dx, axis=1)
            shifted_mask = np.roll(np.roll(covered, dy, axis=0), dx, axis=1)
            fill = shifted_mask & ~grown_mask
            grown[fill] = shifted[fill]
            grown_mask |= fill
        tex, covered = grown, grown_mask
    return tex


def textured(
    mesh: trimesh.Trimesh, base_color: Image.Image, roughness: float, metallic: float
) -> trimesh.Trimesh:
    material = trimesh.visual.material.PBRMaterial(
        baseColorTexture=base_color, roughnessFactor=roughness, metallicFactor=metallic
    )
    out = mesh.copy()
    out.visual = trimesh.visual.TextureVisuals(uv=uvs_of(mesh), material=material)
    return out


def stats(mesh: trimesh.Trimesh) -> dict[str, float | int | bool]:
    return {
        "triangles": len(mesh.faces),
        "vertices": len(mesh.vertices),
        "watertight": bool(mesh.is_watertight),
        "extent_m": [round(float(v), 4) for v in mesh.extents],  # type: ignore[dict-item]
    }
