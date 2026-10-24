"""Skinned meshes in GLB files: a skeleton, and four joint weights per vertex.

trimesh neither reads nor writes glTF skins, so this module adds them to the GLB trimesh
writes and reads them back. The layout is what game engines and Blender import with no manual
steps: one skin shared by every mesh node (one per LOD), joint nodes in the rest pose (a
translation each, no rotation) at the scene root, and JOINTS_0/WEIGHTS_0 on each primitive.
"""

from __future__ import annotations

import json
import struct
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import numpy as np
import trimesh
from numpy.typing import NDArray

#: Trimesh.metadata key for a geometry's VertexSkin, and Scene/Trimesh key for its Skeleton.
SKIN = "skin"
SKELETON = "skeleton"

HUMANOID = "humanoid"
GENERIC = "generic"
INFLUENCES = 4

_FLOAT, _UBYTE, _USHORT = 5126, 5121, 5123
_JSON, _BIN = 0x4E4F534A, 0x004E4942


@dataclass(frozen=True)
class Skeleton:
    """Joints in the rest pose: names, parent indices (-1 for a root), world positions (m)."""

    names: tuple[str, ...]
    parents: tuple[int, ...]
    heads: NDArray[np.float64]
    profile: str = GENERIC

    def __post_init__(self) -> None:
        n = len(self.names)
        if len(self.parents) != n or np.shape(self.heads) != (n, 3):
            raise ValueError("a skeleton needs one name, parent and position per joint")
        if len(set(self.names)) != n:
            raise ValueError("joint names must be unique")
        for j, p in enumerate(self.parents):
            if not -1 <= p < n or p == j:
                raise ValueError(f"joint {self.names[j]} has a bad parent {p}")
        if any(self.depth(j) < 0 for j in range(n)):
            raise ValueError("joint parents form a cycle")

    def __len__(self) -> int:
        return len(self.names)

    def depth(self, j: int) -> int:
        """Links from j up to its root, or -1 when the parents loop."""
        d = 0
        while self.parents[j] >= 0:
            j = self.parents[j]
            d += 1
            if d > len(self.names):
                return -1
        return d

    def children(self, j: int) -> list[int]:
        return [c for c, p in enumerate(self.parents) if p == j]

    @property
    def roots(self) -> list[int]:
        return [j for j, p in enumerate(self.parents) if p < 0]

    def ordered(self) -> list[int]:
        """Joint indices with every parent before its children."""
        return sorted(range(len(self.names)), key=lambda j: (self.depth(j), j))


@dataclass
class VertexSkin:
    """The joints that move each vertex and how much: (n, 4) indices and weights summing to 1."""

    joints: NDArray[np.int64]
    weights: NDArray[np.float32]

    def __len__(self) -> int:
        return len(self.joints)

    def dense(self, joint_count: int) -> NDArray[np.float32]:
        out = np.zeros((len(self.joints), joint_count), dtype=np.float32)
        rows = np.repeat(np.arange(len(self.joints)), self.joints.shape[1])
        np.add.at(out, (rows, self.joints.ravel()), self.weights.ravel())
        return out


def from_dense(weights: NDArray[np.floating[Any]]) -> VertexSkin:
    """The four strongest influences of each vertex, normalized. A vertex no joint reaches
    follows joint 0, the root, rather than staying behind when the rig moves."""
    w = np.nan_to_num(np.asarray(weights, dtype=np.float64), nan=0.0).clip(min=0.0)
    n, j = w.shape
    k = min(INFLUENCES, j)
    idx = np.argsort(-w, axis=1, kind="stable")[:, :k]
    top = np.take_along_axis(w, idx, axis=1)
    total = top.sum(axis=1, keepdims=True)
    lost = total[:, 0] <= 1e-12
    top = np.where(lost[:, None], np.eye(1, k), top / np.maximum(total, 1e-12))
    idx = np.where(lost[:, None], 0, idx)
    top = np.where(top < 1e-4, 0.0, top)
    top /= top.sum(axis=1, keepdims=True)
    joints = np.zeros((n, INFLUENCES), dtype=np.int64)
    out = np.zeros((n, INFLUENCES), dtype=np.float32)
    joints[:, :k] = np.where(top > 0, idx, 0)
    out[:, :k] = top.astype(np.float32)
    # glTF wants each row to sum to 1 within float precision: the strongest absorbs the rest.
    out[:, 0] += 1.0 - out.sum(axis=1)
    return VertexSkin(joints, out)


