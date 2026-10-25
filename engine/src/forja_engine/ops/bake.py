"""High-to-low baking: tangent-space normals, ambient occlusion and, on request, base color,
in the low-poly's UV space.

- Tangent frames are MikkTSpace (ops/tangents.py), the ones Blender, Unreal, Unity and glTF
  viewers derive themselves, so the normal map reads right whether or not an importer keeps
  the TANGENT attribute. Vertices split where MikkTSpace gives one vertex two frames.
- Each texel averages `samples` rotated-grid samples of its footprint (anti-aliasing).
- A sample's ray starts on a cage just outside the low surface and travels inward along the
  smoothed low normal; the first high-poly face it meets from the front gives the detail. The
  cage distance is measured from how far the high-poly actually lies from the low-poly, and
  samples whose ray missed try again from twice and four times as far.
- AO casts cosine-weighted hemisphere occlusion rays from those hits, at a fraction of the map
  resolution, smoothed within each UV island so its noise never crosses a seam.
- Every texel no triangle covers takes its nearest covered texel's value (edge padding all the
  way), so neither bilinear filtering nor any mip level pulls a gutter color across a seam.

Ray casting uses Embree (embreex) on several threads. Without it the normals fall back to the
nearest high-poly vertex and AO is left white, so a bake never fails for lack of a native library.
"""

from __future__ import annotations

import logging
import os
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Any

import numpy as np
import trimesh
from numpy.typing import NDArray
from PIL import Image
from scipy import ndimage
from scipy.sparse import coo_matrix
from scipy.sparse.csgraph import connected_components
from scipy.spatial import cKDTree

from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import tangents as tangent_ops

log = logging.getLogger(__name__)
F32 = NDArray[np.float32]
F64 = NDArray[np.float64]
F = NDArray[np.floating[Any]]  # float32 on the per-sample paths: half the memory traffic

# Rotated-grid offsets inside a texel: four samples, no two in a row or column.
_RGSS = np.array([[-0.125, -0.375], [0.375, -0.125], [0.125, 0.375], [-0.375, 0.125]])
_CHUNK = 1 << 16  # texels per work item
_RETRIES = (1.0, 2.0, 4.0)  # cage multiples tried for samples whose ray missed
_RING = 1.5  # gutter texels baked around each UV island: the ones bilinear filtering reads


@dataclass(frozen=True)
class BakeResult:
    normal: Image.Image  # RGB tangent-space normal map, OpenGL (+Y up) convention as glTF requires
    orm: Image.Image  # R = occlusion, G = B = 1 (roughness and metallic stay constant factors)
    tangents: F32  # per-vertex MikkTSpace tangents of `mesh`, written into the GLB
    hit_ratio: float  # share of texels whose rays found the high-poly; low values mean a bad cage
    ray_engine: str
    # The low-poly the maps belong to: the input, with vertices split where MikkTSpace needs it.
    mesh: trimesh.Trimesh
    # The high-poly's own colors in the low-poly's UVs, when asked for and the high-poly has any
    # (a textured mesh from a model that does shape and texture in one pass).
    color: Image.Image | None = None
    cage: float = 0.0  # the cage distance used first, as a fraction of the low-poly's diagonal


