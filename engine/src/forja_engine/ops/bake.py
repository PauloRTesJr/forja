"""High-to-low baking: tangent-space normals, ambient occlusion and, on request, base color,
in the low-poly's UV space.

For every texel the low-poly covers, a ray starts just outside the low surface (a cage at
`cage` times the model's size) and travels inward along the low normal; the first hit on the
high-poly gives the detail normal. AO casts cosine-weighted hemisphere rays from that hit.

Ray casting uses Embree through trimesh when embreex is installed. Without it the normals
fall back to the nearest high-poly vertex and AO is left white, so a bake never fails for
lack of a native library.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass

import numpy as np
import trimesh
from numpy.typing import NDArray
from PIL import Image
from scipy.spatial import cKDTree

from forja_engine.ops import mesh as mesh_ops

log = logging.getLogger(__name__)
F32 = NDArray[np.float32]


@dataclass(frozen=True)
class BakeResult:
    normal: Image.Image  # RGB tangent-space normal map, OpenGL (+Y up) convention as glTF requires
    orm: Image.Image  # R = occlusion, G = B = 1 (roughness and metallic stay constant factors)
    tangents: F32  # per-vertex tangents of the low mesh, written into the GLB
    hit_ratio: float  # share of texels whose ray found the high-poly; low values mean a bad cage
    ray_engine: str
    # The high-poly's own colors in the low-poly's UVs, when asked for and the high-poly has any
    # (a textured mesh from a model that does shape and texture in one pass).
    color: Image.Image | None = None


@dataclass(frozen=True)
class Texels:
    """Surface samples for every texel a UV triangle covers."""

    ys: NDArray[np.int64]
    xs: NDArray[np.int64]
    position: NDArray[np.float64]
    normal: NDArray[np.float64]
    tangent: NDArray[np.float64]
    bitangent: NDArray[np.float64]


def raster_uv(uv: NDArray[np.float64], faces: NDArray[np.int64], size: int) -> tuple[NDArray[np.int64], F32]:
    """Triangle id (-1 where empty) and barycentrics for each texel of a size x size map."""
    tri_id = np.full((size, size), -1, dtype=np.int64)
    bary = np.zeros((size, size, 3), dtype=np.float32)
    px = uv * size - 0.5  # texel centers
    px[:, 1] = (size - 1) - px[:, 1]  # trimesh UVs are bottom-left; images are top-left
    tri = px[faces]
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
        inside = (w0 >= -1e-4) & (w1 >= -1e-4) & (w2 >= -1e-4)
        tri_id[ys[inside], xs[inside]] = t
        bary[ys[inside], xs[inside]] = np.stack([w0[inside], w1[inside], w2[inside]], axis=-1)
    return tri_id, bary


def _unit(v: NDArray[np.float64]) -> NDArray[np.float64]:
    return v / np.maximum(np.linalg.norm(v, axis=-1, keepdims=True), 1e-12)


def sample_texels(low: trimesh.Trimesh, uv: NDArray[np.float64], tangents: F32, size: int) -> Texels:
    tri_id, bary = raster_uv(uv, np.asarray(low.faces), size)
    ys, xs = np.nonzero(tri_id >= 0)
    corners = np.asarray(low.faces)[tri_id[ys, xs]]
    w = bary[ys, xs].astype(np.float64)[..., None]

    def interp(attr: NDArray[np.float64]) -> NDArray[np.float64]:
        return np.sum(attr[corners] * w, axis=1)

    normal = _unit(interp(np.asarray(low.vertex_normals, dtype=np.float64)))
    tangent = interp(tangents[:, :3].astype(np.float64))
    tangent = _unit(tangent - normal * np.sum(normal * tangent, axis=1, keepdims=True))
    handed = np.where(interp(tangents[:, 3:4].astype(np.float64)) < 0, -1.0, 1.0)
    return Texels(
        ys=ys,
        xs=xs,
        position=interp(np.asarray(low.vertices, dtype=np.float64)),
        normal=normal,
        tangent=tangent,
        bitangent=np.cross(normal, tangent) * handed,
    )


class _Rays:
    def __init__(self, high: trimesh.Trimesh) -> None:
        self.high = high
        self.vertex_normals = np.asarray(high.vertex_normals, dtype=np.float64)
        # Triangles and barycentrics of the latest first_hits, for sampling colors there.
        self.last: tuple[NDArray[np.int64], NDArray[np.float64]] = (np.zeros(0, np.int64), np.zeros((0, 3)))
        try:
            from trimesh.ray.ray_pyembree import RayMeshIntersector

            self.embree: RayMeshIntersector | None = RayMeshIntersector(high)
            self.engine = "embree"
        except ImportError:
            log.warning("embreex not installed: normals use nearest vertices and AO is skipped")
            self.embree = None
            self.engine = "nearest"
            self.tree = cKDTree(np.asarray(high.vertices))

    def first_hits(
        self, origins: NDArray[np.float64], dirs: NDArray[np.float64], max_dist: float
    ) -> tuple[NDArray[np.bool_], NDArray[np.float64], NDArray[np.float64]]:
        """(hit mask, hit points, smooth high-poly normals) for rays within max_dist.

        The hit triangles and barycentrics stay on self.last for sampling colors at the hits.
        """
        n = len(origins)
        hit = np.zeros(n, dtype=bool)
        points = np.zeros((n, 3))
        normals = np.zeros((n, 3))
        tris = np.zeros(n, dtype=np.int64)
        barys = np.zeros((n, 3))
        if self.embree is None:
            dist, idx = self.tree.query(origins + dirs * max_dist / 2)
            hit = dist < max_dist
            points[hit] = self.high.vertices[idx[hit]]
            normals[hit] = self.vertex_normals[idx[hit]]
            # The nearest vertex stands in for the hit: one of its faces, all weight on it.
            faces = np.asarray(self.high.faces)
            first_face = np.full(len(self.high.vertices), -1, dtype=np.int64)
            for corner in range(3):
                unset = first_face[faces[:, corner]] < 0
                first_face[faces[unset, corner]] = np.nonzero(unset)[0]
            tris = np.maximum(first_face[idx], 0)
            corner_of = np.argmax(faces[tris] == idx[:, None], axis=1)
            barys[np.arange(n), corner_of] = 1.0
            self.last = (tris, barys)
            return hit, points, normals
        loc, ray_idx, tri_idx = self.embree.intersects_location(origins, dirs, multiple_hits=False)
        close = np.linalg.norm(loc - origins[ray_idx], axis=1) <= max_dist
        loc, ray_idx, tri_idx = loc[close], ray_idx[close], tri_idx[close]
        bary = trimesh.triangles.points_to_barycentric(self.high.triangles[tri_idx], loc)
        corner_normals = self.vertex_normals[np.asarray(self.high.faces)[tri_idx]]
        hit[ray_idx] = True
        points[ray_idx] = loc
        normals[ray_idx] = _unit(np.einsum("ij,ijk->ik", bary, corner_normals))
        tris[ray_idx] = tri_idx
        barys[ray_idx] = bary
        self.last = (tris, barys)
        return hit, points, normals

    def occlusion(
        self,
        origins: NDArray[np.float64],
        normals: NDArray[np.float64],
        samples: int,
        radius: float,
        seed: int,
    ) -> NDArray[np.float64]:
        """Fraction of cosine-weighted hemisphere rays that reach farther than radius."""
        if self.embree is None or len(origins) == 0:
            return np.ones(len(origins))
        rng = np.random.default_rng(seed)
        # Stratified cosine-weighted directions, randomly rotated per texel to trade banding for noise.
        k = np.arange(samples)
        r = np.sqrt((k + 0.5) / samples)
        phi = k * np.pi * (3 - np.sqrt(5)) + rng.uniform(0, 2 * np.pi, size=(len(origins), 1))
        local = np.stack(
            [r * np.cos(phi), r * np.sin(phi), np.broadcast_to(np.sqrt(1 - r**2), phi.shape)], axis=-1
        )
        helper = np.where(np.abs(normals[:, :1]) < 0.9, [[1.0, 0, 0]], [[0, 1.0, 0]])
        t = _unit(np.cross(helper, normals))
        b = np.cross(normals, t)
        dirs = (
            local[..., 0:1] * t[:, None] + local[..., 1:2] * b[:, None] + local[..., 2:3] * normals[:, None]
        )
        starts = np.repeat(origins + normals * radius * 1e-3, samples, axis=0)
        flat_dirs = dirs.reshape(-1, 3)
        loc, ray_idx, _ = self.embree.intersects_location(starts, flat_dirs, multiple_hits=False)
        blocked = np.zeros(len(starts), dtype=bool)
        blocked[ray_idx[np.linalg.norm(loc - starts[ray_idx], axis=1) < radius]] = True
        return 1.0 - blocked.reshape(-1, samples).mean(axis=1)


def has_color(mesh: trimesh.Trimesh) -> bool:
    """Whether a mesh carries its own colors: a base color texture or vertex colors."""
    visual = mesh.visual
    if isinstance(visual, trimesh.visual.TextureVisuals):
        material = getattr(visual, "material", None)
        image = getattr(material, "baseColorTexture", None) or getattr(material, "image", None)
        return image is not None and getattr(visual, "uv", None) is not None
    return isinstance(visual, trimesh.visual.ColorVisuals) and visual.kind == "vertex"


def sample_color(mesh: trimesh.Trimesh, tris: NDArray[np.int64], bary: NDArray[np.float64]) -> F32:
    """RGB in [0, 1] at points given as (triangle, barycentric) on a mesh with colors."""
    faces = np.asarray(mesh.faces)[tris]
    visual = mesh.visual
    if isinstance(visual, trimesh.visual.TextureVisuals):
        material = visual.material
        image = getattr(material, "baseColorTexture", None) or getattr(material, "image", None)
        assert image is not None, "sample_color needs a mesh for which has_color is true"
        rgb = np.asarray(image.convert("RGB"), dtype=np.float32) / 255
        uv = np.einsum("ij,ijk->ik", bary, np.asarray(visual.uv, dtype=np.float64)[faces]) % 1.0
        h, w = rgb.shape[:2]
        px = np.clip((uv[:, 0] * w).astype(int), 0, w - 1)
        py = np.clip(((1 - uv[:, 1]) * h).astype(int), 0, h - 1)  # UVs are bottom-left
        out = rgb[py, px]
        factor = getattr(material, "baseColorFactor", None)
        if factor is not None:
            out = out * (np.asarray(factor[:3], dtype=np.float32) / 255)
        return out.astype(np.float32)
    assert isinstance(visual, trimesh.visual.ColorVisuals)
    colors = np.asarray(visual.vertex_colors, dtype=np.float32)[:, :3] / 255
    return np.einsum("ij,ijk->ik", bary, colors[faces]).astype(np.float32)


def bake(
    low: trimesh.Trimesh,
    high: trimesh.Trimesh,
    size: int,
    cage: float = 0.02,
    ao_samples: int = 24,
    ao_radius: float = 0.15,
    ao_scale: float = 0.5,
    seed: int = 0,
    color: bool = False,
) -> BakeResult:
    """`cage` and `ao_radius` are fractions of the low-poly's bounding-box diagonal.

    With `color`, the high-poly's base color is baked too, from the same hits as the normals.
    """
    uv = mesh_ops.uvs_of(low)
    tangents = mesh_ops.vertex_tangents(low, uv)
    diag = float(np.linalg.norm(low.extents))
    rays = _Rays(high)

    # Normals: shoot inward from a cage just outside the low surface, take the first high hit.
    tx = sample_texels(low, uv, tangents, size)
    offset = cage * diag
    hit, _, n_high = rays.first_hits(tx.position + tx.normal * offset, -tx.normal, 2 * offset)
    n_high[~hit] = tx.normal[~hit]  # misses keep the low-poly's own normal: flat in tangent space
    n_ts = np.stack(
        [np.sum(n_high * tx.tangent, 1), np.sum(n_high * tx.bitangent, 1), np.sum(n_high * tx.normal, 1)], 1
    )
    n_ts = _unit(n_ts)
    n_ts[:, 2] = np.maximum(n_ts[:, 2], 0.0)  # a detail normal never faces into the surface
    normal_tex = np.zeros((size, size, 3), dtype=np.float32)
    normal_tex[...] = (0.5, 0.5, 1.0)
    covered = np.zeros((size, size), dtype=bool)
    normal_tex[tx.ys, tx.xs] = _unit(n_ts) * 0.5 + 0.5
    covered[tx.ys, tx.xs] = True
    normal_tex = mesh_ops.dilate(normal_tex, covered, iterations=8)

    color_img = None
    if color and has_color(high):
        tris, barys = rays.last
        rgb = np.zeros((size, size, 3), dtype=np.float32)
        rgb[tx.ys[hit], tx.xs[hit]] = sample_color(high, tris[hit], barys[hit])
        got = np.zeros((size, size), dtype=bool)
        got[tx.ys[hit], tx.xs[hit]] = True
        # Texels whose ray missed take their neighbors' color; seams stay filled for mipmaps.
        rgb = mesh_ops.dilate(rgb, got, iterations=16)
        color_img = Image.fromarray((np.clip(rgb, 0, 1) * 255).round().astype(np.uint8), "RGB")

    # AO is low frequency: bake it at a fraction of the resolution from the high-poly hits.
    ao_size = max(64, int(size * ao_scale))
    ax = sample_texels(low, uv, tangents, ao_size)
    a_hit, a_points, a_normals = rays.first_hits(ax.position + ax.normal * offset, -ax.normal, 2 * offset)
    ao = np.ones(len(ax.ys))
    ao[a_hit] = rays.occlusion(a_points[a_hit], a_normals[a_hit], ao_samples, ao_radius * diag, seed)
    ao_tex = np.ones((ao_size, ao_size, 1), dtype=np.float32)
    ao_cov = np.zeros((ao_size, ao_size), dtype=bool)
    ao_tex[ax.ys, ax.xs, 0] = ao
    ao_cov[ax.ys, ax.xs] = True
    ao_tex = mesh_ops.dilate(ao_tex, ao_cov, iterations=6)
    ao_img = Image.fromarray((np.clip(ao_tex[..., 0], 0, 1) * 255).astype(np.uint8), "L")
    ao_full = ao_img.resize((size, size), Image.Resampling.BILINEAR)
    white = Image.new("L", (size, size), 255)

    return BakeResult(
        normal=Image.fromarray((np.clip(normal_tex, 0, 1) * 255).round().astype(np.uint8), "RGB"),
        orm=Image.merge("RGB", (ao_full, white, white)),
        tangents=tangents,
        hit_ratio=float(hit.mean()) if len(hit) else 0.0,
        ray_engine=rays.engine,
        color=color_img,
    )
