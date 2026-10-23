"""Mesh primitives shared by built-in stages and stub models. CPU only, numpy vectorized."""

from __future__ import annotations

import json
import struct
from pathlib import Path

import fast_simplification
import numpy as np
import trimesh
import xatlas
from numpy.typing import NDArray
from PIL import Image

TANGENTS = "tangents"  # Trimesh.metadata key: (n, 4) per-vertex tangents written as TANGENT


def load(path: Path) -> trimesh.Trimesh:
    mesh = trimesh.load(path, file_type="glb", force="mesh", process=False)
    if not isinstance(mesh, trimesh.Trimesh):
        raise ValueError(f"{path} does not contain a triangle mesh")
    # trimesh reads glTF extras into metadata but not the TANGENT attribute. Cached files from
    # earlier builds still carry tangents in extras; they go stale once a stage edits the mesh.
    mesh.metadata.pop(TANGENTS, None)
    return mesh


def save(mesh: trimesh.Trimesh | trimesh.Scene, path: Path) -> Path:
    """Write a GLB with smooth vertex normals, plus tangents for meshes that carry them.

    Without NORMAL, glTF importers fall back to flat shading; without TANGENT, they
    regenerate tangents that may not match the basis a normal map was baked in.
    """
    scene = _as_scene(mesh)
    # trimesh writes metadata into the glTF JSON as extras: tangents there would double the
    # payload as text, and a single NaN would make the whole file invalid JSON.
    tangents = {
        name: g.metadata.pop(TANGENTS) for name, g in scene.geometry.items() if TANGENTS in g.metadata
    }
    try:
        glb = trimesh.exchange.gltf.export_glb(scene, include_normals=True)
    finally:
        for name, tan in tangents.items():
            scene.geometry[name].metadata[TANGENTS] = tan
    path.write_bytes(_with_tangents(glb, tangents) if tangents else glb)
    return path


def _with_tangents(glb: bytes, tangents: dict[str, NDArray[np.float32]]) -> bytes:
    json_len = struct.unpack_from("<I", glb, 12)[0]
    doc = json.loads(glb[20 : 20 + json_len])
    bin_start = 20 + json_len + 8
    binary = bytearray(glb[bin_start : bin_start + struct.unpack_from("<I", glb, 20 + json_len)[0]])
    for mesh in doc.get("meshes", []):
        tan = tangents.get(mesh.get("name", ""))
        if tan is None:
            continue
        for prim in mesh["primitives"]:
            count = doc["accessors"][prim["attributes"]["POSITION"]]["count"]
            if len(tan) != count:
                continue  # vertex order changed on export; engines regenerate tangents instead
            while len(binary) % 4:
                binary.append(0)
            data = np.ascontiguousarray(tan, dtype="<f4").tobytes()
            doc["bufferViews"].append({"buffer": 0, "byteOffset": len(binary), "byteLength": len(data)})
            binary.extend(data)
            doc["accessors"].append(
                {
                    "bufferView": len(doc["bufferViews"]) - 1,
                    "componentType": 5126,
                    "count": count,
                    "type": "VEC4",
                }
            )
            prim["attributes"]["TANGENT"] = len(doc["accessors"]) - 1
    while len(binary) % 4:
        binary.append(0)
    doc["buffers"][0]["byteLength"] = len(binary)
    raw = json.dumps(doc, separators=(",", ":"), allow_nan=False).encode()
    raw += b" " * (-len(raw) % 4)
    total = 12 + 8 + len(raw) + 8 + len(binary)
    return (
        struct.pack("<III", 0x46546C67, 2, total)
        + struct.pack("<II", len(raw), 0x4E4F534A)
        + raw
        + struct.pack("<II", len(binary), 0x004E4942)
        + bytes(binary)
    )


def vertex_tangents(mesh: trimesh.Trimesh, uv: NDArray[np.float64]) -> NDArray[np.float32]:
    """Per-vertex tangents (xyz) and handedness (w) from UV gradients, glTF convention.

    Tangent follows +u and bitangent = cross(normal, tangent) * w follows +v in trimesh's
    bottom-left UV space, i.e. "up" in the texture image, which is what glTF expects.
    """
    v = np.asarray(mesh.vertices, dtype=np.float64)
    f = np.asarray(mesh.faces)
    n = np.asarray(mesh.vertex_normals, dtype=np.float64)
    # A vertex with only zero-area faces, or faces that cancel out, has no normal. Any frame
    # suits it; without one the fallback below divides 0 by 0 and writes NaN.
    n_len = np.linalg.norm(n, axis=1, keepdims=True)
    n = np.where(n_len > 1e-12, n / np.maximum(n_len, 1e-12), [[0.0, 0.0, 1.0]])
    p0, p1, p2 = v[f[:, 0]], v[f[:, 1]], v[f[:, 2]]
    w0, w1, w2 = uv[f[:, 0]], uv[f[:, 1]], uv[f[:, 2]]
    e1, e2 = p1 - p0, p2 - p0
    d1, d2 = w1 - w0, w2 - w0
    det = d1[:, 0] * d2[:, 1] - d2[:, 0] * d1[:, 1]
    r = np.where(np.abs(det) > 1e-12, 1.0 / np.where(det == 0, 1, det), 0.0)[:, None]
    t_face = (e1 * d2[:, 1:2] - e2 * d1[:, 1:2]) * r
    b_face = (e2 * d1[:, 0:1] - e1 * d2[:, 0:1]) * r
    t = np.zeros_like(v)
    b = np.zeros_like(v)
    for k in range(3):
        np.add.at(t, f[:, k], t_face)
        np.add.at(b, f[:, k], b_face)
    t = t - n * np.sum(n * t, axis=1, keepdims=True)  # Gram-Schmidt against the normal
    length = np.linalg.norm(t, axis=1, keepdims=True)
    fallback = np.cross(n, np.where(np.abs(n[:, :1]) < 0.9, [[1.0, 0, 0]], [[0, 1.0, 0]]))
    t = np.where(
        length > 1e-12, t / np.maximum(length, 1e-12), fallback / np.linalg.norm(fallback, axis=1)[:, None]
    )
    w = np.where(np.sum(np.cross(n, t) * b, axis=1) < 0, -1.0, 1.0)
    return np.hstack([t, w[:, None]]).astype(np.float32)