def raster_uv(uv: F64, faces: NDArray[np.int64], size: int) -> tuple[NDArray[np.int64], F32]:
    """Triangle id (-1 where empty) and barycentrics for each texel center of a size x size map.

    Where triangles share a texel the later one wins.
    """
    tri_id = np.full((size, size), -1, dtype=np.int64)
    bary = np.zeros((size, size, 3), dtype=np.float32)
    tri, lo, hi = _pixel_triangles(uv, faces, size)
    span = hi - lo + 1
    count = span[:, 0] * span[:, 1]
    a, b, c = tri[:, 0], tri[:, 1], tri[:, 2]
    # Per-triangle terms of the barycentric formula, each computed as the per-texel one would be.
    coef = np.stack(
        [b[:, 1] - c[:, 1], c[:, 0] - b[:, 0], c[:, 1] - a[:, 1], a[:, 0] - c[:, 0], c[:, 0], c[:, 1]], 1
    )
    det = (b[:, 1] - c[:, 1]) * (a[:, 0] - c[:, 0]) + (c[:, 0] - b[:, 0]) * (a[:, 1] - c[:, 1])
    ends = np.cumsum(count)
    budget = 1 << 21  # candidate texels per batch, to bound memory
    bounds, start = [], 0
    while start < len(faces):
        done = ends[start - 1] if start else 0
        stop = min(len(faces), max(start + 1, int(np.searchsorted(ends, done + budget, side="right"))))
        bounds.append((start, stop))
        start = stop

    def batch(i: int) -> tuple[NDArray[np.int64], NDArray[np.int64], F64]:
        return _raster_batch(coef, det, lo, span, count, *bounds[i], size)

    with ThreadPoolExecutor(max(1, min(4, _threads(), len(bounds)))) as pool:
        # Applied in order, so where triangles share a texel the later one still wins.
        for texel, owner, w in pool.map(batch, range(len(bounds))):
            tri_id.reshape(-1)[texel] = owner
            bary.reshape(-1, 3)[texel] = w
    return tri_id, bary


def _pixel_triangles(
    uv: F64, faces: NDArray[np.int64], size: int
) -> tuple[F64, NDArray[np.int64], NDArray[np.int64]]:
    px = uv * size - 0.5  # texel centers
    px[:, 1] = (size - 1) - px[:, 1]  # trimesh UVs are bottom-left; images are top-left
    tri = px[np.asarray(faces)]
    lo = np.clip(np.floor(tri.min(axis=1)).astype(np.int64), 0, size - 1)
    hi = np.clip(np.ceil(tri.max(axis=1)).astype(np.int64), 0, size - 1)
    return tri, lo, hi


def _raster_batch(
    coef: F64,
    det: F64,
    lo: NDArray[np.int64],
    span: NDArray[np.int64],
    count: NDArray[np.int64],
    start: int,
    stop: int,
    size: int,
) -> tuple[NDArray[np.int64], NDArray[np.int64], F64]:
    """Texels triangles start..stop cover: (flat texel index, triangle, barycentrics)."""
    ids = np.arange(start, stop)
    ids = ids[np.abs(det[ids]) >= 1e-12]
    n = count[ids]
    owner = np.repeat(ids, n)
    local = np.arange(int(n.sum())) - np.repeat(np.cumsum(n) - n, n)
    xs = lo[owner, 0] + local % span[owner, 0]
    ys = lo[owner, 1] + local // span[owner, 0]
    k, d = coef[owner], det[owner]
    dx, dy = xs - k[:, 4], ys - k[:, 5]
    w0 = (k[:, 0] * dx + k[:, 1] * dy) / d
    w1 = (k[:, 2] * dx + k[:, 3] * dy) / d
    w2 = 1.0 - w0 - w1
    inside = np.nonzero((w0 >= -1e-4) & (w1 >= -1e-4) & (w2 >= -1e-4))[0]
    texel = (ys * size + xs)[inside]
    # The last triangle to cover a texel wins, as when drawing them in order.
    last = len(texel) - 1 - np.unique(texel[::-1], return_index=True)[1]
    pick = inside[last]
    return texel[last], owner[pick], np.stack([w0[pick], w1[pick], w2[pick]], axis=-1)


