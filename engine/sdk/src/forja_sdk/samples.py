"""Small, deterministic stage inputs, so an adapter can be exercised without real data."""

from __future__ import annotations

import hashlib
from pathlib import Path

from forja_sdk.artifacts import write_glb, write_png
from forja_sdk.types import ArtifactKind, StageInput, StageType

BACKDROP = (232, 232, 232)


def subject_png(path: Path, alpha: bool, size: int = 256) -> Path:
    """A chest-like subject: a lid (half ellipse) over a box, shaded top to bottom.

    With alpha, the backdrop is transparent, as after background removal.
    """
    px = bytearray()
    cx, lid_y, rx, ry = size / 2, size * 0.42, size * 0.32, size * 0.16
    box = (size * 0.18, size * 0.42, size * 0.82, size * 0.82)
    for y in range(size):
        shade = 1.0 - 0.4 * y / size
        for x in range(size):
            in_lid = y <= lid_y and ((x - cx) / rx) ** 2 + ((y - lid_y) / ry) ** 2 <= 1.0
            in_box = box[0] <= x <= box[2] and box[1] <= y <= box[3]
            if in_lid or in_box:
                band = 40 if int(x / size * 8) % 2 else 0  # planks, so painting has detail
                rgb = (int((170 + band) * shade), int((90 + band // 2) * shade), int(40 * shade))
                px += bytes((*rgb, 255)) if alpha else bytes(rgb)
            else:
                px += bytes((*BACKDROP, 0)) if alpha else bytes(BACKDROP)
    return write_png(path, size, size, bytes(px), alpha=alpha)


def checker_png(path: Path, size: int = 64) -> Path:
    px = bytearray()
    for y in range(size):
        for x in range(size):
            px += bytes((200, 60, 60, 255)) if (x // 8 + y // 8) % 2 else bytes((240, 220, 200, 255))
    return write_png(path, size, size, bytes(px))


# (normal axis, sign) -> the two in-plane axes whose cross product is the outward normal.
_FACES = (
    ((0, 1), (1, 2)),
    ((0, -1), (2, 1)),
    ((1, 1), (2, 0)),
    ((1, -1), (0, 2)),
    ((2, 1), (0, 1)),
    ((2, -1), (1, 0)),
)
_CORNERS = ((-1, -1), (1, -1), (1, 1), (-1, 1))


def box_glb(path: Path, textured: bool = False, half: tuple[float, float, float] = (0.3, 0.4, 0.15)) -> Path:
    """A closed box with outward faces and one UV island per face (a 3 x 2 atlas)."""
    positions: list[tuple[float, float, float]] = []
    uvs: list[tuple[float, float]] = []
    faces: list[tuple[int, int, int]] = []
    for i, ((axis, sign), (u, v)) in enumerate(_FACES):
        base = len(positions)
        col, row = i % 3, i // 3
        for cu, cv in _CORNERS:
            p = [0.0, 0.0, 0.0]
            p[axis], p[u], p[v] = sign * half[axis], cu * half[u], cv * half[v]
            positions.append((p[0], p[1], p[2]))
            uvs.append(((col + 0.05 + 0.45 * (cu + 1)) / 3, (row + 0.05 + 0.45 * (cv + 1)) / 2))
        faces += [(base, base + 1, base + 2), (base, base + 2, base + 3)]
    texture = checker_png(path.with_suffix(".texture.png")).read_bytes() if textured else None
    return write_glb(path, positions, faces, uvs=uvs, texture_png=texture)


def _input(path: Path, kind: ArtifactKind, view: str | None = None) -> StageInput:
    return StageInput(path, kind, "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest(), view)


def inputs_for(task: StageType | str, folder: Path) -> list[StageInput] | None:
    """Inputs shaped like the ones the pipeline sends a stage of this type; None when unknown."""
    folder.mkdir(parents=True, exist_ok=True)
    png, glb = ArtifactKind.IMAGE_PNG, ArtifactKind.MESH_GLB
    photo = lambda: _input(subject_png(folder / "photo.png", alpha=False), png)  # noqa: E731
    cutout = lambda: _input(subject_png(folder / "cutout.png", alpha=True), png)  # noqa: E731
    mesh = lambda: _input(box_glb(folder / "mesh.glb"), glb)  # noqa: E731
    painted = lambda: _input(box_glb(folder / "painted.glb", textured=True), glb)  # noqa: E731
    recipes = {
        StageType.BACKGROUND_REMOVAL: lambda: [photo()],
        StageType.FRAMING: lambda: [cutout()],
        StageType.VIEWS_DETECT: lambda: [cutout()],
        StageType.SHAPE: lambda: [cutout()],
        StageType.MESH_CLEANUP: lambda: [mesh()],
        StageType.MESH_DECIMATE: lambda: [mesh()],
        StageType.MESH_UV: lambda: [mesh()],
        StageType.TEXTURE: lambda: [mesh(), cutout()],
        StageType.TEXTURE_MATCH: lambda: [painted(), cutout()],
        StageType.BAKE: lambda: [painted(), mesh()],
        StageType.MESH_LODS: lambda: [painted()],
        StageType.EXPORT: lambda: [painted()],
        StageType.RIG: lambda: [painted()],
    }
    try:
        recipe = recipes.get(StageType(task))
    except ValueError:
        return None
    return recipe() if recipe else None
