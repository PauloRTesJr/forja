"""Skeletons for the rig stage: the humanoid naming standard, a template fitted to a mesh,
weights by distance to the bones, and the mapping of a predicted skeleton onto humanoid names.

Coordinates are glTF's: Y up, metres, the subject faces +Z, so a character's left is +X.

Humanoid bones are named as Unity's HumanBodyBones (also VRM's humanoid bones): the names
Unity's and Unreal's retargeting map with no manual steps. A rig keeps those names on the
bones themselves; the profile records which predicted bone each one was.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np
import trimesh
from numpy.typing import NDArray
from scipy.spatial import cKDTree

from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import skin as skin_ops
from forja_engine.ops.skin import GENERIC, HUMANOID, Skeleton, VertexSkin, from_dense

NAMING = "unity-humanoid"

#: What a game engine's humanoid avatar cannot do without.
REQUIRED = (
    "Hips",
    "Spine",
    "Head",
    "LeftUpperArm",
    "LeftLowerArm",
    "LeftHand",
    "RightUpperArm",
    "RightLowerArm",
    "RightHand",
    "LeftUpperLeg",
    "LeftLowerLeg",
    "LeftFoot",
    "RightUpperLeg",
    "RightLowerLeg",
    "RightFoot",
)
OPTIONAL = ("Chest", "UpperChest", "Neck", "LeftShoulder", "RightShoulder", "LeftToes", "RightToes")
HUMANOID_BONES = REQUIRED + OPTIONAL

# The template: (name, parent, x, y, z) for a 1 m tall character standing at the origin with
# its arms half-way between hanging down and a T-pose. Fitted per mesh by `template`.
_SPINE = (
    ("Hips", "", 0.0, 0.53, 0.0),
    ("Spine", "Hips", 0.0, 0.60, 0.0),
    ("Chest", "Spine", 0.0, 0.68, 0.0),
    ("UpperChest", "Chest", 0.0, 0.76, 0.0),
    ("Neck", "UpperChest", 0.0, 0.84, 0.0),
    ("Head", "Neck", 0.0, 0.88, 0.0),
)


def _side(prefix: str, sign: float) -> list[tuple[str, str, float, float, float]]:
    return [
        (f"{prefix}Shoulder", "UpperChest", sign * 0.03, 0.82, 0.0),
        (f"{prefix}UpperArm", f"{prefix}Shoulder", sign * 0.10, 0.81, 0.0),
        (f"{prefix}LowerArm", f"{prefix}UpperArm", sign * 0.19, 0.69, 0.0),
        (f"{prefix}Hand", f"{prefix}LowerArm", sign * 0.27, 0.57, 0.0),
        (f"{prefix}UpperLeg", "Hips", sign * 0.08, 0.50, 0.0),
        (f"{prefix}LowerLeg", f"{prefix}UpperLeg", sign * 0.08, 0.28, 0.0),
        (f"{prefix}Foot", f"{prefix}LowerLeg", sign * 0.08, 0.05, 0.0),
        (f"{prefix}Toes", f"{prefix}Foot", sign * 0.08, 0.01, 0.07),
    ]


TEMPLATE = (*_SPINE, *_side("Left", 1.0), *_side("Right", -1.0))


def looks_humanoid(mesh: trimesh.Trimesh) -> bool:
    """A standing character: clearly taller than it is deep, and not much wider than tall."""
    x, y, z = mesh.extents
    return bool(y >= 1.2 * z and y >= 0.7 * x)


def template(mesh: trimesh.Trimesh) -> Skeleton:
    """The humanoid template stretched over the mesh's bounds; the arms follow how far apart
    the hands are (from hanging down to a T-pose)."""
    lo, hi = mesh.bounds
    height = max(float(hi[1] - lo[1]), 1e-9)
    half_span = float(hi[0] - lo[0]) / 2 / height
    cx, cz = float((lo[0] + hi[0]) / 2), float((lo[2] + hi[2]) / 2)
    # How far the hands reach sideways, in heights: about 0.3 hanging down, 1.0 in a T-pose.
    spread = float(np.clip((2 * half_span - 0.3) / 0.7, 0.0, 1.0))
    names, parents, heads = [], [], []
    for name, parent, x, y, z in TEMPLATE:
        if name.endswith(("LowerArm", "Hand")):
            sign = 1.0 if name.startswith("Left") else -1.0
            hand = max(half_span - 0.05, 0.27)
            straight = sign * (hand if name.endswith("Hand") else (0.10 + hand) / 2)
            x = (1 - spread) * x + spread * straight
            y = (1 - spread) * y + spread * 0.81
        names.append(name)
        parents.append(names.index(parent) if parent else -1)
        heads.append((cx + x * height, lo[1] + y * height, cz + z * height))
    return Skeleton(tuple(names), tuple(parents), np.array(heads), HUMANOID)


def chain(mesh: trimesh.Trimesh, bones: int = 4) -> Skeleton:
    """A generic rig for anything else: a chain up the middle of the mesh's tallest axis."""
    lo, hi = mesh.bounds
    axis = int(np.argmax(hi - lo))
    center = (lo + hi) / 2
    heads = np.repeat(center[None, :], bones, axis=0)
    heads[:, axis] = lo[axis] + (hi[axis] - lo[axis]) * np.arange(bones) / bones
    names = ("Root", *(f"Bone_{i}" for i in range(1, bones)))
    return Skeleton(names, (-1, *range(bones - 1)), heads, GENERIC)