def _with_ring(
    uv: F64, faces: NDArray[np.int64], size: int, tri_id: NDArray[np.int64], bary: F32, width: float
) -> tuple[NDArray[np.int64], NDArray[np.int64], NDArray[np.int64], F32, NDArray[np.bool_]]:
    """Covered texels plus a ring of gutter texels up to `width` texels around each island.

    A ring texel continues the plane of its nearest triangle past the island's edge, so bilinear
    filtering at a seam blends real surface samples instead of copies. Returns rows, columns,
    triangles, barycentrics and which entries are covered.
    """
    covered = tri_id >= 0
    if covered.all() or not covered.any():
        ys, xs = np.nonzero(covered)
        return ys, xs, tri_id[ys, xs], bary[ys, xs], np.ones(len(ys), dtype=bool)
    dist, (iy, ix) = ndimage.distance_transform_edt(~covered, return_indices=True)
    take = covered | (dist <= width)
    ys, xs = np.nonzero(take)
    tris = tri_id[iy[ys, xs], ix[ys, xs]]
    inside = covered[ys, xs]
    b = bary[ys, xs]
    if (~inside).any():
        tri, _, _ = _pixel_triangles(uv, faces, size)
        t = tri[tris[~inside]]
        a, bb, c = t[:, 0], t[:, 1], t[:, 2]
        px, py = xs[~inside].astype(np.float64), ys[~inside].astype(np.float64)
        det = (bb[:, 1] - c[:, 1]) * (a[:, 0] - c[:, 0]) + (c[:, 0] - bb[:, 0]) * (a[:, 1] - c[:, 1])
        det = np.where(np.abs(det) < 1e-12, 1.0, det)
        w0 = ((bb[:, 1] - c[:, 1]) * (px - c[:, 0]) + (c[:, 0] - bb[:, 0]) * (py - c[:, 1])) / det
        w1 = ((c[:, 1] - a[:, 1]) * (px - c[:, 0]) + (a[:, 0] - c[:, 0]) * (py - c[:, 1])) / det
        b[~inside] = _near_triangle(np.stack([w0, w1, 1.0 - w0 - w1], axis=1))
    return ys, xs, tris, b, inside


def _near_triangle(bary: F) -> F:
    """Barycentrics past a small triangle's edge would reach far along its plane; these stay
    within half the triangle of it (the clipped weights still sum to at least 0.5)."""
    b = np.clip(bary, -0.5, 1.5)
    return b / np.sum(b, axis=-1, keepdims=True)


def _bary_gradients(uv: F64, faces: NDArray[np.int64], size: int) -> F32:
    """d(barycentrics)/d(texel x, y) per triangle, (m, 3, 2), to place samples inside a texel."""
    tri, _, _ = _pixel_triangles(uv, faces, size)
    a, b, c = tri[:, 0], tri[:, 1], tri[:, 2]
    det = (b[:, 1] - c[:, 1]) * (a[:, 0] - c[:, 0]) + (c[:, 0] - b[:, 0]) * (a[:, 1] - c[:, 1])
    det = np.where(np.abs(det) < 1e-12, np.inf, det)
    g0 = np.stack([(b[:, 1] - c[:, 1]) / det, (c[:, 0] - b[:, 0]) / det], axis=-1)
    g1 = np.stack([(c[:, 1] - a[:, 1]) / det, (a[:, 0] - c[:, 0]) / det], axis=-1)
    return np.stack([g0, g1, -g0 - g1], axis=1).astype(np.float32)


def _dot(a: F, b: F) -> F:
    return np.einsum("...i,...i->...", a, b)


def _unit(v: F) -> F:
    return v / np.maximum(np.sqrt(_dot(v, v)), 1e-12)[..., None]


def _threads() -> int:
    return max(1, min(8, os.cpu_count() or 1))


def _parallel(fn: Callable[[slice], Any], total: int, chunk: int = _CHUNK) -> list[Any]:
    """fn over consecutive slices of range(total), in order. Embree and most of numpy release
    the GIL, so threads share the work."""
    parts = [slice(i, min(i + chunk, total)) for i in range(0, total, chunk)]
    if len(parts) <= 1:
        return [fn(p) for p in parts]
    with ThreadPoolExecutor(min(_threads(), len(parts))) as pool:
        return list(pool.map(fn, parts))


