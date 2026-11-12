import numpy as np
import pytest
from PIL import Image
from scipy import ndimage

from forja_engine.adapters.builtin import Framing
from forja_engine.ops import image as img_ops
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType


def blocks(*boxes: tuple[int, int, int, int], size: int = 400) -> Image.Image:
    """Opaque gray rectangles (x0, y0, x1, y1) on a transparent canvas."""
    out = np.zeros((size, size, 4), dtype=np.uint8)
    for x0, y0, x1, y1 in boxes:
        out[y0:y1, x0:x1] = (200, 120, 60, 255)
    return Image.fromarray(out, "RGBA")


def test_a_stray_speck_does_not_shrink_the_subject():
    subject = (150, 150, 250, 250)
    with_speck = blocks(subject, (4, 4, 6, 6))  # 4 px against 10,000
    assert img_ops.alpha_bbox(with_speck)[:2] == (4, 4)  # the box a speck stretches
    assert np.array_equal(
        np.asarray(img_ops.frame(with_speck, 128)), np.asarray(img_ops.frame(blocks(subject), 128))
    )
    assert img_ops.natural_side(with_speck) == img_ops.natural_side(blocks(subject)) == int(100 * 1.16)


def test_a_faint_speck_goes_too():
    img = blocks((150, 150, 250, 250))
    alpha = np.asarray(img.getchannel("A")).copy()
    alpha[10:12, 380:382] = 40  # semi-transparent, above the crop threshold, tiny
    img.putalpha(Image.fromarray(alpha))
    assert img_ops.alpha_bbox(img)[2] == 382
    assert img_ops.natural_side(img) == int(100 * 1.16)


def test_a_subject_in_several_real_parts_keeps_every_part():
    # Two comparable parts far apart, and a small but real third (a floating gem: 2% of the largest).
    parts = blocks((20, 20, 120, 120), (280, 280, 380, 380), (200, 40, 214, 54))
    framed = np.asarray(img_ops.frame(parts, 256).getchannel("A")) > 16
    assert img_ops.alpha_bbox(parts) == (20, 20, 380, 380)
    assert img_ops.natural_side(parts) == int(360 * 1.16)
    # Every part is still in the frame: three separate blobs.
    assert ndimage.label(framed)[1] == 3


def test_a_part_touching_at_a_corner_belongs_to_the_subject():
    diagonal = blocks((100, 100, 200, 200), (200, 200, 201, 201))  # one pixel, joined corner to corner
    assert img_ops.drop_specks(diagonal).tobytes() == diagonal.tobytes()


def test_an_image_with_one_part_is_not_copied():
    img = blocks((100, 100, 200, 200))
    assert img_ops.drop_specks(img) is img


def test_a_blank_picture_still_says_so():
    with pytest.raises(ValueError, match="No subject"):
        img_ops.frame(blocks(), 64)


def test_the_stage_frames_without_the_speck(tmp_path):
    src = tmp_path / "bg.png"
    blocks((150, 150, 250, 250), (4, 4, 6, 6)).save(src)
    ctx = StageContext(
        "frame",
        StageType.FRAMING,
        [StageInput(src, ArtifactKind.IMAGE_PNG, "h")],
        {"size": 128},
        0,
        tmp_path,
    )
    out = Framing().run(ctx)[0].path
    assert Framing.version == "2"
    assert np.array_equal(np.asarray(Image.open(out)), img_ops.frame(blocks((150, 150, 250, 250)), 128))