def _segments(skeleton: Skeleton) -> tuple[NDArray[np.float64], NDArray[np.float64], NDArray[np.int64]]:
    """Each bone as a segment from its joint to each child; a leaf as a point."""
    starts, ends, owners = [], [], []
    for j in range(len(skeleton)):
        kids = skeleton.children(j)
        for c in kids or [j]:
            starts.append(skeleton.heads[j])
            ends.append(skeleton.heads[c])
            owners.append(j)
    return np.array(starts), np.array(ends), np.array(owners)


def distance_weights(points: NDArray[np.float64], skeleton: Skeleton, falloff: float = 4.0) -> VertexSkin:
    """Weights that fall off with the distance from each point to each bone.

    Deterministic and cheap: the stub's weights, and the fallback for a skeleton that comes
    without any.
    """
    a, b, owner = _segments(skeleton)
    ab = b - a
    length2 = np.maximum((ab * ab).sum(axis=1), 1e-12)
    dense = np.zeros((len(points), len(skeleton)), dtype=np.float64)
    scale = float(np.max(np.ptp(skeleton.heads, axis=0))) or 1.0
    for start in range(0, len(points), 8192):
        p = points[start : start + 8192]
        t = np.clip(np.einsum("pk,sk->ps", p, ab) - (a * ab).sum(axis=1), 0.0, None) / length2
        t = np.clip(t, 0.0, 1.0)
        nearest = a[None, :, :] + t[..., None] * ab[None, :, :]
        d = np.linalg.norm(p[:, None, :] - nearest, axis=2) / scale
        w = 1.0 / np.maximum(d, 1e-4) ** falloff
        block = np.zeros((len(p), len(skeleton)))
        for s in range(len(owner)):
            block[:, owner[s]] = np.maximum(block[:, owner[s]], w[:, s])
        dense[start : start + len(p)] = block
    return from_dense(dense)


def transfer(
    points: NDArray[np.float64], src_points: NDArray[np.float64], src: VertexSkin, joints: int, k: int = 4
) -> VertexSkin:
    """Weights at points, blended from the nearest of another set of skinned points.

    Rig models weight their own resampled copy of the mesh; this moves those weights onto the
    mesh that ships.
    """
    k = min(k, len(src_points))
    dist, idx = cKDTree(src_points).query(points, k=k)
    dist, idx = np.asarray(dist).reshape(len(points), k), np.asarray(idx).reshape(len(points), k)
    inv = 1.0 / np.maximum(dist, 1e-6) ** 2
    inv /= inv.sum(axis=1, keepdims=True)
    dense = np.zeros((len(points), joints), dtype=np.float64)
    rows = np.repeat(np.arange(len(points)), src.joints.shape[1])
    for n in range(k):
        np.add.at(
            dense, (rows, src.joints[idx[:, n]].ravel()), (src.weights[idx[:, n]] * inv[:, n : n + 1]).ravel()
        )
    return from_dense(dense)


# --- the humanoid mapping of a predicted skeleton ------------------------------------------


def _subtree(skeleton: Skeleton, j: int) -> list[int]:
    out, todo = [], [j]
    while todo:
        n = todo.pop()
        out.append(n)
        todo.extend(skeleton.children(n))
    return out


def _main_chain(skeleton: Skeleton, j: int, score: Any) -> list[int]:
    """From j down through the child whose subtree scores highest, to a leaf."""
    out = [j]
    while kids := skeleton.children(out[-1]):
        out.append(max(kids, key=lambda c: (score(c), -c)))
    return out