class _Rays:
    """The high-poly, in the bake's normalized space, for first-hit and occlusion queries."""

    def __init__(self, vertices: F64, faces: NDArray[np.int64], normals: F64) -> None:
        self.faces = np.asarray(faces, dtype=np.int64)
        self.vertices = vertices.astype(np.float32)
        self.normals = _unit(np.asarray(normals, dtype=np.float32))
        corners = vertices[self.faces]
        self.face_normals = _unit(
            np.cross(corners[:, 1] - corners[:, 0], corners[:, 2] - corners[:, 0])
        ).astype(np.float32)
        try:
            from embreex import rtcore_scene
            from embreex.mesh_construction import TriangleMesh

            self.scene: Any = rtcore_scene.EmbreeScene()
            TriangleMesh(scene=self.scene, vertices=self.vertices, indices=self.faces.astype(np.int32))
            self.engine = "embree"
        except ImportError:
            log.warning("embreex not installed: normals use nearest vertices and AO is skipped")
            self.scene = None
            self.engine = "nearest"
            self.tree = cKDTree(vertices)
            first_face = np.full(len(vertices), -1, dtype=np.int64)
            for corner in range(3):
                unset = first_face[self.faces[:, corner]] < 0
                first_face[self.faces[unset, corner]] = np.nonzero(unset)[0]
            self.first_face = np.maximum(first_face, 0)

    def first_hits(
        self, origins: F, dirs: F, max_dist: F
    ) -> tuple[NDArray[np.bool_], NDArray[np.int64], F32]:
        """(hit, triangle, barycentrics) of the first front-facing hit within max_dist.

        A ray that first meets a back face started inside the high-poly; it counts as a miss so
        a farther cage can try again.
        """
        n = len(origins)
        if self.scene is None:
            dist, idx = self.tree.query(origins + dirs * max_dist[:, None] / 2)
            tris = self.first_face[idx]
            bary = np.zeros((n, 3), dtype=np.float32)
            bary[np.arange(n), np.argmax(self.faces[tris] == idx[:, None], axis=1)] = 1.0
            return dist < max_dist, tris, bary
        res = self.scene.run(
            origins.astype(np.float32), dirs.astype(np.float32), dists=max_dist.astype(np.float32), output=1
        )
        tris = res["primID"].astype(np.int64)
        hit = tris >= 0
        tris = np.where(hit, tris, 0)
        hit &= _dot(dirs, self.face_normals[tris]) < 0
        u, v = res["u"], res["v"]
        return hit, tris, np.stack([1.0 - u - v, u, v], axis=1)

    def smooth_normal(self, tris: NDArray[np.int64], bary: F) -> F:
        return _unit(np.einsum("ij,ijk->ik", bary, self.normals[self.faces[tris]]))

    def point(self, tris: NDArray[np.int64], bary: F) -> F:
        return np.einsum("ij,ijk->ik", bary, self.vertices[self.faces[tris]])

    def occluded(self, origins: F, dirs: F, dist: float) -> NDArray[np.bool_]:
        out = self.scene.run(
            origins.astype(np.float32),
            dirs.astype(np.float32),
            dists=np.full(len(origins), dist, dtype=np.float32),
            query="OCCLUDED",
        )
        return np.asarray(out) >= 0


def has_color(mesh: trimesh.Trimesh) -> bool:
    """Whether a mesh carries its own colors: a base color texture or vertex colors."""
    visual = mesh.visual
    if isinstance(visual, trimesh.visual.TextureVisuals):
        material = getattr(visual, "material", None)
        image = getattr(material, "baseColorTexture", None) or getattr(material, "image", None)
        return image is not None and getattr(visual, "uv", None) is not None
    return isinstance(visual, trimesh.visual.ColorVisuals) and visual.kind == "vertex"


def _bilinear(img: F, uv: F64) -> F32:
    """Bilinear lookup with wrapping at bottom-left UVs, as a GPU samples a repeating texture."""
    h, w = img.shape[:2]
    x = (uv[:, 0] % 1.0) * w - 0.5
    y = ((1.0 - uv[:, 1]) % 1.0) * h - 0.5
    x0, y0 = np.floor(x).astype(np.int64), np.floor(y).astype(np.int64)
    fx, fy = (x - x0)[:, None], (y - y0)[:, None]
    x0, x1, y0, y1 = x0 % w, (x0 + 1) % w, y0 % h, (y0 + 1) % h
    top = img[y0, x0] * (1 - fx) + img[y0, x1] * fx
    bottom = img[y1, x0] * (1 - fx) + img[y1, x1] * fx
    return (top * (1 - fy) + bottom * fy).astype(np.float32)


