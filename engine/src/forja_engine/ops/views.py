"""Reference sheets: several views of one subject side by side in one image.

Turnaround and concept sheets put a character's front, side and back next to each other, often
with a larger render and close-ups. A shape model reads one picture as one scene, so a sheet fed
whole becomes a group of figures. `detect` finds the figures in the background-removal cut-out
and proposes a view for each; `split` cuts the chosen ones out as images of their own.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import numpy as np
from numpy.typing import NDArray
from PIL import Image
from scipy import ndimage

from forja_engine.ops import image as img_ops

Box = tuple[int, int, int, int]

# A row of N figures, left to right, by the turnaround convention. Three-quarter views fall
# between the turns a multi-view model reads, so a five-view row leaves them out.
ROW_VIEWS: dict[int, tuple[str, ...]] = {
    2: ("front", "back"),
    3: ("front", "left", "back"),
    4: ("front", "left", "back", "right"),
    5: ("front", "", "left", "", "back"),
}
WORK_SIDE = 768  # detection runs on the mask scaled down to this; boxes are scaled back up
MIN_AREA = 0.002  # of the image: captions, sparks and specks are smaller
CUT = 0.5  # a part whose box edge is this solid was cut by a frame: a close-up, not a figure
GROUND = 0.05  # of the image height: figures whose feet are this close stand in one row
SIMILAR_HEIGHT = 0.8
COPIES = 0.85  # silhouettes this alike are copies of one object, not turns of one subject
SAME_VIEW = 0.7  # a figure this alike to one in the row shows the same view at another size


@dataclass
class _Part:
    label: int
    box: Box  # work-scale pixels
    area: int
    cut: float
    silhouette: NDArray[np.bool_]
    view: str = ""

    @property
    def height(self) -> int:
        return self.box[3] - self.box[1]

    @property
    def figure(self) -> bool:
        return self.cut < CUT


def _work_mask(cutout: Image.Image) -> tuple[NDArray[np.bool_], float]:
    alpha = cutout.getchannel("A") if "A" in cutout.getbands() else Image.new("L", cutout.size, 255)
    scale = min(1.0, WORK_SIDE / max(cutout.size))
    if scale < 1.0:
        size = (max(1, round(cutout.width * scale)), max(1, round(cutout.height * scale)))
        alpha = alpha.resize(size, Image.Resampling.BILINEAR)
    return np.asarray(alpha) > 127, scale


def _disk(r: int) -> NDArray[np.bool_]:
    yy, xx = np.ogrid[-r : r + 1, -r : r + 1]
    return (xx * xx + yy * yy) <= r * r


def _labels(mask: NDArray[np.bool_]) -> tuple[NDArray[np.int32], int]:
    """Connected figures, with small gaps closed so tail strands and claws stay attached."""
    r = max(2, round(0.006 * max(mask.shape)))
    padded = np.pad(mask, r)  # closing at the border would otherwise eat into figures there
    closed = ndimage.binary_closing(padded, structure=_disk(r))[r:-r, r:-r]
    labels, n = ndimage.label(closed)
    return labels.astype(np.int32), int(n)


def _edge_solidity(m: NDArray[np.bool_]) -> float:
    """How much of the part's own box edge is solid, a few pixels in (the mask is soft at a frame).

    A free-standing figure touches its box only at extremities (feet, fingertips); a close-up is
    cut by its frame, so one edge is solid almost end to end.
    """
    dy, dx = max(2, m.shape[0] // 50), max(2, m.shape[1] // 50)
    bands = (m[:dy], m[-dy:], m[:, :dx].T, m[:, -dx:].T)
    return max(float(line.mean()) for band in bands for line in band)


def _silhouette(m: NDArray[np.bool_], side: int = 64) -> NDArray[np.bool_]:
    """The mask scaled to a fixed height and centred on a square, for comparing outlines."""
    h, w = m.shape
    sw = max(1, round(w * side / h))
    im = Image.fromarray(m.astype(np.uint8) * 255).resize((sw, side), Image.Resampling.BILINEAR)
    sq = max(side, sw)
    canvas = Image.new("L", (sq, sq))
    canvas.paste(im, ((sq - sw) // 2, (sq - side) // 2))
    return np.asarray(canvas.resize((side, side), Image.Resampling.BILINEAR)) > 127


def _iou(a: NDArray[np.bool_], b: NDArray[np.bool_]) -> float:
    return int((a & b).sum()) / max(1, int((a | b).sum()))


def _parts(mask: NDArray[np.bool_]) -> list[_Part]:
    labels, _ = _labels(mask)
    parts = []
    for i, sl in enumerate(ndimage.find_objects(labels), start=1):
        if sl is None:
            continue
        m = labels[sl] == i
        area = int(m.sum())
        if area < MIN_AREA * mask.size:
            continue
        box = (sl[1].start, sl[0].start, sl[1].stop, sl[0].stop)
        parts.append(_Part(i, box, area, _edge_solidity(m), _silhouette(m)))
    return parts


def _rows(parts: list[_Part], height: int) -> list[list[_Part]]:
    """Parts grouped by the line their bottoms rest on, top row first, each left to right."""
    rows: list[list[_Part]] = []
    for p in sorted(parts, key=lambda p: (p.box[3], p.box[0])):
        if rows and abs(rows[-1][0].box[3] - p.box[3]) <= GROUND * height:
            rows[-1].append(p)
        else:
            rows.append([p])
    for row in rows:
        row.sort(key=lambda p: p.box[0])
    return rows


def _copies(row: list[_Part]) -> bool:
    """True when every figure has the same outline (mirrored or not): a set of props."""
    for i, a in enumerate(row):
        for b in row[i + 1 :]:
            if max(_iou(a.silhouette, b.silhouette), _iou(a.silhouette, b.silhouette[:, ::-1])) < COPIES:
                return False
    return True


def _label_views(parts: list[_Part], height: int) -> bool:
    """Proposes a view for each part of a sheet; returns whether the image is one."""
    figures = [p for p in parts if p.figure]
    rows = _rows(figures, height)
    if not rows:
        return False
    row = max(rows, key=lambda r: (len(r), sum(p.area for p in r)))
    heights = [p.height for p in row]
    if len(row) < 2 or min(heights) / max(heights) < SIMILAR_HEIGHT or _copies(row):
        return False
    names = ROW_VIEWS.get(len(row), ("front",))
    for p, name in zip(row, names, strict=False):
        p.view = name
    # A larger render of a view in the row (the hero image above a turnaround) has more detail.
    for p in figures:
        if p in row:
            continue
        same = [q for q in row if q.view and _iou(p.silhouette, q.silhouette) > SAME_VIEW]
        if not same:
            continue
        best = max(same, key=lambda q: _iou(p.silhouette, q.silhouette))
        if p.height > best.height:
            p.view, best.view = best.view, ""
    return True


def _full_box(box: Box, scale: float, size: tuple[int, int]) -> list[int]:
    x0, y0, x1, y1 = box
    return [
        max(0, int(np.floor(x0 / scale))),
        max(0, int(np.floor(y0 / scale))),
        min(size[0], int(np.ceil(x1 / scale))),
        min(size[1], int(np.ceil(y1 / scale))),
    ]


def detect(cutout: Image.Image) -> dict[str, Any]:
    """Whether a cut-out holds several views of one subject, and where each one is.

    Parts come in reading order with a box in the cut-out's pixels, a kind ("figure" or
    "detail": a close-up cut by its frame) and a proposed view ("" leaves the part out). Views
    are proposals: an outline cannot tell a front from a back, nor which way a side view faces.
    """
    mask, scale = _work_mask(cutout)
    parts = _parts(mask)
    sheet = _label_views(parts, mask.shape[0])
    ordered = [p for row in _rows(parts, mask.shape[0]) for p in row]
    return {
        "sheet": sheet,
        "size": [cutout.width, cutout.height],
        "parts": [
            {
                "box": _full_box(p.box, scale, cutout.size),
                "kind": "figure" if p.figure else "detail",
                "view": p.view if sheet else "",
            }
            for p in ordered
        ],
    }


def _background(original: Image.Image, alpha: NDArray[np.uint8]) -> tuple[int, int, int]:
    """The sheet's backdrop colour: the median of what background removal took away."""
    small = original.convert("RGB")
    small.thumbnail((256, 256))
    a = np.asarray(Image.fromarray(alpha).resize(small.size, Image.Resampling.BILINEAR))
    rgb = np.asarray(small)[a < 16]
    if len(rgb) == 0:
        return (255, 255, 255)
    med = np.median(rgb, axis=0)
    return (int(med[0]), int(med[1]), int(med[2]))


