"""Read and write the artifact kinds stages exchange, with the standard library only.

The validators check structure, not looks: a PNG whose chunks, CRCs and pixel data add up,
a GLB whose buffers, accessors and indices stay in bounds. They are what the conformance
suite holds every stage output to. The writers make small inputs for tests and simple
adapters; real adapters usually write through their own libraries.
"""

from __future__ import annotations

import json
import math
import struct
import zlib
from array import array
from collections.abc import Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from forja_sdk.types import ArtifactKind


class InvalidArtifact(ValueError):
    """The file is not a valid artifact of the kind it claims to be."""


# --- PNG ----------------------------------------------------------------------------------

_PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"
# Color type -> (channels, allowed bit depths).
_PNG_COLOR_TYPES = {
    0: (1, (1, 2, 4, 8, 16)),
    2: (3, (8, 16)),
    3: (1, (1, 2, 4, 8)),
    4: (2, (8, 16)),
    6: (4, (8, 16)),
}


@dataclass(frozen=True)
class ImageInfo:
    width: int
    height: int
    channels: int
    bit_depth: int
    has_alpha: bool


def write_png(path: Path, width: int, height: int, pixels: bytes, alpha: bool = True) -> Path:
    """Write 8-bit RGBA (or RGB with alpha=False) pixels, row by row from the top."""
    channels = 4 if alpha else 3
    row = width * channels
    if len(pixels) != row * height:
        raise ValueError(f"expected {row * height} bytes of pixels, got {len(pixels)}")
    raw = b"".join(b"\x00" + pixels[y * row : (y + 1) * row] for y in range(height))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6 if alpha else 2, 0, 0, 0)
    path.write_bytes(
        _PNG_SIGNATURE + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b"")
    )
    return path


def read_png_info(data: bytes) -> ImageInfo:
    if not data.startswith(_PNG_SIGNATURE):
        raise InvalidArtifact("not a PNG: wrong signature")
    pos = len(_PNG_SIGNATURE)
    header: tuple[int, ...] | None = None
    idat = bytearray()
    has_trns = has_plte = ended = False
    while pos < len(data):
        if pos + 8 > len(data):
            raise InvalidArtifact("PNG ends inside a chunk header")
        (length,) = struct.unpack(">I", data[pos : pos + 4])
        kind = data[pos + 4 : pos + 8]
        body = data[pos + 8 : pos + 8 + length]
        if len(body) != length or pos + 12 + length > len(data):
            raise InvalidArtifact(f"PNG chunk {kind!r} runs past the end of the file")
        (crc,) = struct.unpack(">I", data[pos + 8 + length : pos + 12 + length])
        if zlib.crc32(kind + body) != crc:
            raise InvalidArtifact(f"PNG chunk {kind!r} has a bad CRC")
        pos += 12 + length
        if header is None and kind != b"IHDR":
            raise InvalidArtifact("PNG does not start with IHDR")
        if kind == b"IHDR":
            if length != 13:
                raise InvalidArtifact("PNG IHDR has the wrong size")
            header = struct.unpack(">IIBBBBB", body)
        elif kind == b"IDAT":
            idat += body
        elif kind == b"PLTE":
            has_plte = True
        elif kind == b"tRNS":
            has_trns = True
        elif kind == b"IEND":
            ended = True
            break
    if header is None or not ended:
        raise InvalidArtifact("PNG has no IHDR or no IEND")
    width, height, depth, color, _compression, _filter, interlace = header
    if width == 0 or height == 0:
        raise InvalidArtifact("PNG has zero width or height")
    if color not in _PNG_COLOR_TYPES or depth not in _PNG_COLOR_TYPES[color][1]:
        raise InvalidArtifact(f"PNG has an invalid color type {color} at bit depth {depth}")
    if color == 3 and not has_plte:
        raise InvalidArtifact("palette PNG has no PLTE chunk")
    if not idat:
        raise InvalidArtifact("PNG has no pixel data")
    try:
        raw = zlib.decompress(bytes(idat))
    except zlib.error as e:
        raise InvalidArtifact(f"PNG pixel data does not decompress: {e}") from e
    channels = _PNG_COLOR_TYPES[color][0]
    if interlace == 0:
        row = (width * channels * depth + 7) // 8
        if len(raw) != height * (row + 1):
            raise InvalidArtifact(f"PNG pixel data is {len(raw)} bytes, expected {height * (row + 1)}")
    return ImageInfo(width, height, channels, depth, color in (4, 6) or has_trns)


# --- GLB ----------------------------------------------------------------------------------