class _Colors:
    """Samples a high-poly's own colors at (triangle, barycentric) points."""

    def __init__(self, mesh: trimesh.Trimesh) -> None:
        self.faces = np.asarray(mesh.faces)
        visual = mesh.visual
        self.texture: F | None = None
        if isinstance(visual, trimesh.visual.TextureVisuals):
            material = visual.material
            image = getattr(material, "baseColorTexture", None) or getattr(material, "image", None)
            assert image is not None, "_Colors needs a mesh for which has_color is true"
            self.texture = np.asarray(image.convert("RGB"), dtype=np.float32) / 255
            self.uv = np.asarray(visual.uv, dtype=np.float64)
            factor = getattr(material, "baseColorFactor", None)
            self.factor = (
                np.ones(3, np.float32) if factor is None else np.asarray(factor[:3], np.float32) / 255
            )
        else:
            assert isinstance(visual, trimesh.visual.ColorVisuals)
            self.vertex = np.asarray(visual.vertex_colors, dtype=np.float32)[:, :3] / 255

    def at(self, tris: NDArray[np.int64], bary: F) -> F32:
        corners = self.faces[tris]
        if self.texture is not None:
            uv = np.einsum("ij,ijk->ik", bary, self.uv[corners])
            return (_bilinear(self.texture, uv) * self.factor).astype(np.float32)
        return np.einsum("ij,ijk->ik", bary, self.vertex[corners]).astype(np.float32)


def sample_color(mesh: trimesh.Trimesh, tris: NDArray[np.int64], bary: F) -> F32:
    """RGB in [0, 1] at points given as (triangle, barycentric) on a mesh with colors."""
    return _Colors(mesh).at(tris, bary)


def fill_gutters(tex: F32, covered: NDArray[np.bool_], keep: NDArray[np.bool_] | None = None) -> F32:
    """Every uncovered texel takes the value of its nearest covered one, except those in `keep`."""
    if covered.all() or not covered.any():
        return tex
    idx = ndimage.distance_transform_edt(~covered, return_distances=False, return_indices=True)
    filled = tex[idx[0], idx[1]]
    if keep is not None:
        filled[keep] = tex[keep]
    return filled


def _islands(faces: NDArray[np.int64], vertex_count: int) -> NDArray[np.int64]:
    """UV island of each face: faces that share a vertex share UVs there (seams split vertices)."""
    m = len(faces)
    rows = np.repeat(np.arange(m), 3)
    graph = coo_matrix((np.ones(3 * m, np.int8), (rows, m + faces.ravel())), shape=(m + vertex_count,) * 2)
    _, label = connected_components(graph, directed=False)
    return label[:m]


def _smooth_within(values: F32, island: NDArray[np.int64], passes: int) -> F32:
    """3x3 box blur in which a texel only averages texels of its own UV island."""
    h, w = values.shape
    for _ in range(passes):
        pv = np.pad(values, 1)
        pi = np.pad(island, 1, constant_values=-1)
        acc = np.zeros_like(values)
        cnt = np.zeros_like(values)
        for dy in range(3):
            for dx in range(3):
                same = (pi[dy : dy + h, dx : dx + w] == island).astype(np.float32)
                acc += pv[dy : dy + h, dx : dx + w] * same
                cnt += same
        values = np.where(island >= 0, acc / np.maximum(cnt, 1), values)
    return values


@dataclass
class _Low:
    """The low-poly's attributes in the bake's normalized space."""

    vertices: F32
    normals: F32
    tangents: F32  # (n, 4)
    faces: NDArray[np.int64]
    uv: F64


def _sample(low: _Low, tris: NDArray[np.int64], bary: F) -> tuple[F, F, F, F]:
    """Position, normal, tangent and bitangent of the low-poly at (triangle, barycentric) points,
    as a renderer reconstructs them: interpolated, normalized, bitangent = w * cross(n, t)."""
    corners = low.faces[tris]
    pos = np.einsum("ij,ijk->ik", bary, low.vertices[corners])
    normal = _unit(np.einsum("ij,ijk->ik", bary, low.normals[corners]))
    tan4 = np.einsum("ij,ijk->ik", bary, low.tangents[corners])
    tangent = _unit(tan4[:, :3])
    sign = np.where(tan4[:, 3:] < 0, -1, 1).astype(tangent.dtype)
    return pos, normal, tangent, _unit(np.cross(normal, tangent) * sign)


