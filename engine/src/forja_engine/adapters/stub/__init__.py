"""Deterministic CPU stand-ins for real models.

They exercise the full pipeline (workers, scheduler, cache, viewer) on any machine and in CI.
Same inputs and seed always produce byte-identical outputs.
"""

from __future__ import annotations

import numpy as np
import trimesh
from numpy.typing import NDArray
from PIL import Image

from forja_engine.ops import image as img_ops
from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import (
    Adapter,
    ArtifactKind,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

MB = 1 << 20


class StubBackground(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.BACKGROUND_REMOVAL,), devices=(Device.CPU,), vram_bytes=200 * MB)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        img = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        if not img_ops.has_real_alpha(img):
            img = img_ops.border_key(img, float(ctx.params.get("tolerance", 0.12)))
        out = ctx.out_dir / "rgba.png"
        img.save(out)
        return [StageOutput(out, ArtifactKind.IMAGE_PNG)]


class StubShape(Adapter):
    """Inflates the subject's silhouette into a closed, pillow-like mesh."""

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CPU,), vram_bytes=2048 * MB)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        res = int(ctx.params.get("resolution", 96))
        depth = float(ctx.params.get("depth", 0.18))
        img = Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path).convert("RGBA")
        mask = np.asarray(img.getchannel("A").resize((res, res), Image.Resampling.BILINEAR)) > 127
        ctx.progress(1, 4)
        ctx.check_cancel()
        dist = _distance(mask)
        height = np.sqrt(dist / max(1.0, float(np.max(dist)))) * depth
        ctx.progress(2, 4)
        ctx.check_cancel()
        mesh = _inflate(mask, height)
        ctx.progress(3, 4)
        trimesh.smoothing.filter_taubin(mesh, iterations=int(ctx.params.get("smooth", 8)))
        mesh.fix_normals()
        ctx.progress(4, 4)
        return [
            StageOutput(mesh_ops.save(mesh, ctx.out_dir / "shape.glb"), ArtifactKind.MESH_GLB, preview=True)
        ]


def _distance(mask: NDArray[np.bool_]) -> NDArray[np.float64]:
    """4-neighbour chamfer distance to the outside, by repeated erosion."""
    dist = np.zeros(mask.shape, dtype=np.float64)
    current = mask.copy()
    while current.any():
        dist[current] += 1
        padded = np.pad(current, 1, constant_values=False)
        current = (
            padded[1:-1, 1:-1] & padded[:-2, 1:-1] & padded[2:, 1:-1] & padded[1:-1, :-2] & padded[1:-1, 2:]
        )
    return dist


def _inflate(mask: NDArray[np.bool_], cell_h: NDArray[np.float64]) -> trimesh.Trimesh:
    n = mask.shape[0]
    padded_mask = np.pad(mask, 1, constant_values=False)
    padded_h = np.pad(cell_h, 1)
    # Each corner (r, c) touches cells (r-1..r, c-1..c) in unpadded terms.
    quad = [padded_mask[r : r + n + 1, c : c + n + 1] for r in (0, 1) for c in (0, 1)]
    quad_h = [padded_h[r : r + n + 1, c : c + n + 1] for r in (0, 1) for c in (0, 1)]
    interior = quad[0] & quad[1] & quad[2] & quad[3]
    corner_h = np.where(interior, sum(quad_h) / 4.0, 0.0)

    rows, cols = np.mgrid[0 : n + 1, 0 : n + 1]
    x = cols / n - 0.5
    y = 0.5 - rows / n
    front = np.stack([x, y, corner_h], axis=-1).reshape(-1, 3)
    back = np.stack([x, y, -corner_h], axis=-1).reshape(-1, 3)
    verts = np.concatenate([front, back])

    r, c = np.nonzero(mask)
    idx = lambda rr, cc: rr * (n + 1) + cc  # noqa: E731
    c00, c01, c10, c11 = idx(r, c), idx(r, c + 1), idx(r + 1, c), idx(r + 1, c + 1)
    off = (n + 1) ** 2
    faces = np.concatenate(
        [
            np.stack([c00, c10, c11], axis=1),
            np.stack([c00, c11, c01], axis=1),
            np.stack([c00 + off, c11 + off, c10 + off], axis=1),
            np.stack([c00 + off, c01 + off, c11 + off], axis=1),
        ]
    )
    mesh = trimesh.Trimesh(verts, faces, process=True)  # merges front/back at the zero-height rim
    mesh.remove_unreferenced_vertices()
    return mesh


class StubPaint(Adapter):
    """Projects the reference image from the front onto the mesh and bakes it into a texture.

    Outputs base color only, with constant roughness and metallic (decision 6 in docs/decisions.md).
    """

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.TEXTURE,), devices=(Device.CPU,), vram_bytes=6144 * MB)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        img = Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path).convert("RGBA")
        size = int(ctx.params.get("resolution", 1024))
        x0, y0, x1, y1 = img_ops.alpha_bbox(img)
        lo, hi = mesh.bounds[0], mesh.bounds[1]
        span = np.maximum(hi - lo, 1e-9)
        u = (x0 + (mesh.vertices[:, 0] - lo[0]) / span[0] * (x1 - x0)) / img.width
        v = (y0 + (hi[1] - mesh.vertices[:, 1]) / span[1] * (y1 - y0)) / img.height
        colors = img_ops.sample(img, np.stack([u, v], axis=1))
        ctx.progress(1, 2)
        ctx.check_cancel()
        tex = mesh_ops.rasterize_vertex_colors(mesh_ops.uvs_of(mesh), mesh.faces, colors, size)
        out = mesh_ops.textured(
            mesh, tex, float(ctx.params.get("roughness", 0.8)), float(ctx.params.get("metallic", 0.0))
        )
        ctx.progress(2, 2)
        return [StageOutput(mesh_ops.save(out, ctx.out_dir / "textured.glb"), ArtifactKind.MESH_GLB)]
