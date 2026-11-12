"""Lower levels of detail for a textured mesh whose vertices are split along UV seams."""

from __future__ import annotations

from typing import NamedTuple

import numpy as np
import trimesh
from numpy.typing import NDArray
from scipy.spatial import cKDTree

from forja_engine.ops import bake
from forja_engine.ops import mesh as mesh_ops

# Nearest triangle centroids tried per point; the true point-to-triangle distance picks among them.
CANDIDATES = 12
# Rows of (triangle, point) pairs measured at once, to bound memory on dense meshes.
_CHUNK = 65_536

F64 = NDArray[np.float64]
I64 = NDArray[np.int64]


class Level(NamedTuple):
    mesh: trimesh.Trimesh
    # Per output vertex: the LOD0 triangle it lies on and its barycentric coordinates there.
    tris: I64
    bary: F64


class Source:
    """A LOD0 prepared once for any number of levels.

    Simplifying the mesh as stored would see every UV seam as a border and tear the levels open
    along it. So geometry is simplified welded by position, and UVs are rebuilt afterwards:
    a vertex on a seam gets one copy per UV island that meets there, same position, own UV.
    """

    def __init__(self, lod0: trimesh.Trimesh):
        self.lod0 = lod0
        self.uv = mesh_ops.uvs_of(lod0)
        self.welded = trimesh.Trimesh(lod0.vertices, lod0.faces, process=False)
        self.welded.merge_vertices(merge_tex=True, merge_norm=True)
        self.island = bake._islands(np.asarray(lod0.faces), len(lod0.vertices))
        self.island_count = int(np.max(self.island, initial=-1)) + 1
        self.triangles = np.asarray(lod0.triangles, dtype=np.float64)
        self.tree = cKDTree(self.triangles.mean(axis=1))

    def level(self, target_tris: int) -> Level:
        lod = mesh_ops.decimate(self.welded, target_tris)
        faces = np.asarray(lod.faces)
        centers = np.asarray(lod.triangles_center)

        # A face takes the island of the LOD0 triangle nearest its centre.
        face_cand = self._candidates(centers)
        face_tri = face_cand[np.arange(len(faces)), self._distance(face_cand, centers).argmin(axis=1)]
        face_island = self.island[face_tri]

        # One output vertex per (welded vertex, island) pair. Same position keeps the surface
        # closed; different UVs keep the seams sharp.
        corner_island = np.repeat(face_island, 3)
        key = faces.ravel() * self.island_count + corner_island
        _, first, inverse = np.unique(key, return_index=True, return_inverse=True)
        vertex = faces.ravel()[first]
        island = corner_island[first]
        points = np.asarray(lod.vertices)[vertex]

        # The closest point of that island's triangles to the vertex; the face's own triangle
        # is a candidate too, so a pair never lacks one in its island.
        cand = np.concatenate([self._candidates(points), np.repeat(face_tri, 3)[first][:, None]], axis=1)
        dist = self._distance(cand, points)
        dist[self.island[cand] != island[:, None]] = np.inf
        tris = cand[np.arange(len(cand)), dist.argmin(axis=1)]
        bary = self._bary(tris, points)

        uv = np.einsum("ij,ijk->ik", bary, self.uv[np.asarray(self.lod0.faces)[tris]])
        out = trimesh.Trimesh(points, inverse.reshape(-1, 3), process=False)
        material = getattr(self.lod0.visual, "material", None)
        out.visual = trimesh.visual.TextureVisuals(uv=uv, material=material)
        mesh_ops.weld_normals(out)
        return Level(out, tris, bary)

    def _candidates(self, points: F64) -> I64:
        _, ids = self.tree.query(points, k=min(CANDIDATES, len(self.triangles)))
        return np.asarray(ids, dtype=np.int64).reshape(len(points), -1)

    def _closest(self, tris: I64, points: F64) -> F64:
        out = np.empty((len(tris), 3))
        for s in range(0, len(tris), _CHUNK):
            out[s : s + _CHUNK] = trimesh.triangles.closest_point(
                self.triangles[tris[s : s + _CHUNK]], points[s : s + _CHUNK]
            )
        return out

    def _distance(self, cand: I64, points: F64) -> F64:
        """(points, candidates) distances from each point to each of its candidate triangles."""
        n, k = cand.shape
        closest = self._closest(cand.ravel(), np.repeat(points, k, axis=0))
        dist = np.linalg.norm(closest.reshape(n, k, 3) - points[:, None], axis=2)
        return np.nan_to_num(dist, nan=np.inf)

    def _bary(self, tris: I64, points: F64) -> F64:
        closest = self._closest(tris, points)
        with np.errstate(all="ignore"):
            bary = trimesh.triangles.points_to_barycentric(self.triangles[tris], closest)
        bary = np.clip(np.nan_to_num(bary, nan=0.0, posinf=0.0, neginf=0.0), 0, 1)
        total = bary.sum(axis=1, keepdims=True)
        # A sliver LOD0 triangle has no usable coordinates: its centre stands in.
        return np.where(total > 1e-9, bary / np.maximum(total, 1e-9), 1 / 3)
