"""Mesh operations Blender does for the engine: QuadriFlow quad remesh and Smart UV Project."""

from __future__ import annotations

import tempfile
from pathlib import Path

import numpy as np
import trimesh

from forja_engine import blender
from forja_engine.ops import mesh as mesh_ops

# QuadriFlow refuses edges under 1e-4 units as zero length; at this size that is 1e-6 of the model.
_WORK_SIZE = 100.0


def quad_remesh(
    mesh: trimesh.Trimesh, target_tris: int, seed: int = 0, preserve_sharp: bool = False, timeout: float = 900
) -> trimesh.Trimesh:
    """An even, all-quad surface of at most target_tris triangles once split, by QuadriFlow.

    Inputs much denser than the result are decimated first: QuadriFlow's time follows its input,
    and the bake recovers the detail from the high-poly anyway.
    """
    quads = max(4, target_tris // 2)
    if len(mesh.faces) > 16 * target_tris:
        mesh = mesh_ops.decimate(mesh, 16 * target_tris)
    center = mesh.bounds.mean(axis=0)
    scale = _WORK_SIZE / max(float(np.linalg.norm(mesh.extents)), 1e-12)
    with tempfile.TemporaryDirectory(prefix="forja-remesh-") as tmp:
        d = Path(tmp)
        np.save(d / "v.npy", ((np.asarray(mesh.vertices) - center) * scale).astype(np.float32))
        np.save(d / "f.npy", np.asarray(mesh.faces, dtype=np.int32))
        blender.run(
            {
                "op": "remesh",
                "vertices": str(d / "v.npy"),
                "faces": str(d / "f.npy"),
                "quads": quads,
                "seed": int(seed) % (1 << 31),
                "sharp": bool(preserve_sharp),
                "out_vertices": str(d / "ov.npy"),
                "out_faces": str(d / "of.npy"),
            },
            d,
            timeout,
        )
        v = np.load(d / "ov.npy").astype(np.float64) / scale + center
        f = np.load(d / "of.npy").astype(np.int64)
    out = trimesh.Trimesh(v, f, process=True)
    out.update_faces(out.nondegenerate_faces())
    out.remove_unreferenced_vertices()
    trimesh.repair.fix_normals(out)
    if len(out.faces) > target_tris:
        out = mesh_ops.decimate(out, target_tris)  # rare: QuadriFlow overshot on every try
    return out


def smart_uv(
    mesh: trimesh.Trimesh, angle_limit: float = 66.0, margin: float = 0.004, timeout: float = 600
) -> trimesh.Trimesh:
    """UVs from Blender's Smart UV Project: islands cut where faces meet at more than angle_limit
    degrees, packed with `margin` (a fraction of the texture) between them. Vertices split along
    the new seams; normals stay welded across them."""
    faces = np.asarray(mesh.faces, dtype=np.int64)
    good = (faces[:, 0] != faces[:, 1]) & (faces[:, 1] != faces[:, 2]) & (faces[:, 0] != faces[:, 2])
    with tempfile.TemporaryDirectory(prefix="forja-uv-") as tmp:
        d = Path(tmp)
        np.save(d / "v.npy", np.asarray(mesh.vertices, dtype=np.float32))
        np.save(d / "f.npy", faces[good].astype(np.int32))
        blender.run(
            {
                "op": "smart_uv",
                "vertices": str(d / "v.npy"),
                "faces": str(d / "f.npy"),
                "angle_limit": float(angle_limit),
                "margin": float(margin),
                "out_uv": str(d / "uv.npy"),
            },
            d,
            timeout,
        )
        corner_uv = np.load(d / "uv.npy").astype(np.float64)
    faces = faces[good]
    # One vertex per (vertex, UV) pair: corners that share both stay one vertex.
    key = np.hstack([faces.reshape(-1, 1).astype(np.float64), corner_uv.reshape(-1, 2)])
    unique, inverse = np.unique(key, axis=0, return_inverse=True)
    out = trimesh.Trimesh(
        np.asarray(mesh.vertices)[unique[:, 0].astype(np.int64)], inverse.reshape(-1, 3), process=False
    )
    out.visual = trimesh.visual.TextureVisuals(uv=unique[:, 1:])
    return mesh_ops.weld_normals(out)