def map_humanoid(skeleton: Skeleton) -> dict[str, int] | None:
    """Humanoid bone names for the joints of a predicted skeleton, or None when it is not one.

    Reads the shape of the tree: the hips are the joint where two legs go down to the floor on
    either side and the spine goes up to the top; the arms branch sideways off the spine below
    the neck, and the head ends it. Fingers, tails and accessories stay unnamed.
    """
    h = skeleton.heads
    if len(skeleton) < len(REQUIRED):
        return None
    lo, hi = h.min(axis=0), h.max(axis=0)
    height = float(hi[1] - lo[1])
    if height <= 0:
        return None
    subtrees = {j: _subtree(skeleton, j) for j in range(len(skeleton))}
    low = {j: float(np.min(h[subtrees[j], 1])) for j in subtrees}
    top = {j: float(np.max(h[subtrees[j], 1])) for j in subtrees}

    def reach(j: int, cx: float) -> float:
        return float(np.abs(h[subtrees[j], 0] - cx).max())

    hips = None
    for j in sorted(range(len(skeleton)), key=lambda j: (skeleton.depth(j), j)):
        kids = skeleton.children(j)
        cx = float(h[j, 0])
        down = [c for c in kids if low[c] < lo[1] + 0.15 * height and top[c] < h[j, 1] + 0.05 * height]
        up = [c for c in kids if top[c] > lo[1] + 0.85 * height]
        left = [c for c in down if h[subtrees[c], 0].mean() > cx]
        right = [c for c in down if h[subtrees[c], 0].mean() < cx]
        if up and left and right:
            hips = (
                j,
                max(up, key=lambda c: top[c]),
                min(left, key=lambda c: low[c]),
                min(right, key=lambda c: low[c]),
            )
            break
    if hips is None:
        return None
    hip, spine_root, left_leg, right_leg = hips
    cx = float(h[hip, 0])
    names: dict[str, int] = {"Hips": hip}

    spine = _main_chain(skeleton, spine_root, lambda c: top[c])
    arms: dict[str, tuple[int, int]] = {}  # side -> (chain index the arm leaves from, arm root)
    for i, j in enumerate(spine):
        for c in skeleton.children(j):
            if i + 1 < len(spine) and c == spine[i + 1]:
                continue
            side = "Left" if h[subtrees[c], 0].mean() > cx else "Right"
            if reach(c, cx) < 0.12 * height:
                continue  # a ponytail, a cape, a weapon on the back
            if side not in arms or reach(c, cx) > reach(arms[side][1], cx):
                arms[side] = (i, c)
    if set(arms) != {"Left", "Right"}:
        return None
    branch = max(arms["Left"][0], arms["Right"][0])
    torso, neck = spine[: branch + 1], spine[branch + 1 :]
    if not neck:
        return None
    names["Spine"] = torso[0]
    if len(torso) == 2:
        names["Chest"] = torso[1]
    elif len(torso) >= 3:
        names["Chest"], names["UpperChest"] = torso[-2], torso[-1]
    if len(neck) == 1:
        names["Head"] = neck[0]
    else:
        names["Neck"], names["Head"] = neck[0], neck[1]

    for side, (_, root) in arms.items():
        arm = _main_chain(skeleton, root, lambda c: reach(c, cx))
        # A collarbone starts at the spine; the upper arm starts out at the shoulder.
        upper = 1 if len(arm) >= 4 and abs(h[arm[0], 0] - cx) < 0.06 * height else 0
        # The hand is where the fingers fan out, or the third joint of a bare arm.
        fan = (i for i in range(upper + 2, len(arm)) if len(skeleton.children(arm[i])) >= 2)
        hand = next(fan, upper + 2)
        if hand >= len(arm):
            return None
        if h[arm[hand], 1] < lo[1] + 0.3 * height:
            return None  # a "hand" on the ground is a front foot
        middle = (h[arm[upper]] + h[arm[hand]]) / 2
        elbow = min(range(upper + 1, hand), key=lambda i: float(np.linalg.norm(h[arm[i]] - middle)))
        if upper:
            names[f"{side}Shoulder"] = arm[0]
        names[f"{side}UpperArm"], names[f"{side}LowerArm"], names[f"{side}Hand"] = (
            arm[upper],
            arm[elbow],
            arm[hand],
        )

    for side, root in (("Left", left_leg), ("Right", right_leg)):
        leg = _main_chain(skeleton, root, lambda c: -low[c])
        # A short pelvis bone off the hips is not the thigh.
        if len(leg) >= 4 and np.linalg.norm(h[leg[0]] - h[hip]) < 0.04 * height:
            leg = leg[1:]
        if len(leg) < 3:
            return None
        ground = lo[1] + 0.12 * height
        foot = next((i for i, j in enumerate(leg) if i >= 2 and h[j, 1] < ground), len(leg) - 1)
        if len(leg) > 3 and foot == len(leg) - 1 and h[leg[-1], 2] > h[leg[-2], 2]:
            foot -= 1  # the last joint is the toes, ahead of the ankle
        knee = min(range(1, foot), key=lambda i: abs(h[leg[i], 1] - (h[leg[0], 1] + h[leg[foot], 1]) / 2))
        names[f"{side}UpperLeg"], names[f"{side}LowerLeg"], names[f"{side}Foot"] = (
            leg[0],
            leg[knee],
            leg[foot],
        )
        if foot + 1 < len(leg):
            names[f"{side}Toes"] = leg[foot + 1]
    if len(set(names.values())) != len(names) or not set(REQUIRED) <= set(names):
        return None
    return names