_GLB_MAGIC = b"glTF"
_CHUNK_JSON = 0x4E4F534A
_CHUNK_BIN = 0x004E4942
_COMPONENT_BYTES = {5120: 1, 5121: 1, 5122: 2, 5123: 2, 5125: 4, 5126: 4}
_INDEX_FORMATS = {5121: "B", 5123: "H", 5125: "I"}
_TYPE_SIZES = {"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4, "MAT2": 4, "MAT3": 9, "MAT4": 16}
_FLOAT = 5126
_MESHOPT = "EXT_meshopt_compression"


@dataclass(frozen=True)
class MeshInfo:
    triangles: int
    vertices: int
    primitives: int
    has_uv: bool
    has_normals: bool
    materials: int
    textured: bool  # some material samples a base color texture
    extensions: tuple[str, ...] = field(default_factory=tuple)


def write_glb(
    path: Path,
    positions: Sequence[Sequence[float]],
    faces: Sequence[Sequence[int]],
    uvs: Sequence[Sequence[float]] | None = None,
    texture_png: bytes | None = None,
) -> Path:
    """Write one triangle mesh; with texture_png (and uvs), also a base color material."""
    if texture_png is not None and uvs is None:
        raise ValueError("a texture needs uvs")
    flat_pos = [float(c) for p in positions for c in p]
    flat_idx = [int(i) for f in faces for i in f]
    blobs: list[bytes] = []
    views: list[dict[str, Any]] = []
    accessors: list[dict[str, Any]] = []
    offset = 0

    def add(blob: bytes, target: int | None = None) -> int:
        nonlocal offset
        view: dict[str, Any] = {"buffer": 0, "byteOffset": offset, "byteLength": len(blob)}
        if target is not None:
            view["target"] = target
        blobs.append(blob + b"\x00" * (-len(blob) % 4))
        offset += len(blobs[-1])
        views.append(view)
        return len(views) - 1

    n = len(positions)
    mins = [min(flat_pos[i::3]) for i in range(3)] if n else [0.0] * 3
    maxs = [max(flat_pos[i::3]) for i in range(3)] if n else [0.0] * 3
    pos_view = add(struct.pack(f"<{len(flat_pos)}f", *flat_pos), 34962)
    accessors.append(
        {
            "bufferView": pos_view,
            "componentType": _FLOAT,
            "count": n,
            "type": "VEC3",
            "min": mins,
            "max": maxs,
        }
    )
    attributes: dict[str, int] = {"POSITION": 0}
    if uvs is not None:
        flat_uv = [float(c) for uv in uvs for c in uv]
        uv_view = add(struct.pack(f"<{len(flat_uv)}f", *flat_uv), 34962)
        accessors.append({"bufferView": uv_view, "componentType": _FLOAT, "count": len(uvs), "type": "VEC2"})
        attributes["TEXCOORD_0"] = len(accessors) - 1
    idx_view = add(struct.pack(f"<{len(flat_idx)}I", *flat_idx), 34963)
    accessors.append(
        {"bufferView": idx_view, "componentType": 5125, "count": len(flat_idx), "type": "SCALAR"}
    )
    primitive: dict[str, Any] = {"attributes": attributes, "indices": len(accessors) - 1, "mode": 4}
    doc: dict[str, Any] = {
        "asset": {"version": "2.0", "generator": "forja-sdk"},
        "scene": 0,
        "scenes": [{"nodes": [0]}],
        "nodes": [{"mesh": 0}],
        "meshes": [{"primitives": [primitive]}],
        "accessors": accessors,
        "bufferViews": views,
    }
    if texture_png is not None:
        image_view = add(texture_png)
        doc["images"] = [{"bufferView": image_view, "mimeType": "image/png"}]
        doc["samplers"] = [{}]
        doc["textures"] = [{"source": 0, "sampler": 0}]
        doc["materials"] = [
            {"pbrMetallicRoughness": {"baseColorTexture": {"index": 0}, "metallicFactor": 0.0}}
        ]
        primitive["material"] = 0
    binary = b"".join(blobs)
    doc["buffers"] = [{"byteLength": len(binary)}]
    text = json.dumps(doc, separators=(",", ":")).encode()
    text += b" " * (-len(text) % 4)
    total = 12 + 8 + len(text) + 8 + len(binary)
    path.write_bytes(
        _GLB_MAGIC
        + struct.pack("<II", 2, total)
        + struct.pack("<II", len(text), _CHUNK_JSON)
        + text
        + struct.pack("<II", len(binary), _CHUNK_BIN)
        + binary
    )
    return path


def _index(doc: dict[str, Any], key: str, i: Any, what: str) -> dict[str, Any]:
    items = doc.get(key) or []
    if not isinstance(i, int) or not 0 <= i < len(items):
        raise InvalidArtifact(f"GLB {what} points at {key}[{i}], which does not exist")
    item = items[i]
    if not isinstance(item, dict):
        raise InvalidArtifact(f"GLB {key}[{i}] is not an object")
    return item


def read_glb_info(data: bytes) -> MeshInfo:
    if len(data) < 20 or data[:4] != _GLB_MAGIC:
        raise InvalidArtifact("not a GLB: wrong magic")
    version, total = struct.unpack("<II", data[4:12])
    if version != 2:
        raise InvalidArtifact(f"GLB version {version}; only glTF 2.0 is supported")
    if total != len(data):
        raise InvalidArtifact(f"GLB header says {total} bytes, the file has {len(data)}")
    chunks: list[tuple[int, bytes]] = []
    pos = 12
    while pos < total:
        if pos + 8 > total:
            raise InvalidArtifact("GLB ends inside a chunk header")
        length, kind = struct.unpack("<II", data[pos : pos + 8])
        if length % 4 or pos + 8 + length > total:
            raise InvalidArtifact("GLB chunk is misaligned or runs past the end of the file")
        chunks.append((kind, data[pos + 8 : pos + 8 + length]))
        pos += 8 + length
    if not chunks or chunks[0][0] != _CHUNK_JSON:
        raise InvalidArtifact("GLB does not start with a JSON chunk")
    try:
        doc = json.loads(chunks[0][1])
    except ValueError as e:
        raise InvalidArtifact(f"GLB JSON does not parse: {e}") from e
    if not isinstance(doc, dict) or (doc.get("asset") or {}).get("version") != "2.0":
        raise InvalidArtifact("GLB JSON has no asset.version 2.0")
    binary = chunks[1][1] if len(chunks) > 1 and chunks[1][0] == _CHUNK_BIN else b""

    # EXT_meshopt_compression adds a fallback buffer with no bytes: its views are decoded at
    # load time from compressed bytes elsewhere, so only their bounds can be checked here.
    virtual: set[int] = set()
    for i, buf in enumerate(doc.get("buffers") or []):
        if "uri" in buf:
            raise InvalidArtifact(
                f"GLB buffers[{i}] points at an external file; artifacts must be self-contained"
            )
        if ((buf.get("extensions") or {}).get(_MESHOPT) or {}).get("fallback"):
            virtual.add(i)
        elif i > 0:
            raise InvalidArtifact(f"GLB buffers[{i}] has no data: only the first buffer is the binary chunk")
        elif int(buf.get("byteLength", 0)) > len(binary):
            raise InvalidArtifact(f"GLB buffers[{i}] is larger than the binary chunk")
    for i, view in enumerate(doc.get("bufferViews") or []):
        buf = _index(doc, "buffers", view.get("buffer"), f"bufferViews[{i}]")
        if int(view.get("byteOffset", 0)) + int(view.get("byteLength", -1)) > int(buf["byteLength"]):
            raise InvalidArtifact(f"GLB bufferViews[{i}] runs past its buffer")
        if packed := (view.get("extensions") or {}).get(_MESHOPT):
            real = _index(doc, "buffers", packed.get("buffer"), f"bufferViews[{i}] (compressed)")
            if int(packed.get("byteOffset", 0)) + int(packed.get("byteLength", -1)) > int(real["byteLength"]):
                raise InvalidArtifact(f"GLB bufferViews[{i}] compressed bytes run past their buffer")

    def readable(acc: dict[str, Any]) -> bool:
        if "bufferView" not in acc:
            return False
        view = doc["bufferViews"][acc["bufferView"]]
        return view.get("buffer") not in virtual and _MESHOPT not in (view.get("extensions") or {})

    def accessor(i: Any, what: str) -> dict[str, Any]:
        acc = _index(doc, "accessors", i, what)
        comp, kind, count = acc.get("componentType"), acc.get("type"), acc.get("count")
        if comp not in _COMPONENT_BYTES or kind not in _TYPE_SIZES or not isinstance(count, int) or count < 1:
            raise InvalidArtifact(f"GLB accessors[{i}] has a bad type, component type or count")
        if "bufferView" in acc:
            view = _index(doc, "bufferViews", acc["bufferView"], f"accessors[{i}]")
            size = _COMPONENT_BYTES[comp] * _TYPE_SIZES[kind]
            stride = int(view.get("byteStride", size))
            end = int(acc.get("byteOffset", 0)) + stride * (count - 1) + size
            if end > int(view["byteLength"]):
                raise InvalidArtifact(f"GLB accessors[{i}] runs past its bufferView")
        return acc

    def values(acc: dict[str, Any], fmt: str) -> array[Any]:
        view = doc["bufferViews"][acc["bufferView"]]
        start = int(view.get("byteOffset", 0)) + int(acc.get("byteOffset", 0))
        n = acc["count"] * _TYPE_SIZES[acc["type"]]
        out: array[Any] = array(fmt)
        out.frombytes(binary[start : start + n * out.itemsize])
        return out

    meshes = doc.get("meshes") or []
    if not meshes:
        raise InvalidArtifact("GLB has no meshes")
    triangles = vertices = primitives = 0
    has_uv = has_normals = textured = False
    for mi, mesh in enumerate(meshes):
        for pi, prim in enumerate(mesh.get("primitives") or []):
            where = f"meshes[{mi}].primitives[{pi}]"
            attrs = prim.get("attributes") or {}
            if "POSITION" not in attrs:
                raise InvalidArtifact(f"GLB {where} has no POSITION")
            position = accessor(attrs["POSITION"], where)
            if position["type"] != "VEC3" or position["componentType"] != _FLOAT:
                raise InvalidArtifact(f"GLB {where} POSITION is not float VEC3")
            tight = (
                readable(position) and doc["bufferViews"][position["bufferView"]].get("byteStride", 12) == 12
            )
            if tight:
                coords = values(position, "f")
                if any(map(math.isnan, coords)) or any(map(math.isinf, coords)):
                    raise InvalidArtifact(f"GLB {where} has NaN or infinite positions")
            for name in attrs:
                accessor(attrs[name], f"{where}.{name}")
            has_uv |= "TEXCOORD_0" in attrs
            has_normals |= "NORMAL" in attrs
            count = position["count"]
            mode = prim.get("mode", 4)
            if "indices" in prim:
                idx = accessor(prim["indices"], f"{where}.indices")
                if idx["componentType"] not in _INDEX_FORMATS or idx["type"] != "SCALAR":
                    raise InvalidArtifact(f"GLB {where} indices are not unsigned integers")
                ids = values(idx, _INDEX_FORMATS[idx["componentType"]]) if readable(idx) else None
                if ids and max(ids) >= count:
                    raise InvalidArtifact(f"GLB {where} indexes past its {count} vertices")
                n = idx["count"]
            else:
                n = count
            if mode == 4:
                if n % 3:
                    raise InvalidArtifact(f"GLB {where} has {n} indices, not a multiple of 3")
                triangles += n // 3
            elif mode in (5, 6):
                triangles += max(0, n - 2)
            vertices += count
            primitives += 1
            if "material" in prim:
                _index(doc, "materials", prim["material"], where)
    if primitives == 0:
        raise InvalidArtifact("GLB meshes have no primitives")
    for i, mat in enumerate(doc.get("materials") or []):
        tex = (mat.get("pbrMetallicRoughness") or {}).get("baseColorTexture")
        if tex is not None:
            texture = _index(doc, "textures", tex.get("index"), f"materials[{i}]")
            if "source" in texture:
                _index(doc, "images", texture["source"], f"textures[{tex.get('index')}]")
            textured = True
    for i, img in enumerate(doc.get("images") or []):
        if "bufferView" in img:
            _index(doc, "bufferViews", img["bufferView"], f"images[{i}]")
        elif not str(img.get("uri", "")).startswith("data:"):
            raise InvalidArtifact(
                f"GLB images[{i}] points at an external file; artifacts must be self-contained"
            )
    return MeshInfo(
        triangles=triangles,
        vertices=vertices,
        primitives=primitives,
        has_uv=has_uv,
        has_normals=has_normals,
        materials=len(doc.get("materials") or []),
        textured=textured,
        extensions=tuple(doc.get("extensionsUsed") or ()),
    )


# --- any kind -----------------------------------------------------------------------------


def validate(path: Path, kind: ArtifactKind) -> ImageInfo | MeshInfo | Any:
    """Check a file is a valid artifact of `kind`; returns what it learned, raises InvalidArtifact."""
    try:
        data = Path(path).read_bytes()
    except OSError as e:
        raise InvalidArtifact(f"cannot read {path}: {e}") from e
    if kind == ArtifactKind.IMAGE_PNG:
        return read_png_info(data)
    if kind == ArtifactKind.MESH_GLB:
        return read_glb_info(data)
    if kind == ArtifactKind.JSON:
        try:
            return json.loads(data)
        except ValueError as e:
            raise InvalidArtifact(f"not valid JSON: {e}") from e
    raise InvalidArtifact(f"no validator for artifact kind {kind}")


def kind_for(path: Path) -> ArtifactKind:
    """The artifact kind a file name implies."""
    suffix = Path(path).suffix.lower()
    kinds = {".png": ArtifactKind.IMAGE_PNG, ".glb": ArtifactKind.MESH_GLB, ".json": ArtifactKind.JSON}
    if suffix not in kinds:
        raise ValueError(f"{path}: cannot tell the artifact kind from {suffix!r} (want .png, .glb or .json)")
    return kinds[suffix]