def interpolate(
    skin: VertexSkin, joint_count: int, faces: NDArray[np.int64], bary: NDArray[np.float64]
) -> VertexSkin:
    """Weights at points inside the given source faces, blended by barycentric coordinates."""
    out_j = np.zeros((len(faces), INFLUENCES), dtype=np.int64)
    out_w = np.zeros((len(faces), INFLUENCES), dtype=np.float32)
    # In chunks: the dense matrix is points x joints, and rigs can have a few hundred joints.
    for start in range(0, len(faces), 8192):
        f = faces[start : start + 8192]
        b = bary[start : start + 8192]
        dense = np.zeros((len(f), joint_count), dtype=np.float64)
        for k in range(3):
            src = skin.joints[f[:, k]]
            w = skin.weights[f[:, k]] * b[:, k : k + 1]
            rows = np.repeat(np.arange(len(f)), INFLUENCES)
            np.add.at(dense, (rows, src.ravel()), w.ravel())
        part = from_dense(dense)
        out_j[start : start + len(f)] = part.joints
        out_w[start : start + len(f)] = part.weights
    return VertexSkin(out_j, out_w)


@dataclass
class _GLB:
    doc: dict[str, Any]
    binary: bytearray = field(default_factory=bytearray)

    @classmethod
    def parse(cls, glb: bytes) -> _GLB:
        if glb[:4] != b"glTF":
            raise ValueError("not a GLB file")
        json_len = struct.unpack_from("<I", glb, 12)[0]
        doc = json.loads(glb[20 : 20 + json_len])
        start = 20 + json_len
        binary = bytearray()
        if start + 8 <= len(glb) and struct.unpack_from("<I", glb, start + 4)[0] == _BIN:
            size = struct.unpack_from("<I", glb, start)[0]
            binary = bytearray(glb[start + 8 : start + 8 + size])
        return cls(doc, binary)

    def add(self, data: NDArray[Any], component: int, kind: str, **extra: Any) -> int:
        while len(self.binary) % 4:
            self.binary.append(0)
        raw = np.ascontiguousarray(data).tobytes()
        self.doc.setdefault("bufferViews", []).append(
            {"buffer": 0, "byteOffset": len(self.binary), "byteLength": len(raw)}
        )
        self.binary.extend(raw)
        count = len(data)
        self.doc.setdefault("accessors", []).append(
            {
                "bufferView": len(self.doc["bufferViews"]) - 1,
                "componentType": component,
                "count": count,
                "type": kind,
                **extra,
            }
        )
        return len(self.doc["accessors"]) - 1

    def read(self, accessor: int) -> NDArray[Any]:
        acc = self.doc["accessors"][accessor]
        view = self.doc["bufferViews"][acc["bufferView"]]
        dtype = {_FLOAT: "<f4", _UBYTE: "u1", _USHORT: "<u2", 5125: "<u4", 5120: "i1", 5122: "<i2"}[
            acc["componentType"]
        ]
        width = {"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4, "MAT4": 16}[acc["type"]]
        item = np.dtype(dtype).itemsize * width
        stride = view.get("byteStride", item)
        start = view.get("byteOffset", 0) + acc.get("byteOffset", 0)
        count = acc["count"]
        if stride == item:
            flat = np.frombuffer(self.binary, dtype=dtype, count=width * count, offset=start)
        else:
            flat = np.concatenate(
                [
                    np.frombuffer(self.binary, dtype=dtype, count=width, offset=start + i * stride)
                    for i in range(count)
                ]
            )
        out: NDArray[Any] = flat.reshape(count, width)
        if acc.get("normalized") and acc["componentType"] in (_UBYTE, _USHORT):
            out = out / np.iinfo(out.dtype).max
        return out

    def pack(self) -> bytes:
        while len(self.binary) % 4:
            self.binary.append(0)
        if self.doc.get("buffers"):
            self.doc["buffers"][0]["byteLength"] = len(self.binary)
        else:
            self.doc["buffers"] = [{"byteLength": len(self.binary)}]
        raw = json.dumps(self.doc, separators=(",", ":"), allow_nan=False).encode()
        raw += b" " * (-len(raw) % 4)
        total = 12 + 8 + len(raw) + 8 + len(self.binary)
        return (
            struct.pack("<III", 0x46546C67, 2, total)
            + struct.pack("<II", len(raw), _JSON)
            + raw
            + struct.pack("<II", len(self.binary), _BIN)
            + bytes(self.binary)
        )


def with_skin(glb: bytes, skeleton: Skeleton, skins: dict[str, VertexSkin], name: str = "") -> bytes:
    """Adds a skeleton to a GLB and binds each named mesh to it with its per-vertex weights."""
    g = _GLB.parse(glb)
    doc = g.doc
    nodes: list[dict[str, Any]] = doc.setdefault("nodes", [])
    order = skeleton.ordered()
    node_of = {j: len(nodes) + i for i, j in enumerate(order)}
    heads = np.asarray(skeleton.heads, dtype=np.float64)
    for j in order:
        p = skeleton.parents[j]
        local = heads[j] - (heads[p] if p >= 0 else 0.0)
        node: dict[str, Any] = {"name": skeleton.names[j]}
        if np.any(np.abs(local) > 0):
            node["translation"] = [round(float(v), 6) for v in local]
        kids = [node_of[c] for c in order if skeleton.parents[c] == j]
        if kids:
            node["children"] = kids
        nodes.append(node)
    # Joints in skin order are skeleton order, so the weights' joint indices need no remapping.
    inverse_bind = np.zeros((len(skeleton), 16), dtype="<f4")
    inverse_bind[:, [0, 5, 10, 15]] = 1.0
    inverse_bind[:, 12:15] = -heads
    skin: dict[str, Any] = {
        "joints": [node_of[j] for j in range(len(skeleton))],
        "inverseBindMatrices": g.add(inverse_bind, _FLOAT, "MAT4"),
        "extras": {"forja_skeleton": skeleton.profile},
    }
    if name:
        skin["name"] = f"{name}_Skeleton"
    if len(skeleton.roots) == 1:
        skin["skeleton"] = node_of[skeleton.roots[0]]
    doc.setdefault("skins", []).append(skin)
    skin_index = len(doc["skins"]) - 1
    scene = doc["scenes"][doc.get("scene", 0)]
    scene.setdefault("nodes", []).extend(node_of[r] for r in skeleton.roots)

    component = _UBYTE if len(skeleton) <= 256 else _USHORT
    for mesh_index, mesh in enumerate(doc.get("meshes", [])):
        vs = skins.get(mesh.get("name", ""))
        if vs is None:
            continue
        for prim in mesh["primitives"]:
            count = doc["accessors"][prim["attributes"]["POSITION"]]["count"]
            if len(vs) != count:
                raise ValueError(
                    f"mesh {mesh.get('name')}: {len(vs)} skinned vertices for {count} in the file"
                )
            joints = vs.joints.astype("u1" if component == _UBYTE else "<u2")
            prim["attributes"]["JOINTS_0"] = g.add(joints, component, "VEC4")
            prim["attributes"]["WEIGHTS_0"] = g.add(vs.weights.astype("<f4"), _FLOAT, "VEC4")
        for node in nodes:
            if node.get("mesh") == mesh_index:
                node["skin"] = skin_index
    return g.pack()


def take(scene: trimesh.Scene) -> tuple[dict[str, VertexSkin], Skeleton | None, Callable[[], None]]:
    """Lifts the skin out of a scene's metadata for export, and a function that puts it back.

    trimesh writes metadata into the file as JSON extras, which arrays would bloat and objects
    would break.
    """
    skins = {n: g.metadata.pop(SKIN) for n, g in scene.geometry.items() if SKIN in g.metadata}
    owners: list[dict[str, Any]] = [scene.metadata, *(g.metadata for g in scene.geometry.values())]
    held = [(m, m.pop(SKELETON)) for m in owners if SKELETON in m]

    def restore() -> None:
        for n, vs in skins.items():
            scene.geometry[n].metadata[SKIN] = vs
        for m, sk in held:
            m[SKELETON] = sk

    return skins, held[0][1] if held else None, restore


def attach(mesh: trimesh.Trimesh, path: Path) -> trimesh.Trimesh:
    """Puts the skin of the GLB `mesh` was loaded from back on it, when the file has one.

    The weights follow the file's vertex order, which loading keeps for a single mesh.
    """
    skeleton, skins = read(path)
    if skeleton is None or not skins:
        return mesh
    weights = list(skins.values())
    joined = VertexSkin(
        np.concatenate([w.joints for w in weights]), np.concatenate([w.weights for w in weights])
    )
    if len(joined) != len(mesh.vertices):
        raise ValueError(f"{path}: {len(joined)} skinned vertices for a mesh of {len(mesh.vertices)}")
    mesh.metadata[SKIN] = joined
    mesh.metadata[SKELETON] = skeleton
    return mesh


def read(path: Path) -> tuple[Skeleton | None, dict[str, VertexSkin]]:
    """The first skin of a GLB and the weights of each mesh bound to it, by mesh name."""
    g = _GLB.parse(path.read_bytes())
    doc = g.doc
    skins = doc.get("skins") or []
    if not skins:
        return None, {}
    skin = skins[0]
    joints: list[int] = skin["joints"]
    nodes = doc["nodes"]
    parent_node = {c: i for i, n in enumerate(nodes) for c in n.get("children", [])}
    index_of = {node: j for j, node in enumerate(joints)}
    world: dict[int, NDArray[np.float64]] = {}

    def head(node: int) -> NDArray[np.float64]:
        if node not in world:
            t = np.asarray(nodes[node].get("translation", [0.0, 0.0, 0.0]), dtype=np.float64)
            up = parent_node.get(node)
            world[node] = t + (head(up) if up is not None else 0.0)
        return world[node]

    names = []
    for j, node in enumerate(joints):
        names.append(str(nodes[node].get("name") or f"joint_{j}"))
    parents = []
    for node in joints:
        up = parent_node.get(node)
        while up is not None and up not in index_of:
            up = parent_node.get(up)
        parents.append(index_of[up] if up is not None else -1)
    heads = np.array([head(n) for n in joints], dtype=np.float64).reshape(len(joints), 3)
    if "inverseBindMatrices" in skin:
        # Rest positions are exact in the inverse bind matrices, whatever the nodes hold.
        ibm = g.read(skin["inverseBindMatrices"]).reshape(-1, 4, 4).transpose(0, 2, 1)
        heads = np.linalg.inv(ibm.astype(np.float64))[:, :3, 3]
    profile = (skin.get("extras") or {}).get("forja_skeleton", GENERIC)
    skeleton = Skeleton(
        tuple(names), tuple(parents), heads, profile if profile in (HUMANOID, GENERIC) else GENERIC
    )
    out: dict[str, VertexSkin] = {}
    bound = {n["mesh"] for n in nodes if n.get("skin") == 0 and "mesh" in n}
    for mesh_index, mesh in enumerate(doc.get("meshes", [])):
        if mesh_index not in bound:
            continue
        parts_j, parts_w = [], []
        for prim in mesh["primitives"]:
            attrs = prim["attributes"]
            if "JOINTS_0" not in attrs or "WEIGHTS_0" not in attrs:
                break
            parts_j.append(g.read(attrs["JOINTS_0"]).astype(np.int64))
            parts_w.append(g.read(attrs["WEIGHTS_0"]).astype(np.float32))
        else:
            out[str(mesh.get("name", mesh_index))] = VertexSkin(
                np.concatenate(parts_j), np.concatenate(parts_w)
            )
    return skeleton, out