def _to_tangent_space(n_world: F, t: F, b: F, n: F) -> F:
    """The tangent-space vector x for which normalize(x.x t + x.y b + x.z n) is n_world.

    Solved exactly rather than projected, since an interpolated tangent need not be orthogonal
    to the interpolated normal; a detail normal never faces into the surface.
    """
    r0, r1, r2 = np.cross(b, n), np.cross(n, t), np.cross(t, b)
    det = _dot(t, r0)[:, None]
    x = np.stack([_dot(r0, n_world), _dot(r1, n_world), _dot(r2, n_world)], 1)
    x = x / np.where(np.abs(det) < 1e-12, 1, det)
    x[:, 2] = np.maximum(x[:, 2], 0)
    x[_dot(x, x) < 1e-18] = (0, 0, 1)
    return _unit(x)


def _cast(rays: _Rays, pos: F, normal: F, cage: float) -> tuple[NDArray[np.bool_], NDArray[np.int64], F32]:
    """First hits from the cage inward; misses try again from farther cages."""
    n = len(pos)
    hit = np.zeros(n, dtype=bool)
    tris = np.zeros(n, dtype=np.int64)
    bary = np.zeros((n, 3), dtype=np.float32)
    todo = np.arange(n)
    for scale in _RETRIES:
        if len(todo) == 0:
            break
        d = cage * scale
        h, t, b = rays.first_hits(
            pos[todo] + normal[todo] * d, -normal[todo], np.full(len(todo), 2 * d, dtype=np.float32)
        )
        hit[todo[h]], tris[todo[h]], bary[todo[h]] = True, t[h], b[h]
        todo = todo[~h]
        if rays.engine != "embree":
            break  # the nearest-vertex stand-in has no distance to widen
    return hit, tris, bary


def _auto_cage(rays: _Rays, pos: F, normal: F) -> float:
    """A cage distance from how far the high-poly lies from the low-poly, measured along the
    low normals: twice the 95th percentile, within 0.3% and 5% of the diagonal."""
    if rays.engine != "embree" or len(pos) == 0:
        return 0.02
    pick = np.linspace(0, len(pos) - 1, min(len(pos), 20000)).astype(np.int64)
    reach = 0.05
    hit, tris, bary = rays.first_hits(
        pos[pick] + normal[pick] * reach, -normal[pick], np.full(len(pick), 2 * reach, dtype=np.float32)
    )
    if hit.mean() < 0.5:
        return 0.02
    offset = _dot(rays.point(tris[hit], bary[hit]) - pos[pick][hit], normal[pick][hit])
    return float(np.clip(2 * np.percentile(np.abs(offset), 95), 0.003, 0.05))


