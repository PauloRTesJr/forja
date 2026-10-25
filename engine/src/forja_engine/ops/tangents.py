"""MikkTSpace tangents, the ones Blender, Unreal, Unity, Godot and the glTF sample viewer derive
when a mesh brings none (http://www.mikktspace.com/). A normal map baked against them reads
the same in every one of those, whether or not it uses the TANGENT attribute.

This follows Morten Mikkelsen's reference implementation with its default 180 degree angular
threshold, under which each group of corners is one subgroup: corners are welded by position,
normal and UV; around each welded vertex, corners of triangles that share an edge and a UV
orientation form a group; a group's tangent is the sum of its triangles' UV derivatives,
projected onto the vertex's normal plane and weighted by the corner angle. Triangles without UV
area add nothing, and here they also join no group (the reference lets them bridge two).

A glTF vertex holds one tangent. Where the corners of one vertex fall into several groups (two
UV charts touching at a corner, a mirror seam), `split` makes one vertex per group.
"""

from __future__ import annotations

import numpy as np
from numpy.typing import NDArray
from scipy.sparse import coo_matrix
from scipy.sparse.csgraph import connected_components

_TINY = float(np.finfo(np.float32).tiny)  # the reference's NotZero threshold, FLT_MIN


def _safe_unit(v: NDArray[np.float64]) -> NDArray[np.float64]:
    """Normalize rows; rows of zeros stay zero, as the reference's NormalizeSafe does."""
    length = np.linalg.norm(v, axis=-1, keepdims=True)
    return np.where(length > _TINY, v / np.maximum(length, _TINY), 0.0)


def _project(v: NDArray[np.float64], n: NDArray[np.float64]) -> NDArray[np.float64]:
    return _safe_unit(v - n * np.sum(n * v, axis=-1, keepdims=True))


def corner_tangents(
    vertices: NDArray[np.float64],
    normals: NDArray[np.float64],
    uv: NDArray[np.float64],
    faces: NDArray[np.int64],
) -> tuple[NDArray[np.float32], NDArray[np.int64]]:
    """Tangent and bitangent sign of each face corner, (m, 3, 4), and each corner's group (m, 3).

    The bitangent is cross(normal, tangent) * w. `uv` has its origin at the bottom left, as in
    trimesh and Blender, so the bitangent follows +v: up in the texture image, as glTF expects.
    """
    # The GLB stores float32, which is what Blender and engines read back.
    v = np.asarray(vertices, dtype=np.float32).astype(np.float64)
    t = np.asarray(uv, dtype=np.float32).astype(np.float64)
    n = np.asarray(normals, dtype=np.float32).astype(np.float64)
    n_len = np.linalg.norm(n, axis=1, keepdims=True)
    n = np.where(n_len > 1e-12, n / np.maximum(n_len, 1e-12), [[0.0, 0.0, 1.0]])
    f = np.asarray(faces, dtype=np.int64).reshape(-1, 3)
    m = len(f)
    out = np.zeros((m, 3, 4), dtype=np.float32)
    groups = np.full((m, 3), -1, dtype=np.int64)
    if m == 0:
        return out, groups

    # The reference identifies a vertex by its values, not its index.
    _, welded = np.unique(np.hstack([v, n, t]), axis=0, return_inverse=True)
    wf = welded.ravel()[f]
    degenerate = (wf[:, 0] == wf[:, 1]) | (wf[:, 1] == wf[:, 2]) | (wf[:, 0] == wf[:, 2])

    d1, d2 = v[f[:, 1]] - v[f[:, 0]], v[f[:, 2]] - v[f[:, 0]]
    t21, t31 = t[f[:, 1]] - t[f[:, 0]], t[f[:, 2]] - t[f[:, 0]]
    area = t21[:, 0] * t31[:, 1] - t21[:, 1] * t31[:, 0]
    v_os = t31[:, 1:2] * d1 - t21[:, 1:2] * d2  # dP/du, up to the sign of the UV area
    v_ot = -t31[:, 0:1] * d1 + t21[:, 0:1] * d2  # dP/dv, likewise
    preserving = area > 0
    len_os = np.linalg.norm(v_os, axis=1)
    len_ot = np.linalg.norm(v_ot, axis=1)
    abs_area = np.maximum(np.abs(area), _TINY)
    good = (
        ~degenerate
        & (np.abs(area) > _TINY)
        & (len_os > _TINY)
        & (len_ot > _TINY)
        & (len_os / abs_area > _TINY)
        & (len_ot / abs_area > _TINY)
    )
    v_os = v_os * (np.where(preserving, 1.0, -1.0) / np.maximum(len_os, _TINY))[:, None]

    # Groups: corners at one welded vertex, joined across shared edges of the same orientation.
    corner = np.arange(3 * m).reshape(m, 3)
    a, b = wf, np.roll(wf, -1, axis=1)  # edge k of a face runs from corner k to corner k + 1
    width = int(np.max(wf)) + 1
    forward = (a * width + b).ravel()
    backward = (b * width + a).ravel()
    order = np.argsort(forward, kind="stable")
    hit = np.searchsorted(forward[order], backward)
    found = hit < len(order)
    hit = np.where(found, hit, 0)
    other = order[hit]
    found &= forward[other] == backward
    edge_face = np.repeat(np.arange(m), 3)
    other_face = other // 3
    joins = (
        found
        & good[edge_face]
        & good[other_face]
        & (preserving[edge_face] == preserving[other_face])
        & (edge_face != other_face)
    )
    e = np.nonzero(joins)[0]
    fa, ka, fb = edge_face[e], e % 3, other_face[e]
    # Edge a -> b of face fa is edge b -> a of face fb: corner at a meets fb's corner at a, and so for b.
    kb_a = np.argmax(wf[fb] == a.ravel()[e][:, None], axis=1)
    kb_b = np.argmax(wf[fb] == b.ravel()[e][:, None], axis=1)
    src = np.concatenate([corner[fa, ka], corner[fa, (ka + 1) % 3]])
    dst = np.concatenate([corner[fb, kb_a], corner[fb, kb_b]])
    graph = coo_matrix((np.ones(len(src), dtype=np.int8), (src, dst)), shape=(3 * m, 3 * m))
    _, label = connected_components(graph, directed=False)
    label = label.reshape(m, 3)

    nc = n[f]  # (m, 3, 3): each corner's normal
    os_c = _project(np.broadcast_to(v_os[:, None, :], (m, 3, 3)), nc)
    e1 = _project(v[np.roll(f, 1, axis=1)] - v[f], nc)  # toward the previous corner
    e2 = _project(v[np.roll(f, -1, axis=1)] - v[f], nc)  # toward the next corner
    angle = np.arccos(np.clip(np.sum(e1 * e2, axis=2), -1.0, 1.0))
    angle = np.where(good[:, None], angle, 0.0)

    flat = label.ravel()
    count = int(flat.max()) + 1
    weighted = (angle[..., None] * os_c).reshape(-1, 3)
    total = np.stack([np.bincount(flat, weights=weighted[:, i], minlength=count) for i in range(3)], 1)
    tangent = _safe_unit(total)[label]
    sign = np.where(preserving, 1.0, -1.0)[:, None]
    # Corners no good triangle reaches get any frame around their normal; glTF wants unit vectors.
    missing = np.linalg.norm(tangent, axis=2) < 0.5
    if missing.any():
        nm = nc[missing]
        helper = np.where(np.abs(nm[:, :1]) < 0.9, [[1.0, 0, 0]], [[0, 1.0, 0]])
        tangent[missing] = _safe_unit(np.cross(nm, helper))
    out[..., :3] = tangent
    out[..., 3] = np.broadcast_to(sign, (m, 3))
    groups[:] = np.where(good[:, None], label, -1)
    return out, groups