def name_humanoid(skeleton: Skeleton, bone_map: dict[str, int]) -> Skeleton:
    """The skeleton with humanoid names on the mapped joints; the others keep theirs unless
    one would clash with a humanoid name."""
    mapped = {j: n for n, j in bone_map.items()}
    taken = set(bone_map)
    names = []
    for j, own in enumerate(skeleton.names):
        if j in mapped:
            names.append(mapped[j])
            continue
        n = own if own not in HUMANOID_BONES and own not in taken else f"{own}_{j}"
        while n in taken:
            n += "_"
        taken.add(n)
        names.append(n)
    return Skeleton(tuple(names), skeleton.parents, skeleton.heads, HUMANOID)


def as_humanoid(skeleton: Skeleton) -> tuple[Skeleton, dict[str, str]]:
    """A predicted skeleton under humanoid names when it is one, else as it came (generic),
    and the map from each humanoid name to the predicted bone's own name."""
    if skeleton.profile == HUMANOID and set(REQUIRED) <= set(skeleton.names):
        return skeleton, {n: n for n in skeleton.names if n in HUMANOID_BONES}
    bone_map = map_humanoid(skeleton)
    if bone_map is None:
        return Skeleton(skeleton.names, skeleton.parents, skeleton.heads, GENERIC), {}
    return name_humanoid(skeleton, bone_map), {n: skeleton.names[j] for n, j in bone_map.items()}


def save(
    mesh: trimesh.Trimesh,
    skeleton: Skeleton,
    weights: VertexSkin,
    bone_map: dict[str, str],
    source: str,
    out_dir: Path,
) -> tuple[Path, Path]:
    """Writes the rig stage's outputs: the skinned GLB and its profile."""
    if len(weights) != len(mesh.vertices):
        raise ValueError(f"{len(weights)} weights for {len(mesh.vertices)} vertices")
    material = getattr(mesh.visual, "material", None)
    if getattr(material, "normalTexture", None) is not None:
        # Same mesh, same normals, same algorithm: the tangents its maps were baked with.
        mesh.metadata[mesh_ops.TANGENTS] = mesh_ops.vertex_tangents(mesh, mesh_ops.uvs_of(mesh))
    mesh.metadata[skin_ops.SKIN] = weights
    mesh.metadata[skin_ops.SKELETON] = skeleton
    glb = mesh_ops.save(mesh, out_dir / "rigged.glb")
    path = out_dir / "skeleton.json"
    path.write_text(json.dumps(profile(skeleton, bone_map, source), sort_keys=True), encoding="utf-8")
    return glb, path


def profile(skeleton: Skeleton, bone_map: dict[str, str], source: str) -> dict[str, Any]:
    """The rig stage's JSON output: what kind of skeleton this is and what each bone is."""
    return {
        "version": 1,
        "profile": skeleton.profile,
        "naming": NAMING if skeleton.profile == HUMANOID else None,
        "source": source,
        "bones": [
            {
                "name": n,
                "parent": skeleton.names[p] if p >= 0 else None,
                "head": [round(float(v), 5) for v in skeleton.heads[j]],
            }
            for j, (n, p) in enumerate(zip(skeleton.names, skeleton.parents, strict=True))
        ],
        "bone_map": bone_map,
    }