def bake(
    low: trimesh.Trimesh,
    high: trimesh.Trimesh,
    size: int,
    cage: float | None = None,
    ao_samples: int = 32,
    ao_radius: float = 0.15,
    ao_scale: float = 0.5,
    seed: int = 0,
    color: bool = False,
    samples: int = 4,
) -> BakeResult:
    """`cage` and `ao_radius` are fractions of the low-poly's bounding-box diagonal; without
    `cage` it is measured from the meshes. `samples` per texel is 1 or 4.

    With `color`, the high-poly's base color is baked too, from the same hits as the normals.
    """
    uv = mesh_ops.uvs_of(low)
    corner, groups = tangent_ops.corner_tangents(low.vertices, low.vertex_normals, uv, low.faces)
    src, faces, tan = tangent_ops.split(low.faces, corner, groups)
    low = _split(low, src, faces, uv)
    uv = mesh_ops.uvs_of(low)

    center = low.bounds.mean(axis=0)
    diag = max(float(np.linalg.norm(low.extents)), 1e-12)
    space = _Low(
        vertices=((np.asarray(low.vertices, np.float64) - center) / diag).astype(np.float32),
        normals=_unit(np.asarray(low.vertex_normals, np.float32)),
        tangents=tan.astype(np.float32),
        faces=np.asarray(low.faces, np.int64),
        uv=uv,
    )
    rays = _Rays(
        (np.asarray(high.vertices, np.float64) - center) / diag, high.faces, np.asarray(high.vertex_normals)
    )

    tri_id, bary = raster_uv(uv, space.faces, size)
    ys, xs, t_tris, t_bary, inside = _with_ring(uv, space.faces, size, tri_id, bary, _RING)
    if cage is None:
        pos0, nrm0, _, _ = _sample(space, t_tris[inside], t_bary[inside])
        cage_d = _auto_cage(rays, pos0, nrm0)
    else:
        cage_d = float(cage)

    offsets = (_RGSS if samples > 1 else np.zeros((1, 2))).astype(np.float32)
    grads = _bary_gradients(uv, space.faces, size)
    colors = _Colors(high) if color and has_color(high) else None

    def normals_chunk(part: slice) -> tuple[F, NDArray[np.bool_], F | None]:
        tr, b0 = t_tris[part], t_bary[part]
        k = len(offsets)
        sub = _near_triangle(b0[:, None, :] + np.einsum("ijk,lk->ilj", grads[tr], offsets))
        tr_k = np.repeat(tr, k)
        pos, nrm, tng, btg = _sample(space, tr_k, sub.reshape(-1, 3))
        hit, h_tris, h_bary = _cast(rays, pos, nrm, cage_d)
        n_ts = np.zeros((len(pos), 3), dtype=np.float32)
        n_ts[:, 2] = 1.0  # misses keep the low-poly's own normal: flat in tangent space
        if hit.any():
            n_high = rays.smooth_normal(h_tris[hit], h_bary[hit])
            n_ts[hit] = _to_tangent_space(n_high, tng[hit], btg[hit], nrm[hit])
        texel = _unit(n_ts.reshape(-1, k, 3).mean(axis=1))
        hits = hit.reshape(-1, k)
        rgb = None
        if colors is not None:
            c = np.zeros((len(pos), 3), dtype=np.float32)
            if hit.any():
                c[hit] = colors.at(h_tris[hit], h_bary[hit])
            got = hits.sum(axis=1)
            rgb = (c.reshape(-1, k, 3) * hits[..., None]).sum(axis=1) / np.maximum(got, 1)[:, None]
        return texel, hits.any(axis=1), rgb

    parts = _parallel(normals_chunk, len(ys))
    covered = tri_id >= 0
    normal_tex = np.zeros((size, size, 3), dtype=np.float32)
    normal_tex[...] = (0.0, 0.0, 1.0)
    hits = np.zeros(len(ys), dtype=bool)
    if parts:
        normal_tex[ys, xs] = np.concatenate([p[0] for p in parts])
        hits = np.concatenate([p[1] for p in parts])
    # The rest of the gutter copies the island's own edge: a ring sample far past it, or one that
    # missed, would spread into every lower mip level.
    ring = np.zeros((size, size), dtype=bool)
    ring[ys[~inside & hits], xs[~inside & hits]] = True
    normal_tex = fill_gutters(normal_tex * 0.5 + 0.5, covered, keep=ring)

    color_img = None
    if colors is not None:
        rgb = np.zeros((size, size, 3), dtype=np.float32)
        got = np.zeros((size, size), dtype=bool)
        if parts:
            rgb[ys, xs] = np.concatenate([p[2] for p in parts])
            got[ys[inside & hits], xs[inside & hits]] = True
        # Texels whose rays all missed take their neighbors' color; gutters stay filled for mipmaps.
        rgb = fill_gutters(rgb, got, keep=ring)
        color_img = Image.fromarray((np.clip(rgb, 0, 1) * 255).round().astype(np.uint8), "RGB")

    ao_full = _bake_ao(space, rays, size, ao_scale, ao_samples, ao_radius, cage_d, seed)
    white = Image.new("L", (size, size), 255)
    return BakeResult(
        normal=Image.fromarray((np.clip(normal_tex, 0, 1) * 255).round().astype(np.uint8), "RGB"),
        orm=Image.merge("RGB", (ao_full, white, white)),
        tangents=tan,
        hit_ratio=float(hits[inside].mean()) if inside.any() else 0.0,
        ray_engine=rays.engine,
        mesh=low,
        color=color_img,
        cage=cage_d,
    )