def mikktspace(
    vertices: NDArray[np.float64],
    normals: NDArray[np.float64],
    uv: NDArray[np.float64],
    faces: NDArray[np.int64],
) -> NDArray[np.float32]:
    """Per-vertex tangents (xyz) and bitangent signs (w). A vertex whose corners fall into
    several groups takes the tangent of the group with the most corners; `split` avoids that."""
    tan, groups = corner_tangents(vertices, normals, uv, faces)
    f = np.asarray(faces, dtype=np.int64).reshape(-1, 3)
    out = np.zeros((len(vertices), 4), dtype=np.float32)
    out[:, 0] = 1.0  # unreferenced vertices
    out[:, 3] = 1.0
    if len(f) == 0:
        return out
    vert, grp = f.ravel(), groups.ravel()
    pairs, size = np.unique(np.stack([vert, grp], 1), axis=0, return_counts=True)
    # Per vertex: a real group over none (-1), then the one with the most corners.
    best = np.lexsort((-size, pairs[:, 1] < 0, pairs[:, 0]))
    first = np.unique(pairs[best, 0], return_index=True)[1]
    chosen = np.full(len(vertices), -2, dtype=np.int64)
    chosen[pairs[best[first], 0]] = pairs[best[first], 1]
    idx = np.nonzero(grp == chosen[vert])[0]
    rep = idx[np.unique(vert[idx], return_index=True)[1]]
    out[vert[rep]] = tan.reshape(-1, 4)[rep]
    return out


def split(
    faces: NDArray[np.int64], tangents: NDArray[np.float32], groups: NDArray[np.int64]
) -> tuple[NDArray[np.int64], NDArray[np.int64], NDArray[np.float32]]:
    """One vertex per (vertex, group) pair: (source vertex of each new vertex, new faces, tangents).

    Corners outside any group join their vertex's first group, as the reference copies a good
    corner's tangent to them, so no vertex splits for them alone.
    """
    f = np.asarray(faces, dtype=np.int64).reshape(-1, 3)
    vert = f.ravel()
    grp = groups.ravel().copy()
    good = grp >= 0
    first_group = np.full(int(np.max(vert)) + 1 if len(vert) else 0, -1, dtype=np.int64)
    first_group[vert[good][::-1]] = grp[good][::-1]
    grp = np.where(good, grp, first_group[vert])
    keys, inverse = np.unique(np.stack([vert, grp], 1), axis=0, return_inverse=True)
    inverse = inverse.ravel()
    # Each new vertex takes the tangent of one of its good corners when it has any.
    order = np.lexsort((~good, inverse))
    rep = order[np.unique(inverse[order], return_index=True)[1]]
    return keys[:, 0], inverse.reshape(f.shape), tangents.reshape(-1, 4)[rep]
