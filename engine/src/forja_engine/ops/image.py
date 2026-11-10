from __future__ import annotations

import numpy as np
from numpy.typing import NDArray
from PIL import Image, ImageOps

MAX_SIDE = 4096


def normalize(img: Image.Image) -> Image.Image:
    """EXIF rotation, sRGB RGBA, longest side capped at MAX_SIDE."""
    img = ImageOps.exif_transpose(img).convert("RGBA")
    if max(img.size) > MAX_SIDE:
        img.thumbnail((MAX_SIDE, MAX_SIDE), Image.Resampling.LANCZOS)
    return img


def has_real_alpha(img: Image.Image) -> bool:
    alpha = np.asarray(img.getchannel("A"))
    return bool((alpha < 250).mean() > 0.01)


def alpha_bbox(img: Image.Image, threshold: int = 16) -> tuple[int, int, int, int]:
    alpha = np.asarray(img.getchannel("A"))
    ys, xs = np.nonzero(alpha > threshold)
    if len(xs) == 0:
        return 0, 0, img.width, img.height
    return int(np.min(xs)), int(np.min(ys)), int(np.max(xs)) + 1, int(np.max(ys)) + 1


def frame(img: Image.Image, size: int, margin: float = 0.08) -> Image.Image:
    """Crop to the subject, pad to a centered square with a margin, resize to `size`."""
    if not (np.asarray(img.getchannel("A")) > 16).any():  # the threshold alpha_bbox crops by
        # A blank or one-colour picture: the stages after this one would model nothing.
        raise ValueError(
            "No subject was found in the image: nothing is left once its background is removed. "
            "Use a picture of one subject that stands out from its background."
        )
    x0, y0, x1, y1 = alpha_bbox(img)
    subject = img.crop((x0, y0, x1, y1))
    side = int(max(subject.size) * (1 + 2 * margin))
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.paste(subject, ((side - subject.width) // 2, (side - subject.height) // 2))
    return canvas.resize((size, size), Image.Resampling.LANCZOS)


def border_key(img: Image.Image, tolerance: float = 0.12) -> Image.Image:
    """Cheap background removal: key out the color dominant along the image border.

    Good enough for concept art on flat backgrounds and for deterministic tests; the real
    path is BiRefNet through ONNX Runtime.
    """
    rgb = np.asarray(img.convert("RGB"), dtype=np.float32) / 255.0
    border = np.concatenate([rgb[0], rgb[-1], rgb[:, 0], rgb[:, -1]])
    key = np.median(border, axis=0)
    dist = np.linalg.norm(rgb - key, axis=-1)
    soft = np.clip((dist - tolerance) / tolerance, 0.0, 1.0)
    out = np.dstack([rgb, soft]).astype(np.float32)
    return Image.fromarray((out * 255).astype(np.uint8), "RGBA")


def sample(img: Image.Image, uv: NDArray[np.float64]) -> NDArray[np.float64]:
    """Bilinear-free nearest sample of RGB at normalized image coordinates (0..1, origin top-left)."""
    arr = np.asarray(img.convert("RGB"), dtype=np.float64) / 255.0
    h, w = arr.shape[:2]
    x = np.clip((uv[:, 0] * (w - 1)).round().astype(int), 0, w - 1)
    y = np.clip((uv[:, 1] * (h - 1)).round().astype(int), 0, h - 1)
    return arr[y, x]