def split(
    original: Image.Image, cutout: Image.Image, boxes: list[Box], margin: float = 0.04
) -> list[Image.Image]:
    """Cuts each box out of the original image, one figure per box.

    Whatever is not that box's figure (a neighbour's outstretched hand, a caption) is painted
    over in the sheet's backdrop colour, so background removal then sees one subject at full
    resolution. An original that already has its own alpha keeps it instead.
    """
    if cutout.size != original.size:
        cutout = cutout.resize(original.size, Image.Resampling.BILINEAR)
    mask, scale = _work_mask(cutout)
    labels, _ = _labels(mask)
    objects = ndimage.find_objects(labels)
    alpha = np.asarray(
        cutout.getchannel("A") if "A" in cutout.getbands() else Image.new("L", cutout.size, 255)
    )
    real_alpha = "A" in original.getbands() and img_ops.has_real_alpha(original)
    backdrop = _background(original, alpha)
    out = []
    for box in boxes:
        x0, y0, x1, y1 = box
        if not (0 <= x0 < x1 <= original.width and 0 <= y0 < y1 <= original.height):
            raise ValueError(f"box {list(box)} is outside the {original.width}x{original.height} image")
        wx0, wy0, wx1, wy1 = x0 * scale, y0 * scale, x1 * scale, y1 * scale
        keep = np.zeros(mask.shape, dtype=bool)
        for i, sl in enumerate(objects, start=1):
            if sl is None:
                continue
            cy, cx = (sl[0].start + sl[0].stop) / 2, (sl[1].start + sl[1].stop) / 2
            if wx0 <= cx <= wx1 and wy0 <= cy <= wy1:
                keep |= labels == i
        mx, my = round((x1 - x0) * margin), round((y1 - y0) * margin)
        region = (
            max(0, x0 - mx),
            max(0, y0 - my),
            min(original.width, x1 + mx),
            min(original.height, y1 + my),
        )
        crop = original.crop(region).convert("RGBA")
        if not keep.any():
            out.append(crop if real_alpha else crop.convert("RGB"))
            continue
        r = max(2, round(0.015 * max(wx1 - wx0, wy1 - wy0)))
        keep = ndimage.binary_dilation(keep, structure=_disk(r))
        soft = Image.fromarray(keep.astype(np.uint8) * 255).resize(original.size, Image.Resampling.BILINEAR)
        soft = soft.crop(region)
        if real_alpha:
            a = np.minimum(np.asarray(crop.getchannel("A")), np.asarray(soft))
            crop.putalpha(Image.fromarray(a.astype(np.uint8)))
            out.append(crop)
        else:
            fill = Image.new("RGB", crop.size, backdrop)
            out.append(Image.composite(crop.convert("RGB"), fill, soft))
    return out