def _split(
    low: trimesh.Trimesh, src: NDArray[np.int64], faces: NDArray[np.int64], uv: F64
) -> trimesh.Trimesh:
    """The low-poly with one vertex per entry of src, keeping its normals, UVs and material."""
    if (
        len(src) == len(low.vertices)
        and np.array_equal(src, np.arange(len(src)))
        and np.array_equal(faces, low.faces)
    ):
        return low
    normals = np.asarray(low.vertex_normals)[src]
    out = trimesh.Trimesh(np.asarray(low.vertices)[src], faces, process=False)
    out.visual = trimesh.visual.TextureVisuals(uv=uv[src], material=getattr(low.visual, "material", None))
    out.vertex_normals = normals  # type: ignore[method-assign]  # trimesh property setter
    return out


def _bake_ao(
    low: _Low, rays: _Rays, size: int, scale: float, samples: int, radius: float, cage: float, seed: int
) -> Image.Image:
    """AO is low frequency: baked at a fraction of the resolution from the high-poly hits."""
    ao_size = max(64, int(size * scale))
    if rays.engine != "embree" or samples <= 0:
        return Image.new("L", (size, size), 255)
    tri_id, bary = raster_uv(low.uv, low.faces, ao_size)
    ys, xs, tris, b, inside = _with_ring(low.uv, low.faces, ao_size, tri_id, bary, _RING)
    pos, nrm, _, _ = _sample(low, tris, b)
    rng = np.random.default_rng(seed)
    turn = rng.uniform(0, 2 * np.pi, size=len(ys)).astype(np.float32)
    k = np.arange(samples)
    r = np.sqrt((k + 0.5) / samples).astype(np.float32)
    golden = (k * np.pi * (3 - np.sqrt(5))).astype(np.float32)
    up = np.sqrt(1 - r**2)
    eps = 1e-4

    def ao_chunk(part: slice) -> tuple[NDArray[np.bool_], F32]:
        hit, h_tris, h_bary = _cast(rays, pos[part], nrm[part], cage)
        ao = np.ones(len(hit), dtype=np.float32)
        if not hit.any():
            return hit, ao
        points = rays.point(h_tris[hit], h_bary[hit])
        normals = rays.smooth_normal(h_tris[hit], h_bary[hit])
        lift = rays.face_normals[h_tris[hit]]
        # Stratified cosine-weighted directions, turned at random per texel: noise, not banding.
        phi = golden + turn[part][hit][:, None]
        local = np.stack([r * np.cos(phi), r * np.sin(phi), np.broadcast_to(up, phi.shape)], -1)
        x_axis, y_axis = np.eye(3, dtype=np.float32)[:2, None]
        helper = np.where(np.abs(normals[:, :1]) < 0.9, x_axis, y_axis)
        t = _unit(np.cross(helper, normals))
        b = np.cross(normals, t)
        dirs = (
            local[..., 0:1] * t[:, None] + local[..., 1:2] * b[:, None] + local[..., 2:3] * normals[:, None]
        )
        starts = np.repeat(points + lift * eps, samples, axis=0)
        blocked = rays.occluded(starts, dirs.reshape(-1, 3), radius)
        ao[hit] = 1.0 - blocked.reshape(-1, samples).mean(axis=1)
        return hit, ao

    parts = _parallel(ao_chunk, len(ys), chunk=max(1024, _CHUNK // max(1, samples // 8)))
    ao_tex = np.ones((ao_size, ao_size), dtype=np.float32)
    island_tex = np.full((ao_size, ao_size), -1, dtype=np.int64)
    ring = np.zeros((ao_size, ao_size), dtype=bool)
    if parts:
        hit = np.concatenate([p[0] for p in parts])
        ao_tex[ys, xs] = np.concatenate([p[1] for p in parts])
        use = inside | hit
        island_tex[ys[use], xs[use]] = _islands(low.faces, len(low.vertices))[tris[use]]
        ring[ys[~inside & hit], xs[~inside & hit]] = True
    ao_tex = _smooth_within(ao_tex, island_tex, passes=2)
    ao_tex = fill_gutters(ao_tex, tri_id >= 0, keep=ring)
    ao_img = Image.fromarray((np.clip(ao_tex, 0, 1) * 255).round().astype(np.uint8), "L")
    return ao_img.resize((size, size), Image.Resampling.BILINEAR)
