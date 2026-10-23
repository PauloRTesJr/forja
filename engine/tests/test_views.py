"""Reference sheets: finding the views of one subject in one image, and cutting them out."""

import json
from pathlib import Path

import numpy as np
import pytest
from PIL import Image, ImageDraw
from pydantic import ValidationError

from forja_engine.adapters.builtin import ViewsDetect, ViewsSplit
from forja_engine.ops import views
from forja_engine.registry import ManifestError, Registry, parse
from forja_engine.runs import StageSpec
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType

BACKDROP = (40, 40, 40)
SKIN = (150, 60, 40)


def figure(draw: ImageDraw.ImageDraw, box, kind: str) -> None:
    """A tailed creature in A-pose, seen from the front, its left side, or its back."""
    x0, y0, x1, y1 = box
    w, h = x1 - x0, y1 - y0

    def r(a, b, c, d):
        return [x0 + a * w, y0 + b * h, x0 + c * w, y0 + d * h]

    def poly(*pts):
        return [(x0 + px * w, y0 + py * h) for px, py in pts]

    if kind == "side":
        draw.ellipse(r(0.35, 0.0, 0.65, 0.12), fill=255)
        draw.rectangle(r(0.3, 0.12, 0.7, 0.55), fill=255)
        draw.rectangle(r(0.35, 0.55, 0.65, 1.0), fill=255)
        draw.rectangle(r(0.0, 0.18, 0.35, 0.24), fill=255)
        draw.polygon(poly((0.7, 0.45), (1.0, 0.85), (0.92, 0.9), (0.68, 0.55)), fill=255)
        return
    tail = ((0.62, 0.45), (0.85, 0.85), (0.78, 0.88), (0.6, 0.55))
    if kind == "back":
        tail = tuple((1 - px, py) for px, py in tail)
    draw.ellipse(r(0.42, 0.0, 0.58, 0.12), fill=255)
    draw.rectangle(r(0.38, 0.12, 0.62, 0.55), fill=255)
    draw.rectangle(r(0.0, 0.15, 1.0, 0.22), fill=255)
    draw.rectangle(r(0.38, 0.55, 0.48, 1.0), fill=255)
    draw.rectangle(r(0.52, 0.55, 0.62, 1.0), fill=255)
    draw.polygon(poly(*tail), fill=255)


def images(size, shapes) -> tuple[Image.Image, Image.Image]:
    """The original on a flat backdrop, and the cut-out background removal would make of it."""
    mask = Image.new("L", size, 0)
    draw = ImageDraw.Draw(mask)
    for box, kind in shapes:
        if kind == "detail":
            # A close-up framed in its own box: the subject runs off the frame's edges.
            inner = Image.new("L", size, 0)
            ImageDraw.Draw(inner).ellipse([box[0] - 80, box[1] + 60, box[2] + 80, box[3] + 300], fill=255)
            crop = Image.new("L", size, 0)
            ImageDraw.Draw(crop).rectangle(box, fill=255)
            mask.paste(255, mask=Image.fromarray(np.minimum(np.asarray(inner), np.asarray(crop))))
        elif kind == "caption":
            draw.rectangle(box, fill=255)
        else:
            figure(draw, box, kind)
    original = Image.composite(Image.new("RGB", size, SKIN), Image.new("RGB", size, BACKDROP), mask)
    cutout = original.copy()
    cutout.putalpha(mask)
    return original, cutout


SHEET = [
    ((100, 40, 800, 780), "front"),  # the hero render
    ((860, 90, 1190, 530), "detail"),
    ((30, 820, 400, 1190), "front"),
    ((540, 820, 690, 1190), "side"),
    ((800, 820, 1170, 1190), "back"),
    ((190, 1215, 250, 1227), "caption"),
]


def test_a_turnaround_sheet_gets_a_view_per_figure():
    _, cutout = images((1214, 1295), SHEET)
    found = views.detect(cutout)
    assert found["sheet"] and found["size"] == [1214, 1295]
    by_view = {p["view"]: p for p in found["parts"] if p["view"]}
    assert set(by_view) == {"front", "left", "back"}
    # The hero is the same view as the small front, with more pixels: it takes the front's place.
    assert by_view["front"]["box"][1] < 100 and by_view["left"]["box"][0] > 500
    kinds = [p["kind"] for p in found["parts"]]
    assert kinds.count("detail") == 1 and kinds.count("figure") == 4  # the caption is too small
    left_out = [p for p in found["parts"] if not p["view"]]
    assert {p["kind"] for p in left_out} == {"detail", "figure"}


def test_boxes_are_in_full_resolution_pixels():
    big = [((x0 * 3, y0 * 3, x1 * 3, y1 * 3), kind) for (x0, y0, x1, y1), kind in SHEET]
    _, cutout = images((1214 * 3, 1295 * 3), big)
    found = views.detect(cutout)
    left = next(p for p in found["parts"] if p["view"] == "left")
    assert left["box"] == pytest.approx([540 * 3, 820 * 3, 690 * 3, 1190 * 3], abs=12)


@pytest.mark.parametrize(
    "shapes",
    [
        [((300, 100, 700, 900), "front")],
        # A set of props: one outline, three times.
        [((50, 300, 250, 700), "side"), ((350, 300, 550, 700), "side"), ((650, 300, 850, 700), "side")],
        # A character and a small prop beside it.
        [((100, 100, 500, 900), "front"), ((600, 700, 800, 900), "side")],
    ],
    ids=["one figure", "copies", "different heights"],
)
def test_other_images_are_not_sheets(shapes):
    _, cutout = images((1000, 1000), shapes)
    found = views.detect(cutout)
    assert not found["sheet"] and all(p["view"] == "" for p in found["parts"])