def _as_scene(mesh: trimesh.Trimesh | trimesh.Scene) -> trimesh.Scene:
    return mesh if isinstance(mesh, trimesh.Scene) else trimesh.Scene(mesh)


def cleanup(mesh: trimesh.Trimesh, min_component_ratio: float = 0.01) -> trimesh.Trimesh:
    """Drop floaters, merge duplicate vertices, drop degenerate faces, make normals consistent."""
    mesh = mesh.copy()
    mesh.merge_vertices()  # keeps UV seams, so a textured mesh keeps its texture
    mesh.update_faces(mesh.nondegenerate_faces())
    mesh.remove_unreferenced_vertices()
    # Parts and winding are worked out on a copy welded by position only: UV seams must not
    # split a textured mesh into islands, each turned its own way. Both then apply by face,
    # which leaves the texture alone.
    welded = trimesh.Trimesh(mesh.vertices, mesh.faces, process=False)
    welded.merge_vertices(merge_tex=True, merge_norm=True)
    labels = trimesh.graph.connected_component_labels(welded.face_adjacency, node_count=len(welded.faces))
    if len(labels) and labels.max() > 0:
        areas = np.bincount(labels, weights=welded.area_faces)
        keep = (areas >= areas.max() * min_component_ratio)[labels]
        mesh.update_faces(keep)
        mesh.remove_unreferenced_vertices()
        welded.update_faces(keep)
    before = welded.faces.copy()
    trimesh.repair.fix_normals(welded)
    flipped = (welded.faces != before).any(axis=1)
    if flipped.any():
        faces = mesh.faces.copy()
        faces[flipped] = faces[flipped, ::-1]
        mesh.faces = faces
    return mesh


def decimate(mesh: trimesh.Trimesh, target_tris: int) -> trimesh.Trimesh:
    faces = len(mesh.faces)
    if faces <= target_tris:
        return mesh.copy()
    reduction = 1.0 - target_tris / faces
    verts, tris = fast_simplification.simplify(
        np.asarray(mesh.vertices, dtype=np.float32), np.asarray(mesh.faces, dtype=np.int32), reduction
    )
    out = trimesh.Trimesh(verts, tris, process=True)
    # Collapses leave zero-area slivers behind; they shade as nothing and can leave a vertex
    # with no normal to build a tangent frame from.
    out.update_faces(out.nondegenerate_faces())
    out.remove_unreferenced_vertices()
    return out


def unwrap(mesh: trimesh.Trimesh) -> trimesh.Trimesh:
    """xatlas UV unwrap. Returns a mesh whose vertices are split along seams, with UVs attached."""
    vmapping, indices, uvs = xatlas.parametrize(
        np.asarray(mesh.vertices, dtype=np.float32), np.asarray(mesh.faces, dtype=np.uint32)
    )
    out = trimesh.Trimesh(mesh.vertices[vmapping], indices, process=False)
    out.visual = trimesh.visual.TextureVisuals(uv=uvs)
    return weld_normals(out)


def weld_normals(mesh: trimesh.Trimesh, digits: int = 6) -> trimesh.Trimesh:
    """Smooth vertex normals that ignore UV seams.

    Unwrapping splits vertices along seams; normals computed per split vertex then differ on
    each side and show as hard lines. Vertices at the same position share one normal here.
    """
    _, inverse = trimesh.grouping.unique_rows(np.asarray(mesh.vertices), digits=digits)
    faces = np.asarray(mesh.faces)
    weighted = mesh.face_normals * mesh.area_faces[:, None]
    acc = np.zeros((int(inverse.max()) + 1, 3))
    for k in range(3):
        np.add.at(acc, inverse[faces[:, k]], weighted)
    acc /= np.maximum(np.linalg.norm(acc, axis=1, keepdims=True), 1e-12)
    mesh.vertex_normals = acc[inverse]  # type: ignore[method-assign]  # trimesh property setter
    return mesh


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
    tex = dilate(tex, covered, iterations=4)
    return Image.fromarray((np.clip(tex, 0, 1) * 255).astype(np.uint8), "RGB")


def dilate(tex: NDArray[np.float32], covered: NDArray[np.bool_], iterations: int) -> NDArray[np.float32]:
    """Grow covered texels outward so filtering never samples empty texels across UV seams."""
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
    return weld_normals(out)


def stats(mesh: trimesh.Trimesh) -> dict[str, float | int | bool]:
    return {
        "triangles": len(mesh.faces),
        "vertices": len(mesh.vertices),
        "watertight": bool(mesh.is_watertight),
        "extent_m": [round(float(v), 4) for v in mesh.extents],  # type: ignore[dict-item]
    }