def test_split_paints_neighbours_over_with_the_backdrop():
    original, cutout = images((400, 260), [((20, 20, 100, 240), "side"), ((200, 20, 300, 240), "side")])
    # A long arm from the right figure reaches into the left one's crop without touching it.
    for img, colour in ((original, SKIN), (cutout, (*SKIN, 255))):
        ImageDraw.Draw(img).rectangle([112, 80, 200, 90], fill=colour)
    (out,) = views.split(original, cutout, [(20, 20, 100, 240)], margin=0.25)
    # The margin runs past the image's top-left corner, so crop pixels are image pixels.
    assert out.mode == "RGB" and out.size == (120, 260)
    assert out.getpixel((116, 85)) == BACKDROP
    assert out.getpixel((60, 150)) == SKIN


def test_split_keeps_an_original_alpha():
    original, cutout = images((400, 260), [((20, 20, 100, 240), "side"), ((200, 20, 300, 240), "side")])
    original = cutout.copy()  # the artist already cut the sheet out
    ImageDraw.Draw(original).rectangle([112, 80, 200, 90], fill=(*SKIN, 255))
    (out,) = views.split(original, cutout, [(20, 20, 100, 240)], margin=0.25)
    assert out.mode == "RGBA" and out.getpixel((116, 85))[3] == 0 and out.getpixel((60, 150))[3] == 255


def test_split_refuses_a_box_outside_the_image():
    original, cutout = images((100, 100), [((10, 10, 90, 90), "side")])
    with pytest.raises(ValueError, match="outside"):
        views.split(original, cutout, [(0, 0, 200, 50)])


def stage(tmp_path, task, inputs, **params):
    return StageContext(
        "s",
        task,
        [StageInput(Path(p), ArtifactKind.IMAGE_PNG, "", v) for p, v in inputs],
        params,
        1,
        tmp_path,
    )


def test_builtin_stages_detect_then_split(tmp_path):
    original, cutout = images((1214, 1295), SHEET)
    original.save(tmp_path / "sheet.png")
    cutout.save(tmp_path / "cutout.png")
    (out,) = ViewsDetect().run(stage(tmp_path, StageType.VIEWS_DETECT, [(tmp_path / "cutout.png", None)]))
    assert out.kind == ArtifactKind.JSON
    found = json.loads(out.path.read_text())
    chosen = [{"view": p["view"], "box": p["box"]} for p in found["parts"] if p["view"]]
    split = ViewsSplit().run(
        stage(
            tmp_path,
            StageType.VIEWS_SPLIT,
            [(tmp_path / "sheet.png", None), (tmp_path / "cutout.png", None)],
            views=chosen,
        )
    )
    assert [o.path.stem for o in split] == [c["view"] for c in chosen]
    front = Image.open(next(o.path for o in split if o.path.stem == "front"))
    assert front.height > 700  # the hero, not the small front


def test_split_needs_a_front_and_each_view_once(tmp_path):
    original, cutout = images((200, 200), [((10, 10, 190, 190), "side")])
    original.save(tmp_path / "o.png")
    cutout.save(tmp_path / "c.png")
    inputs = [(tmp_path / "o.png", None), (tmp_path / "c.png", None)]
    box = [10, 10, 190, 190]
    for wanted in ([{"view": "left", "box": box}], [{"view": "front", "box": box}] * 2):
        with pytest.raises(ValueError, match="front"):
            ViewsSplit().run(stage(tmp_path, StageType.VIEWS_SPLIT, inputs, views=wanted))


def test_untagged_images_are_the_front_and_tags_name_the_rest(tmp_path):
    ctx = stage(tmp_path, StageType.SHAPE, [(tmp_path / "a.png", None), (tmp_path / "b.png", "back")])
    got = ctx.views()
    assert list(got) == ["front", "back"] and got["back"].path.name == "b.png"


def test_stage_specs_label_every_input_with_a_known_view():
    StageSpec(id="s", use=StageType.SHAPE, inputs=["stage:a", "stage:b"], views=["front", "left"])
    StageSpec(id="s", use=StageType.TEXTURE, inputs=["stage:m", "stage:a"], views=["", "front"])
    with pytest.raises(ValidationError, match="label each input"):
        StageSpec(id="s", use=StageType.SHAPE, inputs=["stage:a", "stage:b"], views=["front"])
    with pytest.raises(ValidationError, match="unknown view"):
        StageSpec(id="s", use=StageType.SHAPE, inputs=["stage:a"], views=["top"])


def test_manifests_list_the_views_their_model_reads():
    models = Registry.load().models
    assert models["hunyuan3d2-mv"].views == ("front", "left", "back", "right")
    assert models["triposg"].views == () and models["triposg"].to_json()["views"] == []
    doc = {
        "id": "m",
        "family": "stub",
        "adapter": "a:B",
        "tasks": ["shape"],
        "license": {"spdx": "MIT", "commercial": True},
    }
    assert parse({**doc, "views": ["back", "front"]}, "m").views == ("front", "back")
    for bad in (["left"], ["front", "top"]):
        with pytest.raises(ManifestError, match="views"):
            parse({**doc, "views": bad}, "m")
